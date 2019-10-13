package cli

import (
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/elves/elvish/cli/term"
	"github.com/elves/elvish/edit/ui"
	"github.com/elves/elvish/sys"
)

// TTY is the type the terminal dependency of the editor needs to satisfy.
type TTY interface {
	// Configures the terminal at the beginning of Editor.ReadCode. It returns a
	// restore function to be called at the end of Editor.ReadCode and any
	// error. Errors are always considered fatal and will make ReadCode abort;
	// non-fatal errors should be handled by Setup itself (e.g. by showing a
	// warning message) instead of being returned.
	Setup() (restore func(), err error)

	// Starts the delivery of terminal events and returns a channel on which
	// events are made available.
	StartInput() <-chan term.Event
	// Sets the "raw input" mode of the terminal. The raw input mode is
	// applicable when terminal events are delivered as escape sequences; the
	// raw input mode will cause those escape sequences to be interpreted as
	// individual key events. If the concept is not applicable, this method is a
	// no-op.
	SetRawInput(raw bool)
	// Causes input delivery to be stopped. When this function returns, the
	// channel previously returned by StartInput should no longer deliver
	// events.
	StopInput()

	// Returns the height and width of the terminal.
	Size() (h, w int)

	// Returns the current buffer. The initial value of the current buffer is
	// nil.
	Buffer() *ui.Buffer
	// Resets the current buffer to nil without actuating any redraw.
	ResetBuffer()
	// Updates the current buffer and draw it to the terminal.
	UpdateBuffer(bufNotes, bufMain *ui.Buffer, full bool) error

	// Start relaying signals and returns a channel on which signals are
	// delivered.
	NotifySignals() <-chan os.Signal
	// Stops the relaying of signals. After this function returns, the
	// channel returned by NotifySignals will not deliver any more values.
	StopSignals()
}

type aTTY struct {
	in, out *os.File
	r       term.Reader
	w       term.Writer
	sigCh   chan os.Signal
}

const sigsChanBufferSize = 256

// NewTTY returns a new TTY from input and output terminal files.
func NewTTY(in, out *os.File) TTY {
	return &aTTY{in, out, nil, term.NewWriter(out), nil}
}

// NewStdIOTTY returns a new TTY from stdin and stderr.
func NewStdTTY() TTY {
	return NewTTY(os.Stdin, os.Stderr)
}

func (t *aTTY) Setup() (func(), error) {
	restore, err := term.Setup(t.in, t.out)
	return func() {
		err := restore()
		if err != nil {
			fmt.Println(t.out, "failed to restore terminal properties:", err)
		}
	}, err
}

func (t *aTTY) Size() (h, w int) {
	return sys.GetWinsize(t.out)
}

func (t *aTTY) StartInput() <-chan term.Event {
	t.r = term.NewReader(t.in)
	t.r.Start()
	return t.r.EventChan()
}

func (t *aTTY) SetRawInput(raw bool) {
	t.r.SetRaw(raw)
}

func (t *aTTY) StopInput() {
	t.r.Stop()
	t.r.Close()
	t.r = nil
}

func (t *aTTY) Buffer() *ui.Buffer {
	return t.w.CurrentBuffer()
}

func (t *aTTY) ResetBuffer() {
	t.w.ResetCurrentBuffer()
}

func (t *aTTY) UpdateBuffer(bufNotes, bufMain *ui.Buffer, full bool) error {
	return t.w.CommitBuffer(bufNotes, bufMain, full)
}

func (t *aTTY) NotifySignals() <-chan os.Signal {
	t.sigCh = make(chan os.Signal, sigsChanBufferSize)
	signal.Notify(t.sigCh)
	return t.sigCh
}

func (t *aTTY) StopSignals() {
	signal.Stop(t.sigCh)
	close(t.sigCh)
	t.sigCh = nil
}

const (
	// Maximum number of buffer updates FakeTTY expect to see.
	fakeTTYBufferUpdates = 1024
	// Maximum number of events FakeTTY produces.
	fakeTTYEvents = 1024
	// Maximum number of signals FakeTTY produces.
	fakeTTYSignals = 1024
)

// An implementation of the TTY interface that is useful in tests.
type fakeTTY struct {
	setup func() (func(), error)
	// Channel that StartRead returns. Can be used to inject additional events.
	eventCh chan term.Event
	// Channel for publishing updates of the main buffer and notes buffer.
	bufCh, notesBufCh chan *ui.Buffer
	// Records history of the main buffer and notes buffer.
	bufs, notesBufs []*ui.Buffer
	// Channel that NotifySignals returns. Can be used to inject signals.
	sigCh chan os.Signal

	sizeMutex sync.RWMutex
	// Predefined sizes.
	height, width int
}

// NewFakeTTY creates a new FakeTTY and a handle for controlling it.
func NewFakeTTY() (TTY, TTYCtrl) {
	tty := &fakeTTY{
		eventCh:    make(chan term.Event, fakeTTYEvents),
		sigCh:      make(chan os.Signal, fakeTTYSignals),
		bufCh:      make(chan *ui.Buffer, fakeTTYBufferUpdates),
		notesBufCh: make(chan *ui.Buffer, fakeTTYBufferUpdates),
		height:     24, width: 80,
	}
	return tty, TTYCtrl{tty}
}

// Delegates to the setup function specified using the SetSetup method of
// TTYCtrl, or return a nop function and a nil error.
func (t *fakeTTY) Setup() (func(), error) {
	if t.setup == nil {
		return func() {}, nil
	}
	return t.setup()
}

// Returns the size specified by using the SetSize method of TTYCtrl.
func (t *fakeTTY) Size() (h, w int) {
	t.sizeMutex.RLock()
	defer t.sizeMutex.RUnlock()
	return t.height, t.width
}

// Returns t.eventCh. Events may be injected onto the channel by using the
// TTYCtrl.
func (t *fakeTTY) StartInput() <-chan term.Event {
	return t.eventCh
}

// Nop.
func (t *fakeTTY) SetRawInput(b bool) {}

// Nop.
func (t *fakeTTY) StopInput() { close(t.eventCh) }

// Nop.
func (t *fakeTTY) Newline() {}

// Returns the last recorded buffer.
func (t *fakeTTY) Buffer() *ui.Buffer { return t.bufs[len(t.bufs)-1] }

// Records a nil buffer.
func (t *fakeTTY) ResetBuffer() { t.recordBuf(nil) }

// UpdateBuffer records a new pair of buffers, i.e. sending them to their
// respective channels and appending them to their respective slices.
func (t *fakeTTY) UpdateBuffer(bufNotes, buf *ui.Buffer, _ bool) error {
	t.recordNotesBuf(bufNotes)
	t.recordBuf(buf)
	return nil
}

func (t *fakeTTY) NotifySignals() <-chan os.Signal { return t.sigCh }

func (t *fakeTTY) StopSignals() { close(t.sigCh) }

func (t *fakeTTY) recordBuf(buf *ui.Buffer) {
	t.bufs = append(t.bufs, buf)
	t.bufCh <- buf
}

func (t *fakeTTY) recordNotesBuf(buf *ui.Buffer) {
	t.notesBufs = append(t.notesBufs, buf)
	t.notesBufCh <- buf
}

// TTYCtrl is an interface for controlling a fake terminal.
type TTYCtrl struct{ *fakeTTY }

// SetSetup changes the return values of the Setup method of the fake terminal.
func (t TTYCtrl) SetSetup(restore func(), err error) {
	t.setup = func() (func(), error) {
		return restore, err
	}
}

// SetSize sets the size of the fake terminal.
func (t TTYCtrl) SetSize(h, w int) {
	t.sizeMutex.Lock()
	defer t.sizeMutex.Unlock()
	t.height, t.width = h, w
}

// Inject injects events to the fake terminal.
func (t TTYCtrl) Inject(events ...term.Event) {
	for _, event := range events {
		t.eventCh <- event
	}
}

// InjectSignal injects signals.
func (t TTYCtrl) InjectSignal(sigs ...os.Signal) {
	for _, sig := range sigs {
		t.sigCh <- sig
	}
}

// VerifyBuffer verifies that a buffer will appear within the timeout of 4
// seconds.
func (t TTYCtrl) VerifyBuffer(b *ui.Buffer) bool {
	return verifyBuffer(b, t.bufCh)
}

// TestBuffer verifies that a buffer will appear within the timeout of 4
// seconds, and fails the test if it doesn't
func (t TTYCtrl) TestBuffer(tt *testing.T, b *ui.Buffer) {
	tt.Helper()
	testBuffer(tt, b, t.bufCh, t.LastBuffer)
}

// VerifyNotesBuffer verifies the a notes buffer will appear within the timeout
// of 4 seconds.
func (t TTYCtrl) VerifyNotesBuffer(b *ui.Buffer) bool {
	return verifyBuffer(b, t.notesBufCh)
}

// TestNotesBuffer verifies that a notes buffer will appear within the timeout of 4
// seconds, and fails the test if it doesn't
func (t TTYCtrl) TestNotesBuffer(tt *testing.T, b *ui.Buffer) {
	tt.Helper()
	testBuffer(tt, b, t.notesBufCh, t.LastNotesBuffer)
}

// BufferHistory returns a slice of all buffers that have appeared.
func (t TTYCtrl) BufferHistory() []*ui.Buffer { return t.bufs }

// LastBuffer returns the last buffer that has appeared.
func (t TTYCtrl) LastBuffer() *ui.Buffer {
	if len(t.bufs) == 0 {
		return nil
	}
	return t.bufs[len(t.bufs)-1]
}

// NotesBufferHistory returns a slice of all notes buffers that have appeared.
func (t TTYCtrl) NotesBufferHistory() []*ui.Buffer { return t.notesBufs }

func (t TTYCtrl) LastNotesBuffer() *ui.Buffer {
	if len(t.notesBufs) == 0 {
		return nil
	}
	return t.notesBufs[len(t.notesBufs)-1]
}

var verifyBufferTimeout = 4000 * time.Millisecond

// Check that an expected buffer will eventually appear. Also useful for waiting
// until the editor reaches a certain state.
func verifyBuffer(want *ui.Buffer, ch <-chan *ui.Buffer) bool {
	for {
		select {
		case buf := <-ch:
			if reflect.DeepEqual(buf, want) {
				return true
			}
		case <-time.After(verifyBufferTimeout):
			return false
		}
	}
}

func testBuffer(t *testing.T, want *ui.Buffer, ch <-chan *ui.Buffer, last func() *ui.Buffer) {
	t.Helper()
	if verifyBuffer(want, ch) {
		return
	}
	t.Errorf("Wanted buffer not shown")
	t.Logf("Want: %s", want.TTYString())
	t.Logf("Last buffer: %s", last().TTYString())
}
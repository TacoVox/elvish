// Package prompt implements the prompt subsystem of the editor.
package prompt

import (
	"io/ioutil"
	"math"
	"os"
	"sync"
	"time"

	"github.com/elves/elvish/edit/eddefs"
	"github.com/elves/elvish/edit/ui"
	"github.com/elves/elvish/eval"
	"github.com/elves/elvish/eval/vals"
	"github.com/elves/elvish/eval/vars"
	"github.com/elves/elvish/util"
)

var logger = util.GetLogger("[edit/prompt] ")

// Init initializes the prompt subsystem of the editor.
func Init(ed eddefs.Editor, ns eval.Ns) {
	prompt := makePrompt(ed, defaultPrompt)
	rprompt := makePrompt(ed, defaultRPrompt)
	ed.SetPrompt(prompt)
	ed.SetRPrompt(rprompt)
	installAPI(ns, prompt, "prompt")
	installAPI(ns, rprompt, "rprompt")
}

func installAPI(ns eval.Ns, p *prompt, basename string) {
	ns.Add(basename, vars.NewFromPtr(&p.fn))
	ns.Add(basename+"-stale-transform", vars.NewFromPtr(&p.staleTransform))
	ns.Add("-"+basename+"-eagerness", vars.NewFromPtr(&p.eagerness))
	ns.Add("-"+basename+"-max-wait", vars.NewFromPtr(&p.maxWait))
}

type prompt struct {
	ed eddefs.Editor
	// The main callback.
	fn eval.Callable
	// Callback used to transform stale prompts.
	staleTransform eval.Callable
	// Maximum time to block waiting for prompt callback.
	maxWait float64
	// How eager the prompt should be updated. When >= 5, updated when directory
	// is changed. When >= 10, always update. Default is 5.
	eagerness int

	// Prompt content from last time.
	last []*ui.Styled
	// Working directory when prompt was last updated.
	lastWd string
	// Channel on which prompt contents are sent.
	ch chan []*ui.Styled
}

func makePrompt(ed eddefs.Editor, fn eval.Callable) *prompt {
	return &prompt{
		ed, fn, defaultStaleTransform, math.Inf(1), 5,
		nil, "", make(chan []*ui.Styled)}
}

func (p *prompt) Chan() <-chan []*ui.Styled {
	return p.ch
}

func (p *prompt) Update(force bool) []*ui.Styled {
	if !force && !p.shouldUpdate() {
		return p.last
	}

	timeout := makeMaxWaitChan(p.maxWait)
	ch := make(chan []*ui.Styled)
	go func() {
		content := callPrompt(p.ed, p.fn)
		p.last = content
		ch <- result
	}()
	select {
	case <-timeout:
		go func() {
			p.ch <- <-ch
		}()
		return callTransformer(p.ed, p.staleTransform, p.last)
	case content := <-ch:
		return content
	}
}

func (p *prompt) shouldUpdate() bool {
	if p.eagerness >= 10 {
		return true
	}
	if p.eagerness >= 5 {
		wd, err := os.Getwd()
		if err != nil {
			wd = "error"
		}
		oldWd := p.lastWd
		p.lastWd = wd
		return wd != oldWd
	}
	return false
}

// maxSeconds is the maximum number of seconds time.Duration can represent.
const maxSeconds = float64(math.MaxInt64 / time.Second)

// makeMaxWaitChan makes a channel that sends the current time after f seconds.
// If f does not fits in a time.Duration value, it returns nil, which is a
// channel that never sends any value.
func makeMaxWaitChan(f float64) <-chan time.Time {
	if f > maxSeconds {
		return nil
	}
	return time.After(time.Duration(f * float64(time.Second)))
}

// callPrompt calls a function with no arguments and closed input, and converts
// its outputs to styled objects. Used to call prompt callbacks.
func callPrompt(ed eddefs.Editor, fn eval.Callable) []*ui.Styled {
	ports := []*eval.Port{
		eval.DevNullClosedChan,
		{}, // Will be replaced when capturing output
		{File: os.Stderr},
	}

	return callAndGetStyled(ed, fn, ports)
}

// callTransformer calls a function with no arguments and the given inputs, and
// converts its outputs to styled objects. Used to call stale transformers.
func callTransformer(ed eddefs.Editor, fn eval.Callable, currentPrompt []*ui.Styled) []*ui.Styled {
	input := make(chan interface{})
	stopInputWriter := make(chan struct{})

	ports := []*eval.Port{
		{Chan: input, File: eval.DevNull},
		{}, // Will be replaced when capturing output
		{File: os.Stderr},
	}
	go func() {
		defer close(input)
		for _, char := range currentPrompt {
			select {
			case input <- char:
			case <-stopInputWriter:
				return
			}
		}
	}()
	defer close(stopInputWriter)

	return callAndGetStyled(ed, fn, ports)
}

func callAndGetStyled(ed eddefs.Editor, fn eval.Callable, ports []*eval.Port) []*ui.Styled {
	var (
		styleds      []*ui.Styled
		styledsMutex sync.Mutex
	)
	add := func(s *ui.Styled) {
		styledsMutex.Lock()
		styleds = append(styleds, s)
		styledsMutex.Unlock()
	}
	// Value output may be of type ui.Styled or any other type, in which case
	// they are converted to ui.Styled.
	valuesCb := func(ch <-chan interface{}) {
		for v := range ch {
			if s, ok := v.(*ui.Styled); ok {
				add(s)
			} else {
				add(&ui.Styled{vals.ToString(v), ui.Styles{}})
			}
		}
	}
	// Byte output is added to the prompt as a single unstyled text.
	bytesCb := func(r *os.File) {
		allBytes, err := ioutil.ReadAll(r)
		if err != nil {
			logger.Println("error reading prompt byte output:", err)
		}
		if len(allBytes) > 0 {
			add(&ui.Styled{string(allBytes), ui.Styles{}})
		}
	}

	// XXX There is no source to pass to NewTopEvalCtx.
	ec := eval.NewTopFrame(ed.Evaler(), eval.NewInternalSource("[prompt]"), ports)
	err := ec.CallWithOutputCallback(fn, nil, eval.NoOpts, valuesCb, bytesCb)

	if err != nil {
		ed.Notify("prompt function error: %v", err)
		return nil
	}

	return styleds
}

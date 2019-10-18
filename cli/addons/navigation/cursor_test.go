package navigation

import (
	"errors"
	"os"
)

var (
	errCannotCd   = errors.New("cannot cd")
	errNoSuchFile = errors.New("no such file")
	errNoSuchDir  = errors.New("no such directory")
)

type testCursor struct {
	root dir
	pwd  []string

	currentErr, parentErr, ascendErr, descendErr error
}

func (c *testCursor) Current() (File, error) {
	if c.currentErr != nil {
		return nil, c.currentErr
	}
	return getDirFile(c.root, c.pwd)
}

func (c *testCursor) Parent() (File, error) {
	if c.parentErr != nil {
		return nil, c.parentErr
	}
	parent := c.pwd
	if len(parent) > 0 {
		parent = parent[:len(parent)-1]
	}
	return getDirFile(c.root, parent)
}

func (c *testCursor) Ascend() error {
	if c.ascendErr != nil {
		return c.ascendErr
	}
	if len(c.pwd) > 0 {
		c.pwd = c.pwd[:len(c.pwd)-1]
	}
	return nil
}

func (c *testCursor) Descend(name string) error {
	if c.descendErr != nil {
		return c.descendErr
	}
	pwdCopy := append([]string{}, c.pwd...)
	childPath := append(pwdCopy, name)
	if _, err := getDirFile(c.root, childPath); err == nil {
		c.pwd = childPath
		return nil
	}
	return errCannotCd
}

func getFile(root dir, path []string) (File, error) {
	var f interface{} = root
	for _, p := range path {
		d, ok := f.(dir)
		if !ok {
			return nil, errNoSuchFile
		}
		f = d[p]
	}
	name := ""
	if len(path) > 0 {
		name = path[len(path)-1]
	}
	return testFile{name, f}, nil
}

func getDirFile(root dir, path []string) (File, error) {
	f, err := getFile(root, path)
	if err != nil {
		return nil, err
	}
	if f.Mode()&os.ModeDir == 0 {
		return nil, errNoSuchDir
	}
	return f, nil
}

type testFile struct {
	name string
	data interface{}
}

func (f testFile) Name() string { return f.name }

func (f testFile) Mode() os.FileMode {
	if _, ok := f.data.(dir); ok {
		return os.ModeDir
	}
	return 0
}

func (f testFile) DeepMode() (os.FileMode, error) { return f.Mode(), nil }

func (f testFile) Read() ([]File, []byte, error) {
	if dir, ok := f.data.(dir); ok {
		files := make([]File, 0, len(dir))
		for name, data := range dir {
			files = append(files, testFile{name, data})
		}
		return files, nil, nil
	}
	return nil, []byte(f.data.(string)), nil
}
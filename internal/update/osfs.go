package update

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// OSFS is FS on the real file system.
type OSFS struct{}

// MkdirAll creates dir and its parents.
func (OSFS) MkdirAll(dir string) error { return os.MkdirAll(dir, 0o755) }

// Create creates or truncates path.
func (OSFS) Create(path string) (io.WriteCloser, error) { return os.Create(path) }

// Open opens path for reading.
func (OSFS) Open(path string) (io.ReadCloser, error) { return os.Open(path) }

// Rename renames (on Windows: MoveFileEx with MOVEFILE_REPLACE_EXISTING).
func (OSFS) Rename(from, to string) error { return os.Rename(from, to) }

// Remove deletes path; a missing file is not an error.
func (OSFS) Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// List returns the names of the regular files in dir (nil if dir is missing).
func (OSFS) List(dir string) ([]string, error) {
	es, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if e.Type().IsRegular() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// OSStarter starts a process with os.StartProcess in the folder of the
// executable and does not wait for it. Its standard handles are the null
// device (a GUI parent has none to pass on); nothing else is inherited.
type OSStarter struct{}

// Start starts exe with args.
func (OSStarter) Start(exe string, args []string) error {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	p, err := os.StartProcess(exe, append([]string{exe}, args...),
		&os.ProcAttr{Dir: filepath.Dir(exe), Files: []*os.File{null, null, null}})
	if err != nil {
		return err
	}
	return p.Release()
}

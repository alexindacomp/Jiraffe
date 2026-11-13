// Package applog provides a size-rotated log file writer for the standard
// log package.
package applog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"jiraffe/internal/filelock"
)

// rotateRetry is the pause after a failed rotation (e.g. another process
// still has the file open): the log keeps growing meanwhile.
const rotateRetry = time.Minute

// lockTimeout bounds the wait for another process's rotation.
const lockTimeout = time.Second

// Writer appends to a file and rotates it to path+".1" when it grows beyond
// maxBytes. It is safe for concurrent use. Several processes may write the
// same file: rotations are serialized by a lock file (path+".lock") and
// re-checked under it, a .1 file is only ever replaced by the rename of a
// real rotation step (never deleted), and a rotation that fails never loses
// the line being written.
type Writer struct {
	path     string
	lock     string
	maxBytes int64

	mu      sync.Mutex
	f       *os.File
	size    int64
	retryAt time.Time // no rotation attempt before this (after a failure)

	// test hooks
	now    func() time.Time
	rename func(from, to string) error
}

// Open opens (creating directories as needed) the log file at path.
func Open(path string, maxBytes int64) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	w := &Writer{path: path, lock: path + ".lock", maxBytes: maxBytes, now: time.Now, rename: os.Rename}
	if err := w.open(); err != nil {
		return nil, err
	}
	if w.size >= maxBytes {
		w.rotate() // best effort: a log that cannot rotate still logs
		if w.f == nil {
			return nil, fmt.Errorf("open log: %s", path)
		}
	}
	return w, nil
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log: %w", err)
	}
	w.f, w.size = f, st.Size()
	return nil
}

// rotate moves the file to path+".1" (replacing the previous one) and
// starts a new one. Under the lock it first re-checks the file: if another
// process already rotated it (path is no longer the file this writer
// appends to), it only switches to the new file. On failure it keeps (or
// reopens) the current file and waits rotateRetry before the next attempt.
// w.f is nil afterwards only if the file cannot be opened at all.
func (w *Writer) rotate() {
	if w.now().Before(w.retryAt) {
		return
	}
	unlock, err := filelock.Lock(w.lock, lockTimeout)
	if err != nil {
		w.retryAt = w.now().Add(rotateRetry)
		return
	}
	defer unlock()
	if w.f != nil {
		if mine, err1 := w.f.Stat(); err1 == nil {
			if cur, err2 := os.Stat(w.path); err2 == nil && !os.SameFile(mine, cur) {
				w.f.Close()
				w.f = nil
				_ = w.open() // rotated by another process: continue in its new file
				return
			}
		}
		w.f.Close()
		w.f = nil
	}
	if err := w.rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		w.retryAt = w.now().Add(rotateRetry)
		if w.open() == nil {
			fmt.Fprintf(w.f, "%s applog: rotate failed (%v); continuing in the same file\n", w.now().Format("2006/01/02 15:04:05"), err)
		}
		return
	}
	_ = w.open()
}

// Write implements io.Writer.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.size+int64(len(p)) > w.maxBytes && w.size > 0 {
		w.rotate()
		if w.f == nil {
			if err := w.open(); err != nil {
				return 0, err
			}
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

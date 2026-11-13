// Package filelock provides an exclusive lock on a file shared between
// processes (LockFileEx on Windows, flock elsewhere). It guards
// read-modify-write cycles of files that several Jiraffe processes write.
package filelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrTimeout is returned when the lock is still held by someone else after
// the timeout.
var ErrTimeout = errors.New("lock is held by another process")

// retryEvery is how often a held lock is retried.
const retryEvery = 10 * time.Millisecond

// Lock takes an exclusive lock on path (created if missing, with its
// directory), waiting up to timeout. The returned function releases it.
func Lock(path string, timeout time.Duration) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		held, err := tryLock(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", filepath.Base(path), err)
		}
		if !held {
			return func() { release(f); f.Close() }, nil
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", filepath.Base(path), ErrTimeout)
		}
		time.Sleep(retryEvery)
	}
}

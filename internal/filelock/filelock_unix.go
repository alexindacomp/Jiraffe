//go:build unix

package filelock

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes the lock without waiting; held reports that another open
// file description has it (flock locks are per open file, so two Lock calls
// in one process exclude each other too).
func tryLock(f *os.File) (held bool, err error) {
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	return false, err
}

func release(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

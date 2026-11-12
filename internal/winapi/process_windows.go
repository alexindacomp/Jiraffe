package winapi

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// ErrWaitTimeout is returned by WaitProcessExit when the process is still
// running after the timeout.
var ErrWaitTimeout = errors.New("the process is still running")

// WaitProcessExit waits up to timeout for process pid to exit. It returns
// nil when the process is gone (or never existed), ErrWaitTimeout when it
// is still running after timeout, and another error when it cannot be
// waited for (e.g. access denied).
func WaitProcessExit(pid uint32, timeout time.Duration) error {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil // no such process (any more)
	}
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	switch {
	case err != nil:
		return fmt.Errorf("wait for process %d: %w", pid, err)
	case ev == windows.WAIT_OBJECT_0:
		return nil
	case ev == uint32(windows.WAIT_TIMEOUT):
		return ErrWaitTimeout
	}
	return fmt.Errorf("wait for process %d: result %#x", pid, ev)
}

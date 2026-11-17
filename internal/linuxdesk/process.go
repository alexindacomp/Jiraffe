//go:build linux

package linuxdesk

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrWaitTimeout: the process still runs after the timeout.
var ErrWaitTimeout = errors.New("timed out")

// WaitProcessExit waits until process pid is gone.
func WaitProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return err
		}
		if time.Now().After(deadline) {
			return ErrWaitTimeout
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Instance is the single-instance lock: an abstract Unix socket named after
// the user, freed by the kernel when the process ends.
type Instance struct{ l net.Listener }

func instanceAddr() string { return "@jiraffe-" + strconv.Itoa(os.Getuid()) }

// AcquireSingleInstance takes the lock. When another instance holds it, it
// asks that one to show its window and returns nil, false.
func AcquireSingleInstance() (*Instance, bool, error) {
	l, err := net.Listen("unix", instanceAddr())
	if err == nil {
		return &Instance{l: l}, true, nil
	}
	c, derr := net.DialTimeout("unix", instanceAddr(), 2*time.Second)
	if derr != nil {
		return nil, false, err // neither listening nor reachable: run anyway
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = c.Write([]byte("show\n"))
	return nil, false, nil
}

// Serve calls onShow for every "show" request (from a goroutine).
func (in *Instance) Serve(onShow func()) {
	if in == nil {
		return
	}
	go func() {
		for {
			c, err := in.l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				buf := make([]byte, 16)
				n, _ := c.Read(buf)
				if strings.TrimSpace(string(buf[:n])) == "show" {
					onShow()
				}
			}()
		}
	}()
}

// OpenURL opens an http(s) URL in the default browser (xdg-open).
func OpenURL(u string) error {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return errors.New("only http(s) links are opened")
	}
	tool, err := exec.LookPath("xdg-open")
	if err != nil {
		if tool, err = exec.LookPath("gio"); err != nil {
			return errors.New("neither xdg-open nor gio is installed")
		}
		return startDetached(tool, "open", u)
	}
	return startDetached(tool, u)
}

func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

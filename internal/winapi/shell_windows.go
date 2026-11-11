package winapi

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// OpenURL opens an http(s) URL in the default browser.
func OpenURL(u string) error {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return fmt.Errorf("refusing to open non-http URL")
	}
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(u)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// Message box styles.
const (
	MBIconError   = 0x10
	MBIconWarning = 0x30
	MBIconInfo    = 0x40
)

// MessageBox shows a modal message box.
func MessageBox(title, text string, style uint32) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	_, _ = windows.MessageBox(0, m, t, style|0x00010000 /* MB_SETFOREGROUND */)
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetAutostart writes (HKCU Run → "<exe>" --minimized) or removes the
// autostart entry named name.
func SetAutostart(name string, enabled bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer k.Close()
	if !enabled {
		if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("delete Run value: %w", err)
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate exe: %w", err)
	}
	want := `"` + exe + `" --minimized`
	if cur, _, err := k.GetStringValue(name); err == nil && cur == want {
		return nil
	}
	if err := k.SetStringValue(name, want); err != nil {
		return fmt.Errorf("write Run value: %w", err)
	}
	return nil
}

// AutostartEnabled reports whether the Run value name exists and points to
// the current executable.
func AutostartEnabled(name string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("open Run key: %w", err)
	}
	defer k.Close()
	cur, _, err := k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Run value: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("locate exe: %w", err)
	}
	if !autostartPointsTo(cur, exe) {
		return false, nil
	}
	return startupApproved(name)
}

const startupApprovedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`

// startupApproved reads (never writes) the Task Manager / Startup Apps
// switch for name.
func startupApproved(name string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, startupApprovedKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return true, nil
		}
		return false, fmt.Errorf("open StartupApproved key: %w", err)
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read StartupApproved value: %w", err)
	}
	return startupApprovedValue(b), nil
}

// AcquireSingleInstance creates the named mutex. It returns false if another
// instance already owns it. The handle is intentionally kept open for the
// process lifetime.
func AcquireSingleInstance(name string) (bool, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	h, _, e := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(n)))
	if h == 0 {
		return false, fmt.Errorf("CreateMutex: %w", e)
	}
	if errno, ok := e.(syscall.Errno); ok && errno == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(windows.Handle(h))
		return false, nil
	}
	return true, nil
}

// SignalOtherInstance finds the running instance's hidden window by class
// name and posts msg to it, allowing it to take the foreground.
func SignalOtherInstance(className string, msg uint32) bool {
	cls, _ := windows.UTF16PtrFromString(className)
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
	if hwnd == 0 {
		return false
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != 0 {
		procAllowSetForegroundWindow.Call(uintptr(pid))
	}
	r, _, _ := procPostMessageW.Call(hwnd, uintptr(msg), 0, 0)
	return r != 0
}

// IsWindow reports whether hwnd is an existing window.
func IsWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	r, _, _ := procIsWindow.Call(hwnd)
	return r != 0
}

// BringToFront restores (if minimized), shows and focuses a top-level window.
func BringToFront(hwnd uintptr) {
	if !IsWindow(hwnd) {
		return
	}
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		procShowWindow.Call(hwnd, SWRestore)
	} else {
		procShowWindow.Call(hwnd, SWShow)
	}
	procSetForegroundWindow.Call(hwnd)
}

// SystemDPI returns the system DPI (96 = 100%).
func SystemDPI() int {
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d > 0 {
			return int(d)
		}
	}
	return 96
}

type rect struct{ Left, Top, Right, Bottom int32 }

// WorkArea returns the primary monitor work area size in pixels.
func WorkArea() (w, h int) {
	var r rect
	const spiGetWorkArea = 0x0030
	if ok, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); ok != 0 {
		return int(r.Right - r.Left), int(r.Bottom - r.Top)
	}
	cx, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	cy, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	return int(cx), int(cy)
}

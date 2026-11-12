package winapi

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	// dwmapi.dll is loaded on first use (Find), so a system without it only
	// loses the coloured title bar.
	modDwmapi                 = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = modDwmapi.NewProc("DwmSetWindowAttribute")

	procSetWindowPos        = modUser32.NewProc("SetWindowPos")
	procGetForegroundWindow = modUser32.NewProc("GetForegroundWindow")
	procSendMessageW        = modUser32.NewProc("SendMessageW")
	procSetWindowsHookExW   = modUser32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = modUser32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = modUser32.NewProc("CallNextHookEx")
)

// DWM window attributes.
const (
	dwmwaUseImmersiveDarkModeOld = 19 // Windows 10 before 20H1
	dwmwaUseImmersiveDarkMode    = 20
	dwmwaCaptionColor            = 35 // Windows 11
)

// dwmSet calls DwmSetWindowAttribute with a 4-byte value.
func dwmSet(hwnd uintptr, attr uint32, v uint32) error {
	if err := procDwmSetWindowAttribute.Find(); err != nil {
		return err
	}
	hr, _, _ := procDwmSetWindowAttribute.Call(hwnd, uintptr(attr), uintptr(unsafe.Pointer(&v)), unsafe.Sizeof(v))
	if hr != 0 {
		return fmt.Errorf("DwmSetWindowAttribute(%d): HRESULT %#x", attr, uint32(hr))
	}
	return nil
}

// SetDarkTitleBar switches the title bar of a top-level window between the
// dark and the light system look (attribute 20, else 19 on older Windows 10).
func SetDarkTitleBar(hwnd uintptr, dark bool) error {
	if hwnd == 0 {
		return errors.New("no window")
	}
	var v uint32
	if dark {
		v = 1
	}
	if err := dwmSet(hwnd, dwmwaUseImmersiveDarkMode, v); err != nil {
		if err2 := dwmSet(hwnd, dwmwaUseImmersiveDarkModeOld, v); err2 != nil {
			return fmt.Errorf("%v; %v", err, err2)
		}
	}
	return nil
}

// SetCaptionColor sets the title bar colour (COLORREF 0x00BBGGRR, see
// ColorRef; CaptionColorDefault resets it). Windows 11 only: older systems
// return an error, which callers may ignore.
func SetCaptionColor(hwnd uintptr, colorRef uint32) error {
	if hwnd == 0 {
		return errors.New("no window")
	}
	return dwmSet(hwnd, dwmwaCaptionColor, colorRef)
}

// RedrawFrame makes a visible window repaint its frame, so a changed title
// bar look shows without waiting for the next activation. Windows 10 does
// not repaint the caption of the active window on SWP_FRAMECHANGED alone:
// there the caption is redrawn inactive and active again (WM_NCACTIVATE
// only paints the non-client area; it does not change the activation).
func RedrawFrame(hwnd uintptr) {
	if !IsWindow(hwnd) {
		return
	}
	const flags = 0x0001 | 0x0002 | 0x0004 | 0x0010 | 0x0020 // NOSIZE|NOMOVE|NOZORDER|NOACTIVATE|FRAMECHANGED
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, flags)
	if v := windows.RtlGetVersion(); v != nil && v.MajorVersion == 10 && v.BuildNumber < 22000 { // Windows 10
		if fg, _, _ := procGetForegroundWindow.Call(); fg == hwnd {
			const wmNCActivate = 0x0086
			procSendMessageW.Call(hwnd, wmNCActivate, 0, 0)
			procSendMessageW.Call(hwnd, wmNCActivate, 1, 0)
		}
	}
}

// AppsUseDarkTheme reports whether the Windows app theme is dark
// (HKCU\…\Themes\Personalize\AppsUseLightTheme = 0). Missing or unreadable
// values mean light, the Windows default.
func AppsUseDarkTheme() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// createHook is the state of OnWindowCreate's CBT hook. A thread hook only
// runs on the thread that installed it, so it needs no locking.
var createHook struct {
	handle  uintptr // installed hook; 0 once removed
	class   string
	fn      func(hwnd uintptr)
	matched bool
}

// unhookCreate removes the CBT hook if it is still installed.
func unhookCreate() {
	if h := createHook.handle; h != 0 {
		createHook.handle = 0
		procUnhookWindowsHookEx.Call(h)
	}
}

var cbtProcPtr = windows.NewCallback(cbtProc)

// Win32 structures of the HCBT_CREATEWND notification.
type createStruct struct {
	lpCreateParams uintptr
	hInstance      uintptr
	hMenu          uintptr
	hwndParent     uintptr
	cy, cx, y, x   int32
	style          int32
	lpszName       *uint16
	lpszClass      *uint16
	dwExStyle      uint32
}

type cbtCreateWnd struct {
	lpcs            *createStruct
	hwndInsertAfter uintptr
}

func cbtProc(code, wParam, lParam uintptr) uintptr {
	const hcbtCreateWnd = 3
	r, _, _ := procCallNextHookEx.Call(0, code, wParam, lParam)
	if int32(code) == hcbtCreateWnd && createHook.fn != nil && !createHook.matched && lParam != 0 {
		// lParam points to system memory (not the Go heap); the indirection
		// keeps vet's uintptr→pointer check quiet.
		if cs := (*(**cbtCreateWnd)(unsafe.Pointer(&lParam))).lpcs; cs != nil && cs.hwndParent == 0 &&
			uintptr(unsafe.Pointer(cs.lpszClass)) > 0xFFFF && // not an atom
			strings.EqualFold(windows.UTF16PtrToString(cs.lpszClass), createHook.class) {
			createHook.matched = true
			createHook.fn(wParam)
			unhookCreate() // only the first window is wanted
		}
	}
	return r
}

// OnWindowCreate runs create on the calling thread and calls fn for the
// first top-level window of class className created meanwhile, before the
// window is shown (a thread-local WH_CBT hook, removed after that window or
// when create returns). Used to style a window that a library creates and
// shows in one call. It reports whether fn ran; if the hook cannot be
// installed, create still runs and err says why.
func OnWindowCreate(className string, fn func(hwnd uintptr), create func()) (matched bool, err error) {
	const whCBT = 5
	h, _, e := procSetWindowsHookExW.Call(whCBT, cbtProcPtr, 0, uintptr(windows.GetCurrentThreadId()))
	if h == 0 {
		create()
		return false, fmt.Errorf("SetWindowsHookEx: %w", e)
	}
	createHook.handle, createHook.class, createHook.fn, createHook.matched = h, className, fn, false
	defer func() {
		unhookCreate() // no-op when the hook already removed itself
		createHook.class, createHook.fn = "", nil
	}()
	create()
	return createHook.matched, nil
}

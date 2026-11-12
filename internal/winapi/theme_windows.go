package winapi

import (
	"errors"
	"fmt"
	"strings"
	"sync"
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
	procEnumThreadWindows   = modUser32.NewProc("EnumThreadWindows")
	procGetClassNameW       = modUser32.NewProc("GetClassNameW")
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

// threadWindowState collects the result of one ThreadWindow call; the
// EnumThreadWindows callback runs synchronously on the caller's thread.
var (
	threadWindowMu    sync.Mutex
	threadWindowClass string
	threadWindowFound uintptr
)

var enumThreadWindowsProc = windows.NewCallback(func(hwnd, _ uintptr) uintptr {
	var buf [256]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n > 0 && strings.EqualFold(windows.UTF16ToString(buf[:n]), threadWindowClass) {
		threadWindowFound = hwnd
		return 0 // stop
	}
	return 1
})

// ThreadWindow returns the first top-level window of class className owned
// by thread threadID, or 0. Used to style a window that a library creates
// and shows in one call, as soon as the thread pumps messages again.
func ThreadWindow(threadID uint32, className string) uintptr {
	threadWindowMu.Lock()
	defer threadWindowMu.Unlock()
	threadWindowClass, threadWindowFound = className, 0
	procEnumThreadWindows.Call(uintptr(threadID), enumThreadWindowsProc, 0)
	return threadWindowFound
}

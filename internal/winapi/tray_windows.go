package winapi

import (
	"fmt"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10
	nifShowTip = 0x80

	niifUser      = 0x04
	niifLargeIcon = 0x20

	notifyIconVersion4 = 4

	ninSelect           = 0x0400
	ninKeySelect        = 0x0401
	ninBalloonUserClick = 0x0405

	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	mfString    = 0x0000
	mfSeparator = 0x0800

	imageIcon     = 1
	lrDefaultSize = 0x0040
	lrShared      = 0x8000

	wsPopup        = 0x80000000
	wsExToolWin    = 0x00000080
	idiApplication = 32512

	// AppIconID is the resource id of the application icon group (#1).
	AppIconID = 1

	trayCallbackMsg = WMApp + 1
	trayIconUID     = 1
)

// notifyIconData is NOTIFYICONDATAW (Vista+ layout, 976 bytes on amd64).
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type point struct{ X, Y int32 }

type msgT struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

// MenuItem is an entry of the tray context menu.
type MenuItem struct {
	Text      string
	Default   bool
	Separator bool
	Action    func()
}

// TrayHandlers are invoked on the main (UI) thread.
type TrayHandlers struct {
	OnOpen         func()            // left click / keyboard select
	Menu           func() []MenuItem // right click
	OnBalloonClick func()
	// OnMessage handles the app's own messages (WMApp+2 … 0xBFFF) posted to
	// the tray window; return true when handled.
	OnMessage func(msg uint32, wParam, lParam uintptr) bool
}

// Tray is the notification-area icon with its hidden owner window. All
// methods except Post must be called on the thread that created it.
type Tray struct {
	hwnd           uintptr
	h              TrayHandlers
	iconSmall      uintptr
	iconLarge      uintptr
	tip            string
	taskbarCreated uint32
}

var (
	current        *Tray // the one tray of the process (main thread only)
	trayWndProcPtr = windows.NewCallback(trayWndProc)
)

func hInstance() uintptr {
	var h windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &h)
	return uintptr(h)
}

// NewTray creates the hidden window of class className and adds the icon.
func NewTray(className, tooltip string, h TrayHandlers) (*Tray, error) {
	if current != nil {
		return nil, fmt.Errorf("tray already created")
	}
	hinst := hInstance()
	cls, err := windows.UTF16PtrFromString(className)
	if err != nil {
		return nil, err
	}
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   trayWndProcPtr,
		HInstance:     hinst,
		LpszClassName: cls,
	}
	if r, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return nil, fmt.Errorf("RegisterClassEx: %w", e)
	}
	t := &Tray{h: h, tip: tooltip}
	current = t
	// A hidden top-level (not message-only) window: it receives the
	// TaskbarCreated broadcast and can be found with FindWindow.
	hwnd, _, e := procCreateWindowExW.Call(wsExToolWin, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(cls)),
		wsPopup, 0, 0, 0, 0, 0, 0, hinst, 0)
	if hwnd == 0 {
		current = nil
		return nil, fmt.Errorf("CreateWindowEx: %w", e)
	}
	t.hwnd = hwnd
	tc, _ := windows.UTF16PtrFromString("TaskbarCreated")
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tc)))
	t.taskbarCreated = uint32(r)
	t.iconSmall = loadAppIcon(hinst, false)
	t.iconLarge = loadAppIcon(hinst, true)
	if err := t.add(); err != nil {
		return t, err // window exists; the icon may appear after explorer starts
	}
	return t, nil
}

// Hwnd returns the hidden window handle.
func (t *Tray) Hwnd() uintptr { return t.hwnd }

// Post posts msg to the tray window; safe from any goroutine.
func (t *Tray) Post(msg uint32, wParam, lParam uintptr) bool {
	r, _, _ := procPostMessageW.Call(t.hwnd, uintptr(msg), wParam, lParam)
	return r != 0
}

func (t *Tray) nid() notifyIconData {
	n := notifyIconData{HWnd: t.hwnd, UID: trayIconUID}
	n.CbSize = uint32(unsafe.Sizeof(n))
	return n
}

func (t *Tray) add() error {
	n := t.nid()
	n.UFlags = nifMessage | nifIcon | nifTip | nifShowTip
	n.UCallbackMessage = trayCallbackMsg
	n.HIcon = t.iconSmall
	copyUTF16(n.SzTip[:], t.tip)
	if r, _, e := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&n))); r == 0 {
		return fmt.Errorf("Shell_NotifyIcon(NIM_ADD): %w", e)
	}
	n.UVersion = notifyIconVersion4
	procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&n)))
	return nil
}

// SetTooltip changes the icon tooltip.
func (t *Tray) SetTooltip(tip string) {
	t.tip = tip
	n := t.nid()
	n.UFlags = nifTip | nifShowTip
	copyUTF16(n.SzTip[:], tip)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n)))
}

// ShowBalloon shows a notification (a toast on Windows 10/11).
func (t *Tray) ShowBalloon(title, text string) {
	if text == "" {
		text = " " // an empty text removes the balloon instead of showing it
	}
	n := t.nid()
	n.UFlags = nifInfo
	copyUTF16(n.SzInfoTitle[:], title)
	copyUTF16(n.SzInfo[:], text)
	n.DwInfoFlags = niifUser | niifLargeIcon
	n.HBalloonIcon = t.iconLarge
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n)))
}

// Remove deletes the icon and destroys the hidden window.
func (t *Tray) Remove() {
	n := t.nid()
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&n)))
	procDestroyWindow.Call(t.hwnd)
	for _, ic := range []uintptr{t.iconSmall, t.iconLarge} {
		if ic != 0 {
			procDestroyIcon.Call(ic)
		}
	}
}

func (t *Tray) showMenu() {
	if t.h.Menu == nil {
		return
	}
	items := t.h.Menu()
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	for i, it := range items {
		if it.Separator {
			procAppendMenuW.Call(menu, mfSeparator, 0, 0)
			continue
		}
		text, _ := windows.UTF16PtrFromString(it.Text)
		procAppendMenuW.Call(menu, mfString, uintptr(i+1), uintptr(unsafe.Pointer(text)))
		if it.Default {
			procSetMenuDefaultItem.Call(menu, uintptr(i+1), 0)
		}
	}
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Required so the menu closes when the user clicks elsewhere.
	procSetForegroundWindow.Call(t.hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmBottomAlign|tpmReturnCmd|tpmNoNotify,
		uintptr(pt.X), uintptr(pt.Y), 0, t.hwnd, 0)
	procPostMessageW.Call(t.hwnd, WMNull, 0, 0)
	if i := int(cmd) - 1; i >= 0 && i < len(items) && items[i].Action != nil {
		items[i].Action()
	}
}

func trayWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	t := current
	if t != nil && hwnd == t.hwnd {
		m := uint32(msg)
		switch {
		case m == trayCallbackMsg:
			switch uint32(lParam & 0xFFFF) {
			case ninSelect, ninKeySelect:
				if t.h.OnOpen != nil {
					t.h.OnOpen()
				}
			case WMContextMenu:
				t.showMenu()
			case ninBalloonUserClick:
				if t.h.OnBalloonClick != nil {
					t.h.OnBalloonClick()
				}
			}
			return 0
		case t.taskbarCreated != 0 && m == t.taskbarCreated:
			_ = t.add() // explorer restarted
			return 0
		case m > trayCallbackMsg && m <= 0xBFFF:
			if t.h.OnMessage != nil && t.h.OnMessage(m, wParam, lParam) {
				return 0
			}
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

func loadAppIcon(hinst uintptr, large bool) uintptr {
	var h uintptr
	if procLoadIconMetric.Find() == nil {
		lim := uintptr(0) // LIM_SMALL
		if large {
			lim = 1 // LIM_LARGE
		}
		if hr, _, _ := procLoadIconMetric.Call(hinst, AppIconID, lim, uintptr(unsafe.Pointer(&h))); hr == 0 && h != 0 {
			return h
		}
	}
	cx, _, _ := procGetSystemMetrics.Call(49) // SM_CXSMICON
	if large {
		cx, _, _ = procGetSystemMetrics.Call(11) // SM_CXICON
	}
	h, _, _ = procLoadImageW.Call(hinst, AppIconID, imageIcon, cx, cx, 0)
	if h == 0 {
		h, _, _ = procLoadImageW.Call(0, idiApplication, imageIcon, 0, 0, lrShared|lrDefaultSize)
	}
	return h
}

// copyUTF16 copies s into dst, truncating and NUL-terminating.
func copyUTF16(dst []uint16, s string) {
	u := utf16.Encode([]rune(s))
	if len(u) > len(dst)-1 {
		u = u[:len(dst)-1]
		if n := len(u); n > 0 && u[n-1] >= 0xD800 && u[n-1] < 0xDC00 {
			u = u[:n-1] // do not split a surrogate pair
		}
	}
	copy(dst, u)
	dst[len(u)] = 0
}

// Pump runs the message loop of the current thread until stop() reports
// true (checked after every dispatched message) or WM_QUIT arrives.
func Pump(stop func() bool) {
	var m msgT
	for !stop() {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return // WM_QUIT or error
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

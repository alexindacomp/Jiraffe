package winapi

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	procSetWindowLongPtrW            = modUser32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW              = modUser32.NewProc("CallWindowProcW")
	procIsZoomed                     = modUser32.NewProc("IsZoomed")
	procGetDpiForWindow              = modUser32.NewProc("GetDpiForWindow")
	procGetSystemMetricsForDpi       = modUser32.NewProc("GetSystemMetricsForDpi")
	procReleaseCapture               = modUser32.NewProc("ReleaseCapture")
	procGetAsyncKeyState             = modUser32.NewProc("GetAsyncKeyState")
	procDwmExtendFrameIntoClientArea = modDwmapi.NewProc("DwmExtendFrameIntoClientArea")
	procMonitorFromWindow            = modUser32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW              = modUser32.NewProc("GetMonitorInfoW")
	procSHAppBarMessage              = modShell32.NewProc("SHAppBarMessage")
	procGetClientRect                = modUser32.NewProc("GetClientRect")
	procInvalidateRect               = modUser32.NewProc("InvalidateRect")
	procBeginPaint                   = modUser32.NewProc("BeginPaint")
	procEndPaint                     = modUser32.NewProc("EndPaint")
	procFillRect                     = modUser32.NewProc("FillRect")
	procSetWindowCompositionAttr     = modUser32.NewProc("SetWindowCompositionAttribute") // undocumented
	procGetStockObject               = modGdi32.NewProc("GetStockObject")
	procSetTimer                     = modUser32.NewProc("SetTimer")
	procKillTimer                    = modUser32.NewProc("KillTimer")

	modGdi32 = windows.NewLazySystemDLL("gdi32.dll")
)

// Window messages and constants of the chrome.
const (
	WMSize          = 0x0005
	WMActivate      = 0x0006
	WMSettingChange = 0x001A
	WMSizing        = 0x0214 // the user is resizing (inside the size/move loop)
	WMExitSizeMove  = 0x0232 // the size/move loop ended
	wmPaint         = 0x000F
	wmEraseBkgnd    = 0x0014
	wmNCDestroy     = 0x0082
	wmNCCalcSize    = 0x0083
	wmNCLButtonDown = 0x00A1
	wmTimer         = 0x0113

	// While the user resizes the window, the WebView keeps its size and is
	// resized once when the resize ends: with a real mouse there are hundreds
	// of WM_SIZE per second and WebView2 cannot follow them (even throttled
	// to 50/s the page trailed far behind the frame). sizeTimerID is kept for
	// a throttled mode (sizeThrottleMs > 0).
	sizeTimerID    = 0x4A53 // "JS"
	sizeThrottleMs = 0

	SizeRestored  = 0
	SizeMaximized = 2

	gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC = -4

	dwmwaWindowCornerPreference = 33
	dwmwcpRound                 = 2
	dwmwaSystemBackdropType     = 38
	dwmsbtNone                  = 1
	dwmsbtTransientWindow       = 3 // acrylic

	blackBrush = 4 // GetStockObject

	wcaAccentPolicy               = 19
	accentEnableTransparentGrad   = 2 // a tinted surface without blur (cheap)
	accentEnableBlurBehind        = 3
	accentEnableAcrylicBlurBehind = 4

	smCXFrame        = 32
	smCYFrame        = 33
	smCXPaddedBorder = 92
	smSwapButton     = 23
	vkLButton        = 0x01
	vkRButton        = 0x02

	swMinimize = 6
	swMaximize = 3
)

// MessageHook observes messages of a frameless window before its original
// procedure handles them (UI thread).
type MessageHook func(msg uint32, wParam, lParam uintptr)

// subclass is the state of the one frameless window (UI thread only; the
// mutex only guards against misuse from other goroutines).
var subclass struct {
	sync.Mutex
	hwnd  uintptr
	orig  uintptr
	hook  MessageHook
	clear bool // glass: paint the window transparent black

	sizing  bool // inside an interactive resize (WM_SIZING .. WM_EXITSIZEMOVE)
	pending bool // a WM_SIZE was held back during the resize
	timer   bool // the size timer is running
}

// framelessProcPtr is created once: windows.NewCallback has a process-wide
// limit.
var framelessProcPtr = windows.NewCallback(framelessProc)

func framelessProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	subclass.Lock()
	orig, hook, clear := subclass.orig, subclass.hook, subclass.clear
	if hwnd != subclass.hwnd {
		orig, hook, clear = 0, nil, false
	}
	subclass.Unlock()
	if orig == 0 { // not ours (cannot happen): default handling
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}
	if msg == wmNCCalcSize && wParam != 0 {
		// No non-client area: the client (the page) covers the window.
		// Maximized, the window extends by the frame beyond the monitor.
		if IsMaximized(hwnd) {
			fx, fy := frameThickness(hwnd)
			r := *(**Rect)(unsafe.Pointer(&lParam)) // NCCALCSIZE_PARAMS.rgrc[0] comes first
			*r = LeaveAutoHideGap(InsetMaximized(*r, fx, fy), autoHideEdges(hwnd))
		}
		return 0
	}
	if clear {
		// The window's own surface lies under the transparent page. Nothing
		// else paints it (the class has no background brush), and unpainted
		// it shows as a pale layer over the backdrop; black is transparent
		// for DWM.
		switch msg {
		case wmEraseBkgnd:
			var rc Rect
			if r, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc))); r != 0 && wParam != 0 {
				fillBlack(wParam, &rc)
			}
			return 1
		case wmPaint:
			var ps paintStruct
			if hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps))); hdc != 0 {
				fillBlack(hdc, &ps.rcPaint)
				procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
			}
			return 0
		}
	}
	if hook != nil {
		hook(uint32(msg), wParam, lParam)
	}
	switch msg {
	case WMSizing:
		subclass.Lock()
		subclass.sizing = true
		subclass.Unlock()
	case WMSize:
		// Hold the WebView resize back while the user drags an edge; the
		// timer below forwards the latest size every sizeThrottleMs.
		if wParam == SizeRestored && throttleSize(hwnd) {
			return 0
		}
	case wmTimer:
		if wParam == sizeTimerID {
			flushSize(hwnd, orig, false)
			return 0
		}
	case WMExitSizeMove:
		subclass.Lock()
		subclass.sizing = false
		subclass.Unlock()
		flushSize(hwnd, orig, true)
	}
	r, _, _ := procCallWindowProcW.Call(orig, hwnd, msg, wParam, lParam)
	if msg == wmNCDestroy {
		subclass.Lock()
		if subclass.hwnd == hwnd {
			subclass.hwnd, subclass.orig, subclass.hook, subclass.clear = 0, 0, nil, false
			subclass.sizing, subclass.pending, subclass.timer = false, false, false
		}
		subclass.Unlock()
	}
	return r
}

// throttleSize reports whether a WM_SIZE is held back (an interactive
// resize is in progress) and starts the size timer if needed.
func throttleSize(hwnd uintptr) bool {
	subclass.Lock()
	defer subclass.Unlock()
	if !subclass.sizing || subclass.hwnd != hwnd {
		return false
	}
	subclass.pending = true
	if sizeThrottleMs == 0 {
		return true // frozen until WM_EXITSIZEMOVE
	}
	if !subclass.timer {
		if r, _, _ := procSetTimer.Call(hwnd, sizeTimerID, sizeThrottleMs, 0); r != 0 {
			subclass.timer = true
		} else {
			return false // no timer: resize directly
		}
	}
	return true
}

// flushSize forwards a held-back WM_SIZE with the current client size to the
// original window procedure (which resizes the WebView). final stops the
// timer (the resize ended); otherwise the timer stops once nothing is pending.
func flushSize(hwnd, orig uintptr, final bool) {
	subclass.Lock()
	pending := subclass.pending
	subclass.pending = false
	stop := subclass.timer && (final || !pending)
	if stop {
		subclass.timer = false
	}
	subclass.Unlock()
	if stop {
		procKillTimer.Call(hwnd, sizeTimerID)
	}
	if !pending {
		return
	}
	var rc Rect
	if r, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc))); r == 0 {
		return
	}
	w, h := uint32(rc.Right-rc.Left)&0xFFFF, uint32(rc.Bottom-rc.Top)&0xFFFF
	procCallWindowProcW.Call(orig, hwnd, WMSize, SizeRestored, uintptr(h<<16|w))
}

// paintStruct is PAINTSTRUCT.
type paintStruct struct {
	hdc         uintptr
	fErase      int32
	rcPaint     Rect
	fRestore    int32
	fIncUpdate  int32
	rgbReserved [32]byte
}

func fillBlack(hdc uintptr, rc *Rect) {
	brush, _, _ := procGetStockObject.Call(blackBrush)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(rc)), brush)
}

// paintClear makes the subclassed hwnd paint itself transparent black from
// now on and repaints it.
func paintClear(hwnd uintptr) {
	subclass.Lock()
	ok := subclass.hwnd == hwnd
	if ok {
		subclass.clear = true
	}
	subclass.Unlock()
	if ok {
		procInvalidateRect.Call(hwnd, 0, 1)
	}
}

// frameThickness is the sizing frame plus padded border at the window's DPI.
func frameThickness(hwnd uintptr) (int32, int32) {
	dpi := uintptr(96)
	if procGetDpiForWindow.Find() == nil {
		if d, _, _ := procGetDpiForWindow.Call(hwnd); d > 0 {
			dpi = d
		}
	}
	metric := func(m uintptr) int32 {
		if procGetSystemMetricsForDpi.Find() == nil {
			v, _, _ := procGetSystemMetricsForDpi.Call(m, dpi)
			return int32(v)
		}
		v, _, _ := procGetSystemMetrics.Call(m)
		return int32(v)
	}
	pad := metric(smCXPaddedBorder)
	return metric(smCXFrame) + pad, metric(smCYFrame) + pad
}

// appBarData is APPBARDATA (64-bit layout).
type appBarData struct {
	cbSize           uint32
	hWnd             uintptr
	uCallbackMessage uint32
	uEdge            uint32
	rc               Rect
	lParam           uintptr
}

// monitorInfo is MONITORINFO.
type monitorInfo struct {
	cbSize    uint32
	rcMonitor Rect
	rcWork    Rect
	dwFlags   uint32
}

// autoHideEdges finds the auto-hide app bars on the edges of the window's
// monitor (ABM_GETAUTOHIDEBAREX per edge).
func autoHideEdges(hwnd uintptr) AutoHideEdges {
	var e AutoHideEdges
	if procSHAppBarMessage.Find() != nil || procMonitorFromWindow.Find() != nil {
		return e
	}
	const monitorDefaultToNearest = 2
	mon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); mon == 0 || r == 0 {
		return e
	}
	const abmGetAutoHideBarEx = 0x0000000b
	for _, edge := range []struct {
		abe uint32
		dst *bool
	}{{0, &e.Left}, {1, &e.Top}, {2, &e.Right}, {3, &e.Bottom}} {
		abd := appBarData{cbSize: uint32(unsafe.Sizeof(appBarData{})), uEdge: edge.abe, rc: mi.rcMonitor}
		if bar, _, _ := procSHAppBarMessage.Call(abmGetAutoHideBarEx, uintptr(unsafe.Pointer(&abd))); bar != 0 {
			*edge.dst = true
		}
	}
	return e
}

// MakeFrameless subclasses hwnd (a WS_OVERLAPPEDWINDOW, which keeps snap,
// the min/max animations and Alt+Space) so it has no non-client area, and
// makes Windows recompute its frame. hook sees every other message first.
// Only one window is subclassed at a time; calling it again for the same
// window only replaces the hook.
func MakeFrameless(hwnd uintptr, hook MessageHook) error {
	if !IsWindow(hwnd) {
		return fmt.Errorf("no window")
	}
	subclass.Lock()
	if subclass.hwnd == hwnd {
		subclass.hook = hook
		subclass.Unlock()
		return nil
	}
	subclass.Unlock()
	orig, _, err := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, framelessProcPtr)
	if orig == 0 {
		return fmt.Errorf("SetWindowLongPtr: %w", err)
	}
	subclass.Lock()
	subclass.hwnd, subclass.orig, subclass.hook = hwnd, orig, hook
	subclass.Unlock()
	const flags = 0x0001 | 0x0002 | 0x0004 | 0x0010 | 0x0020 // NOSIZE|NOMOVE|NOZORDER|NOACTIVATE|FRAMECHANGED
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, flags)
	return nil
}

// SetRoundCorners asks DWM for rounded corners (Windows 11; an error on 10).
func SetRoundCorners(hwnd uintptr) error {
	return dwmSet(hwnd, dwmwaWindowCornerPreference, dwmwcpRound)
}

// EnableAcrylic sets the acrylic system backdrop and extends the frame
// over the whole client area (the "sheet of glass"). It fails on systems
// without DWMWA_SYSTEMBACKDROP_TYPE.
func EnableAcrylic(hwnd uintptr) error {
	if err := dwmSet(hwnd, dwmwaSystemBackdropType, dwmsbtTransientWindow); err != nil {
		return err
	}
	if err := procDwmExtendFrameIntoClientArea.Find(); err != nil {
		return err
	}
	margins := [4]int32{-1, -1, -1, -1}
	if hr, _, _ := procDwmExtendFrameIntoClientArea.Call(hwnd, uintptr(unsafe.Pointer(&margins))); hr != 0 {
		return fmt.Errorf("DwmExtendFrameIntoClientArea: HRESULT %#x", uint32(hr))
	}
	return nil
}

// EnableBackdrop puts the glass backdrop b behind the (transparent) page
// of the frameless window hwnd and returns the backdrop it applied. The
// accent backdrops use an undocumented API; when it fails the system
// acrylic is used instead and fallback tells why. tint (ABGR) colours the
// accent acrylic. err means no backdrop at all.
func EnableBackdrop(hwnd uintptr, b Backdrop, tint uint32) (applied Backdrop, fallback, err error) {
	if b == BackdropAccentAcrylic || b == BackdropBlur {
		if fallback = enableAccent(hwnd, b, tint); fallback == nil {
			paintClear(hwnd)
			return b, nil, nil
		}
		fallback = fmt.Errorf("%s: %w", b, fallback)
	}
	if err := EnableAcrylic(hwnd); err != nil {
		if fallback != nil {
			err = fmt.Errorf("%v; system acrylic: %w", fallback, err)
		}
		return b, nil, err
	}
	paintClear(hwnd)
	return BackdropAcrylic, fallback, nil
}

// accentPolicy is ACCENT_POLICY of SetWindowCompositionAttribute.
type accentPolicy struct {
	accentState   uint32
	accentFlags   uint32
	gradientColor uint32 // ABGR
	animationID   uint32
}

// windowCompositionAttribData is WINDOWCOMPOSITIONATTRIBDATA.
type windowCompositionAttribData struct {
	attrib uint32
	data   unsafe.Pointer
	size   uintptr // ULONG, padded on 64-bit
}

// enableAccent sets the accent policy blur (without a DWM system backdrop,
// which would paint over it) and the 1 px frame extension for the shadow.
func enableAccent(hwnd uintptr, b Backdrop, tint uint32) error {
	if err := procSetWindowCompositionAttr.Find(); err != nil {
		return err
	}
	// The shadow first: a failure leaves nothing half-applied for the
	// fallback.
	if err := ExtendFrameForShadow(hwnd); err != nil {
		return err
	}
	policy := accentPolicy{accentState: accentEnableBlurBehind}
	if b == BackdropAccentAcrylic {
		policy = accentPolicy{accentState: accentEnableAcrylicBlurBehind, gradientColor: tint}
	}
	data := windowCompositionAttribData{attrib: wcaAccentPolicy, data: unsafe.Pointer(&policy), size: unsafe.Sizeof(policy)}
	if r, _, err := procSetWindowCompositionAttr.Call(hwnd, uintptr(unsafe.Pointer(&data))); r == 0 {
		return fmt.Errorf("SetWindowCompositionAttribute: %w", err)
	}
	_ = dwmSet(hwnd, dwmwaSystemBackdropType, dwmsbtNone) // Windows 11 22H2+ only; the default anyway
	return nil
}

// PauseBackdrop swaps the accent blur of hwnd for a plain tinted surface
// (ABGR tint, no blur). DWM recomputes the accent blur for every size step,
// and with a real mouse (hundreds of steps per second) the window frame then
// falls far behind the cursor; the tinted surface is cheap. ResumeBackdrop
// puts the blur back when the resize ends.
func PauseBackdrop(hwnd uintptr, tint uint32) error {
	return setAccent(hwnd, accentPolicy{accentState: accentEnableTransparentGrad, accentFlags: 2, gradientColor: tint})
}

// ResumeBackdrop restores the accent backdrop b after PauseBackdrop.
func ResumeBackdrop(hwnd uintptr, b Backdrop, tint uint32) error {
	policy := accentPolicy{accentState: accentEnableBlurBehind}
	if b == BackdropAccentAcrylic {
		policy = accentPolicy{accentState: accentEnableAcrylicBlurBehind, gradientColor: tint}
	}
	return setAccent(hwnd, policy)
}

func setAccent(hwnd uintptr, policy accentPolicy) error {
	if err := procSetWindowCompositionAttr.Find(); err != nil {
		return err
	}
	data := windowCompositionAttribData{attrib: wcaAccentPolicy, data: unsafe.Pointer(&policy), size: unsafe.Sizeof(policy)}
	if r, _, err := procSetWindowCompositionAttr.Call(hwnd, uintptr(unsafe.Pointer(&data))); r == 0 {
		return fmt.Errorf("SetWindowCompositionAttribute: %w", err)
	}
	return nil
}

// ExtendFrameForShadow extends the DWM frame 1 px into the top of the client
// area. A frameless window without the acrylic backdrop otherwise has no
// border or shadow; with this DWM draws its standard shadow (and the
// Windows 11 border). The 1 px row is covered by the page.
func ExtendFrameForShadow(hwnd uintptr) error {
	if err := procDwmExtendFrameIntoClientArea.Find(); err != nil {
		return err
	}
	margins := [4]int32{0, 0, 1, 0} // MARGINS: left, right, top, bottom
	if hr, _, _ := procDwmExtendFrameIntoClientArea.Call(hwnd, uintptr(unsafe.Pointer(&margins))); hr != 0 {
		return fmt.Errorf("DwmExtendFrameIntoClientArea: HRESULT %#x", uint32(hr))
	}
	return nil
}

// TransparencyEnabled reads the user's "Transparency effects" setting
// (HKCU\…\Themes\Personalize\EnableTransparency; absent = on, the default).
func TransparencyEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("EnableTransparency")
	return err != nil || v != 0
}

// BuildNumber is the Windows build (22631 on 11 23H2).
func BuildNumber() uint32 {
	if v := windows.RtlGetVersion(); v != nil {
		return v.BuildNumber
	}
	return 0
}

// IsMaximized reports whether hwnd is maximized.
func IsMaximized(hwnd uintptr) bool {
	r, _, _ := procIsZoomed.Call(hwnd)
	return r != 0
}

// Minimize minimizes hwnd.
func Minimize(hwnd uintptr) { procShowWindow.Call(hwnd, swMinimize) }

// ToggleMaximize maximizes or restores hwnd.
func ToggleMaximize(hwnd uintptr) {
	if IsMaximized(hwnd) {
		procShowWindow.Call(hwnd, SWRestore)
	} else {
		procShowWindow.Call(hwnd, swMaximize)
	}
}

// PostClose asks hwnd to close (the normal close path).
func PostClose(hwnd uintptr) { procPostMessageW.Call(hwnd, WMClose, 0, 0) }

// primaryButtonDown reports whether the primary mouse button is down now.
func primaryButtonDown() bool {
	vk := uintptr(vkLButton)
	if swapped, _, _ := procGetSystemMetrics.Call(smSwapButton); swapped != 0 {
		vk = vkRButton // GetAsyncKeyState reads the physical buttons
	}
	r, _, _ := procGetAsyncKeyState.Call(vk)
	return r&0x8000 != 0
}

// StartNonClientDrag lets Windows run its move (HTCAPTION) or resize
// (HTLEFT…) loop as if the frame had been pressed — only while the primary
// button is down. It reports whether it started.
func StartNonClientDrag(hwnd, hitTest uintptr) bool {
	if !IsWindow(hwnd) || !primaryButtonDown() {
		return false
	}
	procReleaseCapture.Call()
	r, _, _ := procPostMessageW.Call(hwnd, wmNCLButtonDown, hitTest, 0)
	return r != 0
}

// SettingChangeArea reads the area string of WM_SETTINGCHANGE (lParam),
// e.g. "ImmersiveColorSet"; "" when absent.
func SettingChangeArea(lParam uintptr) string {
	if lParam == 0 {
		return ""
	}
	return windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&lParam)))
}

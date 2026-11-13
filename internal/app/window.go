package app

// RGB is a colour of the native window frame.
type RGB struct{ R, G, B uint8 }

// WindowTheme is a setWindowTheme request for the native window frame.
type WindowTheme struct {
	Dark bool
	// Caption is the title bar colour; nil leaves it to the system.
	Caption *RGB
}

// setWindowTheme validates { dark: bool, caption?: "#rrggbb" } and hands it
// to the platform (which ignores it when there is no window).
func (a *App) setWindowTheme(dark *bool, caption *string) error {
	if dark == nil {
		return a.badParams("dark is required")
	}
	t := WindowTheme{Dark: *dark}
	if caption != nil {
		c, ok := parseHexColor(*caption)
		if !ok {
			return a.badParams(`caption must be a "#rrggbb" colour`)
		}
		t.Caption = &c
	}
	a.plat.SetWindowTheme(t)
	return nil
}

// badParams is a bad_params error with an English detail, like decode errors.
func (a *App) badParams(detail string) *Error {
	return &Error{Code: CodeBadParams, Message: T(a.Language(), "err_bad_params") + ": " + detail}
}

// parseHexColor parses "#rrggbb" (either case).
func parseHexColor(s string) (RGB, bool) {
	if len(s) != 7 || s[0] != '#' {
		return RGB{}, false
	}
	var v [3]uint8
	for i := range v {
		hi, ok1 := hexDigit(s[1+2*i])
		lo, ok2 := hexDigit(s[2+2*i])
		if !ok1 || !ok2 {
			return RGB{}, false
		}
		v[i] = hi<<4 | lo
	}
	return RGB{R: v[0], G: v[1], B: v[2]}, true
}

func hexDigit(c byte) (uint8, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// WindowInfo is the getWindowInfo result and the "windowState" event: the
// page draws glass surfaces only when glass, and its own window controls.
type WindowInfo struct {
	Glass     bool `json:"glass"`
	Maximized bool `json:"maximized"`
	Active    bool `json:"active"`
	// Resizing is true while the user resizes the window with the mouse:
	// the page drops costly effects so it can keep up with the frame.
	Resizing bool `json:"resizing"`
}

// WindowChrome is implemented by a platform with a frameless window
// (Windows). Without it the methods are no-ops ({glass:false,…}).
type WindowChrome interface {
	// WindowInfo is the current state (safe from any goroutine).
	WindowInfo() WindowInfo
	// WindowControl performs a validated action on the UI thread: minimize,
	// toggleMaximize, close, drag, resize (with edge). It never fails.
	WindowControl(action, edge string)
}

// windowEdges are the resize edges of windowControl.
var windowEdges = map[string]bool{"n": true, "s": true, "e": true, "w": true, "ne": true, "nw": true, "se": true, "sw": true}

func (a *App) getWindowInfo() WindowInfo {
	if wc, ok := a.plat.(WindowChrome); ok {
		return wc.WindowInfo()
	}
	return WindowInfo{}
}

// windowControl validates {action, edge?} and hands it to the platform.
func (a *App) windowControl(action, edge string) error {
	switch action {
	case "minimize", "toggleMaximize", "close", "drag":
		if edge != "" {
			return a.badParams("edge is only for resize")
		}
	case "resize":
		if !windowEdges[edge] {
			return a.badParams("edge must be n, s, e, w, ne, nw, se or sw")
		}
	default:
		return a.badParams("action must be minimize, toggleMaximize, close, drag or resize")
	}
	if wc, ok := a.plat.(WindowChrome); ok {
		wc.WindowControl(action, edge)
	}
	return nil
}

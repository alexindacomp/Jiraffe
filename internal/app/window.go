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

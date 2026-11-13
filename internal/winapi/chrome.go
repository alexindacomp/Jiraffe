package winapi

// Pure parts of the frameless window chrome (testable on any OS).

import (
	"fmt"
	"strconv"
	"strings"
)

// Hit-test codes of WM_NCLBUTTONDOWN.
const (
	HTCaption     = 2
	HTLeft        = 10
	HTRight       = 11
	HTTop         = 12
	HTTopLeft     = 13
	HTTopRight    = 14
	HTBottom      = 15
	HTBottomLeft  = 16
	HTBottomRight = 17
)

// EdgeHitTest maps a resize edge of windowControl to its hit-test code.
func EdgeHitTest(edge string) (uintptr, bool) {
	ht, ok := map[string]uintptr{
		"n": HTTop, "s": HTBottom, "e": HTRight, "w": HTLeft,
		"ne": HTTopRight, "nw": HTTopLeft, "se": HTBottomRight, "sw": HTBottomLeft,
	}[edge]
	return ht, ok
}

// Rect is a Win32 RECT.
type Rect struct{ Left, Top, Right, Bottom int32 }

// InsetMaximized is the client rect of a maximized frameless window: the
// window rect a maximized window gets extends by the frame thickness beyond
// the monitor, so the content is inset by it.
func InsetMaximized(r Rect, frameX, frameY int32) Rect {
	return Rect{Left: r.Left + frameX, Top: r.Top + frameY, Right: r.Right - frameX, Bottom: r.Bottom - frameY}
}

// MinAcrylicBuild is the first Windows 11 build with
// DWMWA_SYSTEMBACKDROP_TYPE (22H2).
const MinAcrylicBuild = 22621

// GlassCandidate reports whether the acrylic backdrop is worth trying:
// Windows 11 22H2+ (by build number: the registry product name says
// "Windows 10" on Windows 11) with the user's transparency effects on.
func GlassCandidate(build uint32, transparency bool) bool {
	return build >= MinAcrylicBuild && transparency
}

// Backdrop is the glass material behind the transparent page.
type Backdrop int

const (
	// BackdropAcrylic is the DWM system backdrop (DWMSBT_TRANSIENTWINDOW):
	// documented, but nearly opaque in the dark theme.
	BackdropAcrylic Backdrop = iota
	// BackdropAccentAcrylic is the accent policy acrylic
	// (ACCENT_ENABLE_ACRYLICBLURBEHIND, undocumented) with a light tint.
	BackdropAccentAcrylic
	// BackdropBlur is the accent policy plain blur (ACCENT_ENABLE_BLURBEHIND,
	// undocumented); the page supplies the tint.
	BackdropBlur
)

func (b Backdrop) String() string {
	switch b {
	case BackdropAcrylic:
		return "system acrylic"
	case BackdropAccentAcrylic:
		return "accent acrylic"
	case BackdropBlur:
		return "accent blur"
	}
	return fmt.Sprintf("backdrop(%d)", int(b))
}

// DefaultBackdrop is the backdrop of glass windows.
const DefaultBackdrop = BackdropBlur

// DefaultAccentTint is the tint of the accent acrylic (ABGR): a neutral
// dark grey at 25 % opacity.
const DefaultAccentTint uint32 = 0x40141518

// ParseBackdrop reads the JIRAFFE_BACKDROP test switch: "" (the default),
// "A" (system acrylic), "B" (accent acrylic, optionally "B:<ABGR hex>" for
// its tint) or "C" (accent blur), case-insensitive. ok is false for
// anything else, which then means the default.
func ParseBackdrop(s string) (b Backdrop, tint uint32, ok bool) {
	tint = DefaultAccentTint
	mode, arg, hasArg := strings.Cut(strings.TrimSpace(s), ":")
	switch strings.ToUpper(mode) {
	case "":
		if hasArg {
			return DefaultBackdrop, tint, false
		}
		return DefaultBackdrop, tint, true
	case "A":
		b = BackdropAcrylic
	case "B":
		b = BackdropAccentAcrylic
	case "C":
		b = BackdropBlur
	default:
		return DefaultBackdrop, tint, false
	}
	if hasArg {
		if b != BackdropAccentAcrylic {
			return DefaultBackdrop, DefaultAccentTint, false
		}
		v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(arg), "0x"), 16, 32)
		if err != nil || arg == "" {
			return DefaultBackdrop, DefaultAccentTint, false
		}
		tint = uint32(v)
	}
	return b, tint, true
}

// AutoHideEdges tells on which edges of a monitor an auto-hide app bar
// (the taskbar) lives.
type AutoHideEdges struct{ Left, Top, Right, Bottom bool }

// LeaveAutoHideGap shrinks the client rect of a maximized window by 1 px on
// every monitor edge with an auto-hide app bar: a window covering the whole
// monitor would keep the hidden taskbar from sliding out.
func LeaveAutoHideGap(r Rect, e AutoHideEdges) Rect {
	if e.Left {
		r.Left++
	}
	if e.Top {
		r.Top++
	}
	if e.Right {
		r.Right--
	}
	if e.Bottom {
		r.Bottom--
	}
	return r
}

package winapi

import "testing"

func TestEdgeHitTest(t *testing.T) {
	want := map[string]uintptr{"n": 12, "s": 15, "e": 11, "w": 10, "ne": 14, "nw": 13, "se": 17, "sw": 16}
	for edge, ht := range want {
		if got, ok := EdgeHitTest(edge); !ok || got != ht {
			t.Errorf("%s: %d %v, want %d", edge, got, ok, ht)
		}
	}
	for _, bad := range []string{"", "N", "top", "nn", "en"} {
		if _, ok := EdgeHitTest(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestInsetMaximized(t *testing.T) {
	// A maximized 1920×1040 work area: Windows places the window 8 px
	// beyond it on every side (frame 4 + padded border 4 at 96 DPI).
	r := Rect{Left: -8, Top: -8, Right: 1928, Bottom: 1048}
	if got := InsetMaximized(r, 8, 8); got != (Rect{0, 0, 1920, 1040}) {
		t.Errorf("%+v", got)
	}
}

func TestGlassCandidate(t *testing.T) {
	for _, tc := range []struct {
		build uint32
		on    bool
		want  bool
	}{
		{22631, true, true},   // the user's 23H2
		{22621, true, true},   // 22H2
		{22000, true, false},  // 21H2: no system backdrop attribute
		{19045, true, false},  // Windows 10
		{22631, false, false}, // transparency effects off
	} {
		if got := GlassCandidate(tc.build, tc.on); got != tc.want {
			t.Errorf("%d/%v: %v", tc.build, tc.on, got)
		}
	}
}

func TestLeaveAutoHideGap(t *testing.T) {
	r := Rect{0, 0, 2560, 1600}
	for _, tc := range []struct {
		e    AutoHideEdges
		want Rect
	}{
		{AutoHideEdges{}, Rect{0, 0, 2560, 1600}},
		{AutoHideEdges{Bottom: true}, Rect{0, 0, 2560, 1599}}, // the usual taskbar
		{AutoHideEdges{Top: true}, Rect{0, 1, 2560, 1600}},
		{AutoHideEdges{Left: true, Right: true}, Rect{1, 0, 2559, 1600}},
		{AutoHideEdges{true, true, true, true}, Rect{1, 1, 2559, 1599}},
	} {
		if got := LeaveAutoHideGap(r, tc.e); got != tc.want {
			t.Errorf("%+v: %+v, want %+v", tc.e, got, tc.want)
		}
	}
}

func TestParseBackdrop(t *testing.T) {
	for _, tc := range []struct {
		in   string
		b    Backdrop
		tint uint32
		ok   bool
	}{
		{"", DefaultBackdrop, DefaultAccentTint, true},
		{"  ", DefaultBackdrop, DefaultAccentTint, true},
		{"A", BackdropAcrylic, DefaultAccentTint, true},
		{"b", BackdropAccentAcrylic, DefaultAccentTint, true},
		{"C", BackdropBlur, DefaultAccentTint, true},
		{"B:0x26141518", BackdropAccentAcrylic, 0x26141518, true},
		{"B:26141518", BackdropAccentAcrylic, 0x26141518, true},
		{"B:", DefaultBackdrop, DefaultAccentTint, false},
		{"B:xyz", DefaultBackdrop, DefaultAccentTint, false},
		{"B:0x1ffffffff", DefaultBackdrop, DefaultAccentTint, false},
		{"C:0x40141518", DefaultBackdrop, DefaultAccentTint, false},
		{":1", DefaultBackdrop, DefaultAccentTint, false},
		{"D", DefaultBackdrop, DefaultAccentTint, false},
		{"acrylic", DefaultBackdrop, DefaultAccentTint, false},
	} {
		b, tint, ok := ParseBackdrop(tc.in)
		if b != tc.b || tint != tc.tint || ok != tc.ok {
			t.Errorf("%q: %v %#x %v, want %v %#x %v", tc.in, b, tint, ok, tc.b, tc.tint, tc.ok)
		}
	}
}

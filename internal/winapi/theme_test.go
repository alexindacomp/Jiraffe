package winapi

import "testing"

func TestColorRef(t *testing.T) {
	for _, tc := range []struct {
		r, g, b uint8
		want    uint32
	}{
		{0, 0, 0, 0},
		{0x12, 0x34, 0x56, 0x00563412},
		{0xFF, 0, 0, 0x000000FF},
		{0, 0, 0xFF, 0x00FF0000},
		{0xFF, 0xFF, 0xFF, 0x00FFFFFF},
	} {
		if got := ColorRef(tc.r, tc.g, tc.b); got != tc.want {
			t.Errorf("ColorRef(%#x,%#x,%#x) = %#08x, want %#08x", tc.r, tc.g, tc.b, got, tc.want)
		}
	}
	if ColorRef(0xFF, 0xFF, 0xFF) == CaptionColorDefault {
		t.Error("white must differ from the default marker")
	}
}

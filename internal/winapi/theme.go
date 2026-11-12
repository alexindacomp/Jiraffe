package winapi

// CaptionColorDefault resets DWMWA_CAPTION_COLOR to the system colour.
const CaptionColorDefault uint32 = 0xFFFFFFFF

// ColorRef returns the Win32 COLORREF (0x00BBGGRR) of an RGB colour.
func ColorRef(r, g, b uint8) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

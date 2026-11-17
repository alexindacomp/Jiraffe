// Command icongen draws the Jiraffe app icon — the logo of the UI (the
// "i-logo" symbol of ui/index.html: a yellow rounded square with a dark "J"
// with two dots and three soft spots) — and writes a multi-size .ico file
// (16, 24, 32, 48, 256; PNG-compressed entries) plus a 256 px PNG (the Linux
// window, tray and menu icon).
//
// It is a build-time tool run by build.sh / build-linux.sh; it is not part of
// the binary.
//
//	go run ./tools/icongen -out assets
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var sizes = []int{16, 24, 32, 48, 256}

// Colours of the logo (ui/index.html, symbol i-logo).
var (
	yellow = rgb(0xFF, 0xF2, 0x00)
	spot   = rgb(0xD9, 0xCD, 0x00)
	ink    = rgb(0x2A, 0x2C, 0x30)
)

type fcolor struct{ r, g, b float64 }

func rgb(r, g, b uint8) fcolor { return fcolor{float64(r), float64(g), float64(b)} }

// The logo in its 32×32 viewBox.
const (
	view       = 32.0
	cornerR    = 10.0 // rect rx
	strokeHalf = 1.6  // J stroke-width 3.2
)

// spotsPath is the spots' path ("M x y" then relative cubic segments, "z").
const spotsPath = "M5 22.5c1.6-.6 3.4.1 3.6 1.7.2 1.5-1.4 2.6-3 2.3-1.7-.3-2.1-3.3-.6-4z" +
	"M24.3 21c1.8-.3 3.3 1 2.9 2.6-.4 1.5-2.4 2-3.7 1.1-1.2-.9-.9-3.4.8-3.7z" +
	"M6.3 6.2c1.5-.4 2.9.7 2.6 2-.3 1.4-2.2 1.9-3.3 1-1-.8-.7-2.7.7-3z"

// spots are the flattened spot outlines.
var spots = parseSpots(spotsPath)

// parseSpots reads the subset of SVG path syntax used by spotsPath and
// flattens each cubic into line segments.
func parseSpots(d string) [][][2]float64 {
	var polys [][][2]float64
	var cur [][2]float64
	var x, y float64
	for _, part := range strings.SplitAfter(d, "z") {
		part = strings.TrimSuffix(strings.TrimSpace(part), "z")
		if part == "" {
			continue
		}
		mi, ci := strings.Index(part, "M"), strings.Index(part, "c")
		m := numbers(part[mi+1 : ci])
		x, y = m[0], m[1]
		cur = [][2]float64{{x, y}}
		c := numbers(part[ci+1:])
		for i := 0; i+5 < len(c); i += 6 {
			x1, y1, x2, y2, x3, y3 := x+c[i], y+c[i+1], x+c[i+2], y+c[i+3], x+c[i+4], y+c[i+5]
			for k := 1; k <= 16; k++ {
				t := float64(k) / 16
				u := 1 - t
				cur = append(cur, [2]float64{
					u*u*u*x + 3*u*u*t*x1 + 3*u*t*t*x2 + t*t*t*x3,
					u*u*u*y + 3*u*u*t*y1 + 3*u*t*t*y2 + t*t*t*y3,
				})
			}
			x, y = x3, y3
		}
		polys = append(polys, cur)
	}
	return polys
}

// numbers splits SVG number lists ("1.6-.6 3.4.1": signs and second dots
// start new numbers).
func numbers(s string) []float64 {
	var out []float64
	var b strings.Builder
	dot := false
	flush := func() {
		if b.Len() > 0 {
			v, err := strconv.ParseFloat(b.String(), 64)
			if err != nil {
				panic(err)
			}
			out = append(out, v)
			b.Reset()
		}
		dot = false
	}
	for _, r := range s {
		switch {
		case r == ' ' || r == ',':
			flush()
		case r == '-':
			flush()
			b.WriteRune(r)
		case r == '.':
			if dot {
				flush()
			}
			dot = true
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

func inPolygon(p [2]float64, poly [][2]float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a[1] > p[1]) != (b[1] > p[1]) && p[0] < (b[0]-a[0])*(p[1]-a[1])/(b[1]-a[1])+a[0] {
			in = !in
		}
	}
	return in
}

// inJ reports whether (x, y) is on the "J": M19.5 11 v8.5, then the lower
// half circle of radius 4.5 around (15, 19.5) to (10.5, 19.5), round caps.
func inJ(x, y float64) bool {
	const (
		top, bottom = 11.0, 19.5
		stemX       = 19.5
		cx, cy, r   = 15.0, 19.5, 4.5
	)
	if y >= top && y <= bottom && math.Abs(x-stemX) <= strokeHalf {
		return true
	}
	if y >= cy && math.Abs(math.Hypot(x-cx, y-cy)-r) <= strokeHalf {
		return true
	}
	for _, e := range [][2]float64{{stemX, top}, {cx - r, cy}} { // round caps
		if math.Hypot(x-e[0], y-e[1]) <= strokeHalf {
			return true
		}
	}
	return false
}

// sample returns colour and coverage (0..1) at unit point (x, y).
func sample(x, y float64, size int) (fcolor, float64) {
	margin := 0.0 // small icons use every pixel
	if size >= 48 {
		margin = 0.04
	}
	// To viewBox units.
	vx := (x - margin) / (1 - 2*margin) * view
	vy := (y - margin) / (1 - 2*margin) * view
	if vx < 0 || vy < 0 || vx > view || vy > view {
		return fcolor{}, 0
	}
	dx := math.Max(math.Max(cornerR-vx, vx-(view-cornerR)), 0)
	dy := math.Max(math.Max(cornerR-vy, vy-(view-cornerR)), 0)
	if dx*dx+dy*dy > cornerR*cornerR {
		return fcolor{}, 0
	}
	if inJ(vx, vy) || math.Hypot(vx-17.2, vy-6.6) <= 1.7 || math.Hypot(vx-21.8, vy-6.6) <= 1.7 {
		return ink, 1
	}
	for _, poly := range spots {
		if inPolygon([2]float64{vx, vy}, poly) {
			return spot, 1
		}
	}
	return yellow, 1
}

func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4 // supersampling per axis
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var acc fcolor
			var cov float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					c, a := sample(x, y, size)
					acc.r += c.r * a
					acc.g += c.g * a
					acc.b += c.b * a
					cov += a
				}
			}
			if cov == 0 {
				continue
			}
			n := float64(ss * ss)
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(math.Round(acc.r / cov)),
				G: uint8(math.Round(acc.g / cov)),
				B: uint8(math.Round(acc.b / cov)),
				A: uint8(math.Round(255 * cov / n)),
			})
		}
	}
	return img
}

func encodePNG(img image.Image) ([]byte, error) {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// writeICO writes an ICO with PNG-compressed entries (supported since Vista).
func writeICO(path string, pngs [][]byte, sizes []int) error {
	var b bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&b, le, [3]uint16{0, 1, uint16(len(pngs))})
	offset := 6 + 16*len(pngs)
	for i, p := range pngs {
		dim := uint8(sizes[i])
		if sizes[i] >= 256 {
			dim = 0
		}
		_ = binary.Write(&b, le, struct {
			W, H, Colors, Reserved uint8
			Planes, BitCount       uint16
			Bytes, Offset          uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(p)), uint32(offset)})
		offset += len(p)
	}
	for _, p := range pngs {
		b.Write(p)
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func main() {
	out := flag.String("out", "assets", "output directory")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var pngs [][]byte
	for _, s := range sizes {
		p, err := encodePNG(render(s))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		pngs = append(pngs, p)
		if s == 256 {
			if err := os.WriteFile(filepath.Join(*out, "icon.png"), p, 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}
	if err := writeICO(filepath.Join(*out, "icon.ico"), pngs, sizes); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("icon written to", filepath.Join(*out, "icon.ico"))
}

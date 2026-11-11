// Command icongen draws the Jiraffe app icon (an amber rounded square with a
// giraffe-like pattern of brown patches) and writes a multi-size .ico file
// (16, 24, 32, 48, 256; PNG-compressed entries) plus a 256 px PNG preview.
//
// It is a build-time tool run by build.sh; it is not part of the binary.
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
)

var sizes = []int{16, 24, 32, 48, 256}

// Colors.
var (
	amberTop    = rgb(0xFF, 0xC5, 0x3D)
	amberBottom = rgb(0xF2, 0x9A, 0x1A)
	patch       = rgb(0x8A, 0x4B, 0x1C)
	patchDark   = rgb(0x6E, 0x37, 0x12)
)

type fcolor struct{ r, g, b float64 }

func rgb(r, g, b uint8) fcolor { return fcolor{float64(r), float64(g), float64(b)} }

func lerp(a, b fcolor, t float64) fcolor {
	return fcolor{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t}
}

// Voronoi seeds in unit coordinates; jittered grid gives giraffe-like patches.
var seeds = [][2]float64{
	{0.18, 0.20}, {0.50, 0.14}, {0.84, 0.22},
	{0.30, 0.48}, {0.66, 0.44}, {0.92, 0.58},
	{0.12, 0.76}, {0.46, 0.80}, {0.80, 0.86},
	{-0.10, 0.45}, {1.10, 0.10}, {0.55, 1.12}, {-0.05, 1.05}, {1.08, 1.05},
}

// sample returns color and coverage (0..1) at unit point (x, y).
func sample(x, y float64, size int) (fcolor, float64) {
	// Rounded square: corner radius 22% of size, small margin for 256.
	margin := 0.0
	if size >= 48 {
		margin = 0.03
	}
	r := 0.22
	cx, cy := math.Abs(x-0.5), math.Abs(y-0.5)
	half := 0.5 - margin
	dx, dy := math.Max(cx-(half-r), 0), math.Max(cy-(half-r), 0)
	if cx > half || cy > half || dx*dx+dy*dy > r*r {
		return fcolor{}, 0
	}
	base := lerp(amberTop, amberBottom, y)

	// Distance to nearest and second-nearest seed: near the bisector we draw
	// the light "network" between patches.
	d1, d2 := math.MaxFloat64, math.MaxFloat64
	for _, s := range seeds {
		d := math.Hypot(x-s[0], y-s[1])
		if d < d1 {
			d1, d2 = d, d1
		} else if d < d2 {
			d2 = d
		}
	}
	gap := 0.045
	if size <= 24 {
		gap = 0.07 // thicker lines so the pattern survives at small sizes
	}
	// Keep a clean amber border around the patches.
	border := 0.09
	edge := math.Min(half-cx, half-cy)
	if d2-d1 > gap && edge > border {
		return lerp(patch, patchDark, d1*1.6), 1
	}
	return base, 1
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

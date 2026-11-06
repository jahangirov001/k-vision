//go:build ignore

// Draws the K-Vision icon as a 1024px PNG: a seven-spoke helm wheel (Kubernetes)
// whose hub is an eye's iris (vision), on a cobalt-to-violet tile.
// go run packaging/macos/icon.go out.png
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

func main() {
	const n = 1024
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	top, bot := color.RGBA{0x2E, 0x66, 0xE6, 0xFF}, color.RGBA{0x4A, 0x2A, 0xA8, 0xFF}
	white := color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	c := float64(n) / 2
	inset, radius := 100.0, 185.0 // macOS icon grid: tile inset, corner radius
	for y := 0; y < n; y++ {
		t := float64(y) / n
		row := color.RGBA{lerp(top.R, bot.R, t), lerp(top.G, bot.G, t), lerp(top.B, bot.B, t), 0xFF}
		for x := 0; x < n; x++ {
			fx, fy := float64(x)+.5, float64(y)+.5
			a := tile(fx, fy, inset, float64(n)-inset, radius)
			if a <= 0 {
				continue
			}
			px := row
			// wheel: rim, hub, eight spokes with handles
			d := math.Hypot(fx-c, fy-c)
			ang := math.Atan2(fy-c, fx-c)
			on := false
			if d > 205 && d < 245 {
				on = true
			}
			// hub: an iris -- a ring with a dark pupil and a highlight
			pupil := false
			if d < 78 {
				on = true
				if d < 40 {
					on, pupil = false, true
				}
				if math.Hypot(fx-(c+18), fy-(c-18)) < 11 {
					on, pupil = true, false
				}
			}
			for k := 0; k < 7; k++ {
				sa := float64(k)*2*math.Pi/7 - math.Pi/2
				da := math.Abs(math.Remainder(ang-sa, 2*math.Pi))
				if d > 78 && d < 330 && d*math.Sin(da) < 19 && math.Cos(da) > 0 {
					on = true
				}
				hx, hy := c+math.Cos(sa)*318, c+math.Sin(sa)*318
				if math.Hypot(fx-hx, fy-hy) < 34 {
					on = true
				}
			}
			if on {
				px = white
			} else if pupil {
				px = color.RGBA{0x10, 0x18, 0x40, 0xFF}
			}
			img.Set(x, y, color.RGBA{px.R, px.G, px.B, uint8(255 * a)})
		}
	}
	f, err := os.Create(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

func lerp(a, b uint8, t float64) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }

// tile returns 0..1 coverage of a rounded square, antialiased over one pixel.
func tile(x, y, lo, hi, r float64) float64 {
	cx := math.Max(lo+r, math.Min(x, hi-r))
	cy := math.Max(lo+r, math.Min(y, hi-r))
	d := math.Hypot(x-cx, y-cy) - r
	if x < lo || x > hi || y < lo || y > hi {
		return 0
	}
	return math.Max(0, math.Min(1, .5-d))
}

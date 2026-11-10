package menubar

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
)

// DMG window geometry in points; scripts/make-dmg.sh positions the Finder icons with the same numbers.
const (
	DMGWidth, DMGHeight = 640, 400
	DMGAppX, DMGAppY    = 170, 190 // icon centres
	DMGAppsX, DMGAppsY  = 470, 190
)

// DMGText is the caption drawn on the installer background.
const DMGText = "Drag HAL9001 to Applications"

// DMGBackground renders the installer window background at scale pixels per point (1 for background.png, 2 for
// background@2x.png): a dark vertical gradient, the HAL eye, a red arrow from the app icon to the Applications icon and
// DMGText underneath.
func DMGBackground(scale int) []byte {
	w, h := DMGWidth*scale, DMGHeight*scale
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	top, bottom := color.NRGBA{30, 31, 36, 255}, color.NRGBA{12, 12, 15, 255}
	for y := range h {
		c := lerp(top, bottom, float64(y)/float64(h))
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}

	eye := 76 * scale
	if src, err := png.Decode(bytes.NewReader(Icon(eye, false))); err == nil {
		draw.Draw(img, image.Rect(w/2-eye/2, 28*scale, w/2+eye/2, 28*scale+eye), src, image.Point{}, draw.Over)
	}

	red := color.NRGBA{220, 38, 38, 255}
	x0, x1 := float64((DMGAppX+84)*scale), float64((DMGAppsX-84)*scale)
	cy := float64(DMGAppY * scale)
	half := float64(5 * scale)
	head := float64(22 * scale)
	fill(img, func(x, y float64) bool {
		if x >= x0 && x <= x1-head && math.Abs(y-cy) <= half {
			return true
		}
		// triangular head: tip at x1
		return x > x1-head && x <= x1 && math.Abs(y-cy) <= (x1-x)/head*head*0.7
	}, red)

	drawText(img, DMGText, w/2, 330*scale, 3*scale, color.NRGBA{235, 235, 240, 255})

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// fill paints every pixel (sampled at its centre) for which in returns true.
func fill(img *image.NRGBA, in func(x, y float64) bool, c color.NRGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if in(float64(x)+0.5, float64(y)+0.5) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

// drawText draws s centred on cx with its top at y, in the 5x7 bitmap font below, each font pixel being px×px.
func drawText(img *image.NRGBA, s string, cx, y, px int, c color.NRGBA) {
	total := len(s)*6*px - px
	x := cx - total/2
	for _, r := range s {
		g, ok := glyphs[r]
		if ok {
			for row, bits := range g {
				for col := range 5 {
					if bits&(1<<(4-col)) != 0 {
						draw.Draw(img, image.Rect(x+col*px, y+row*px, x+(col+1)*px, y+(row+1)*px), image.NewUniform(c), image.Point{}, draw.Over)
					}
				}
			}
		}
		x += 6 * px
	}
}

// glyphs covers the characters of DMGText: 7 rows of 5 bits each.
var glyphs = map[rune][7]uint8{
	' ': {},
	'D': {0b11110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b11110},
	'H': {0b10001, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'A': {0b01110, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'L': {0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b11111},
	'9': {0b01110, 0b10001, 0b10001, 0b01111, 0b00001, 0b00010, 0b01100},
	'0': {0b01110, 0b10011, 0b10101, 0b10101, 0b10101, 0b11001, 0b01110},
	'1': {0b00100, 0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'r': {0b00000, 0b00000, 0b10110, 0b11001, 0b10000, 0b10000, 0b10000},
	'a': {0b00000, 0b00000, 0b01110, 0b00001, 0b01111, 0b10001, 0b01111},
	'g': {0b00000, 0b01111, 0b10001, 0b10001, 0b01111, 0b00001, 0b01110},
	't': {0b01000, 0b01000, 0b11100, 0b01000, 0b01000, 0b01001, 0b00110},
	'o': {0b00000, 0b00000, 0b01110, 0b10001, 0b10001, 0b10001, 0b01110},
	'p': {0b00000, 0b00000, 0b11110, 0b10001, 0b11110, 0b10000, 0b10000},
	'l': {0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'i': {0b00100, 0b00000, 0b01100, 0b00100, 0b00100, 0b00100, 0b01110},
	'c': {0b00000, 0b00000, 0b01110, 0b10000, 0b10000, 0b10001, 0b01110},
	'n': {0b00000, 0b00000, 0b10110, 0b11001, 0b10001, 0b10001, 0b10001},
	's': {0b00000, 0b00000, 0b01111, 0b10000, 0b01110, 0b00001, 0b11110},
}

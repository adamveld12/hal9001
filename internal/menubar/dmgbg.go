package menubar

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// DMG window geometry in points; scripts/dmg-settings.py positions the icons with the same numbers.
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

	eye := 56 * scale
	if src, err := png.Decode(bytes.NewReader(Icon(eye, false))); err == nil {
		draw.Draw(img, image.Rect(w/2-eye/2, 18*scale, w/2+eye/2, 18*scale+eye), src, image.Point{}, draw.Over)
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

	drawText(img, DMGText, w/2, 348*scale, float64(28*scale), color.NRGBA{200, 202, 210, 255})

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

// drawText draws s anti-aliased in Go Medium, centred on cx with its baseline at y; size is the font size in pixels.
func drawText(img *image.NRGBA, s string, cx, y int, size float64, c color.NRGBA) {
	f, err := opentype.Parse(gomedium.TTF)
	if err != nil {
		return
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return
	}
	defer func() { _ = face.Close() }()
	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: face}
	d.Dot = fixed.P(cx, y).Sub(fixed.Point26_6{X: d.MeasureString(s) / 2})
	d.DrawString(s)
}

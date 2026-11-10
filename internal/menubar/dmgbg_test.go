package menubar

import (
	"bytes"
	"image/png"
	"testing"
)

func TestDMGBackground(t *testing.T) {
	for _, scale := range []int{1, 2} {
		img, err := png.Decode(bytes.NewReader(DMGBackground(scale)))
		if err != nil {
			t.Fatal(err)
		}
		if b := img.Bounds(); b.Dx() != DMGWidth*scale || b.Dy() != DMGHeight*scale {
			t.Fatalf("scale %d: size %v", scale, b)
		}
		// The arrow midpoint is red and the corner is the dark background.
		r, g, _, _ := img.At((DMGAppX+DMGAppsX)/2*scale, DMGAppY*scale).RGBA()
		if r>>8 < 200 || g>>8 > 80 {
			t.Errorf("scale %d: no red arrow at the midpoint", scale)
		}
		if r, _, _, _ := img.At(2, 2).RGBA(); r>>8 > 60 {
			t.Errorf("scale %d: background not dark", scale)
		}
	}
}

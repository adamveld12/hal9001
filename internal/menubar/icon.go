package menubar

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"time"
)

// Icon renders the HAL 9000 eye as a PNG of size×size pixels: a dark outer ring, a red lens and a bright centre
// highlight. With dim set it is rendered in greys (daemon unreachable). Rendering is 4x supersampled.
// Use size 16 for 1x and 32 for 2x displays.
func Icon(size int, dim bool) []byte { return render(size, dim, 1) }

// Throb timing: one pulse period at ThrobFPS frames per second.
const (
	ThrobPeriod = 2500 * time.Millisecond
	ThrobFPS    = 12
	ThrobFrames = 30 // ThrobPeriod * ThrobFPS
	minGlow     = 0.4
)

// ThrobIcons precomputes one full pulse cycle of the eye: the lens glow eases between minGlow and full brightness
// (cosine easing, starting at full brightness).
func ThrobIcons(size int) [][]byte {
	frames := make([][]byte, ThrobFrames)
	for i := range frames {
		phase := (1 + math.Cos(2*math.Pi*float64(i)/ThrobFrames)) / 2 // 1 -> 0 -> 1
		frames[i] = render(size, false, minGlow+(1-minGlow)*phase)
	}
	return frames
}

// ShouldThrob reports whether the icon should animate: the daemon is reachable and at least one flow run is running.
func ShouldThrob(snap *Snapshot) bool { return snap != nil && snap.Status.RunningFlowRuns > 0 }

func render(size int, dim bool, glow float64) []byte {
	const ss = 4
	big := size * ss
	hi := image.NewNRGBA(image.Rect(0, 0, big, big))
	c := float64(big) / 2
	for y := range big {
		for x := range big {
			d := math.Hypot(float64(x)+0.5-c, float64(y)+0.5-c) / c // 0 centre .. 1 edge
			if col, ok := eyePixel(d, dim, glow); ok {
				hi.SetNRGBA(x, y, col)
			}
		}
	}
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			var r, g, b, a int
			for dy := range ss {
				for dx := range ss {
					p := hi.NRGBAAt(x*ss+dx, y*ss+dy)
					// premultiplied accumulation
					r += int(p.R) * int(p.A)
					g += int(p.G) * int(p.A)
					b += int(p.B) * int(p.A)
					a += int(p.A)
				}
			}
			if a > 0 {
				out.SetNRGBA(x, y, color.NRGBA{u8(r / a), u8(g / a), u8(b / a), u8(a / (ss * ss))})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, out)
	return buf.Bytes()
}

func lerp(a, b color.NRGBA, t float64) color.NRGBA {
	t = math.Max(0, math.Min(1, t))
	f := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.NRGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), 255}
}

// eyePixel returns the colour at normalised radius d (0 centre, 1 edge).
func eyePixel(d float64, dim bool, glow float64) (color.NRGBA, bool) {
	if d > 0.96 {
		return color.NRGBA{}, false
	}
	ring := color.NRGBA{18, 18, 20, 255}
	rim := color.NRGBA{70, 70, 76, 255}
	edge := color.NRGBA{120, 6, 6, 255}     // lens edge
	mid := color.NRGBA{225, 20, 14, 255}    // lens body
	core := color.NRGBA{255, 214, 160, 255} // highlight
	if dim {
		edge, mid, core = color.NRGBA{70, 70, 70, 255}, color.NRGBA{120, 120, 120, 255}, color.NRGBA{170, 170, 170, 255}
	}
	if glow < 1 { // dim the lens only; ring and rim stay put
		dark := color.NRGBA{18, 18, 20, 255}
		edge, mid, core = lerp(dark, edge, glow), lerp(dark, mid, glow), lerp(dark, core, glow)
	}
	switch {
	case d > 0.88: // thin metallic rim so the ring reads on dark menubars
		return rim, true
	case d > 0.62: // black ring
		return ring, true
	case d > 0.18:
		return lerp(mid, edge, (d-0.18)/0.44), true // glow darkens toward the lens edge
	default:
		return lerp(core, mid, d/0.18), true
	}
}

func u8(v int) uint8 { return uint8(min(max(v, 0), 255)) }

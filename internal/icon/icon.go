// Package icon draws TailConnect's icon at runtime (no binary assets):
// two linked device nodes. ICO wraps the PNG for the Windows tray.
package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
)

const size = 64

type variant struct{ active, template bool }

var (
	mu    sync.Mutex
	cache = map[variant][]byte{}
)

func get(v variant) []byte {
	mu.Lock()
	defer mu.Unlock()
	if b, ok := cache[v]; ok {
		return b
	}
	b := render(v.active, v.template)
	cache[v] = b
	return b
}

// PNG returns the colour icon; active=false renders a muted (signed-out) variant.
func PNG(active bool) []byte { return get(variant{active, false}) }

// Template returns a black, alpha-only icon for the macOS menu bar, which
// recolours template images to match light/dark mode. Inactive is faded.
func Template(active bool) []byte { return get(variant{active, true}) }

// ICO wraps PNG(active) in a single-image .ico container.
func ICO(active bool) []byte {
	p := PNG(active)
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, []uint16{0, 1, 1}) // reserved, type=icon, count
	buf.Write([]byte{size, size, 0, 0})                            // w, h, colors, reserved
	_ = binary.Write(&buf, binary.LittleEndian, []uint16{1, 32})   // planes, bpp
	_ = binary.Write(&buf, binary.LittleEndian, []uint32{uint32(len(p)), 22})
	buf.Write(p)
	return buf.Bytes()
}

type pt struct{ x, y float64 }

// distance from p to segment ab.
func segDist(p, a, b pt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	t := ((p.x-a.x)*dx + (p.y-a.y)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	cx, cy := a.x+t*dx, a.y+t*dy
	return math.Hypot(p.x-cx, p.y-cy)
}

func render(active, template bool) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, draw(size, active, template, false))
	return buf.Bytes()
}

// AppIcon renders the application icon (glyph on a dark rounded tile) at
// px×px, for the macOS .icns / Windows .ico / Linux desktop icon.
func AppIcon(px int) image.Image { return draw(px, true, false, true) }

// draw renders the glyph at px×px. Geometry is defined on a 64-unit grid.
func draw(px int, active, template, tile bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, px, px))
	accent := [3]float64{0x10, 0xb9, 0x81} // emerald
	second := [3]float64{0x38, 0xbd, 0xf8} // sky
	if !active {
		accent = [3]float64{0x94, 0xa3, 0xb8}
		second = [3]float64{0x64, 0x74, 0x8b}
	}
	fade := 1.0
	if template {
		accent, second = [3]float64{}, [3]float64{}
		if !active {
			fade = 0.45
		}
	}
	scale := float64(px) / size
	// On a tile the glyph is shrunk into the safe area (macOS icon grid).
	gscale, off := 1.0, 0.0
	if tile {
		gscale, off = 0.62, 32*(1-0.62)
	}
	a, b := pt{19*gscale + off, 45*gscale + off}, pt{45*gscale + off, 19*gscale + off}
	r, hole, link := 11.5*gscale, 4.5*gscale, 4.0*gscale
	tileBG := func(p pt) (float64, [3]float64) {
		// Rounded square inset to the macOS icon grid (824/1024).
		in, rad := 32*(1-824.0/1024), 64*0.185
		hx := math.Max(math.Abs(p.x-32)-(32-in-rad), 0)
		hy := math.Max(math.Abs(p.y-32)-(32-in-rad), 0)
		d := math.Hypot(hx, hy) - rad
		t := (p.y - in) / (64 - 2*in) // vertical gradient
		col := [3]float64{0x1b + 0x0c*(1-t), 0x22 + 0x10*(1-t), 0x2e + 0x14*(1-t)}
		return d, col
	}
	for y := 0; y < px; y++ {
		for x := 0; x < px; x++ {
			// Work in grid units; coverage uses distance in device pixels.
			p := pt{(float64(x) + .5) / scale, (float64(y) + .5) / scale}
			cov := func(d float64) float64 { return math.Max(0, math.Min(1, 0.5-d*scale)) }
			var cr, cg, cb, ca float64
			over := func(col [3]float64, v float64) {
				cr = col[0]*v + cr*(1-v)
				cg = col[1]*v + cg*(1-v)
				cb = col[2]*v + cb*(1-v)
				ca = v + ca*(1-v)
			}
			bgCov := 0.0
			var bgCol [3]float64
			if tile {
				d, col := tileBG(p)
				bgCov, bgCol = cov(d), col
				over(bgCol, bgCov)
			}
			over(second, cov(segDist(p, a, b)-link))
			over(accent, cov(math.Hypot(p.x-a.x, p.y-a.y)-r))
			over(second, cov(math.Hypot(p.x-b.x, p.y-b.y)-r))
			// "Screen" hole in each node: transparent, or the tile showing through.
			h := math.Max(cov(math.Hypot(p.x-a.x, p.y-a.y)-hole), cov(math.Hypot(p.x-b.x, p.y-b.y)-hole)) * 0.85
			if tile {
				cr = cr*(1-h) + bgCol[0]*bgCov*h
				cg = cg*(1-h) + bgCol[1]*bgCov*h
				cb = cb*(1-h) + bgCol[2]*bgCov*h
				ca = ca*(1-h) + bgCov*h
			} else {
				k := (1 - h) * fade
				cr, cg, cb, ca = cr*k, cg*k, cb*k, ca*k
			}
			if ca > 0 {
				img.SetNRGBA(x, y, color.NRGBA{uint8(math.Min(255, cr/ca)), uint8(math.Min(255, cg/ca)), uint8(math.Min(255, cb/ca)), uint8(ca * 255)})
			}
		}
	}
	return img
}

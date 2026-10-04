package pipeline

import (
	"math"
	"sync"

	"booth-display/controller/internal/config"
)

type pt struct {
	x, y float64
}

type logoShape struct {
	color byte // 1: white, 2: red (#c7000a)
	minX  float64
	maxX  float64
	minY  float64
	maxY  float64
	loops [][]pt
}

var rawLogoShapes = []logoShape{
	{color: 2, minX: 28.46, maxX: 45.85, minY: 16.94, maxY: 58.03, loops: [][]pt{
		{{45.84, 26.97}, {38.68, 22.83}, {38.67, 43.92}, {35.64, 45.68}, {35.64, 21.08}, {28.47, 16.94}, {28.46, 58.03}, {45.85, 47.99}, {45.84, 26.97}},
	}},
	{color: 1, minX: 45.21, maxX: 64.05, minY: 26.60, maxY: 48.36, loops: [][]pt{
		{{45.21, 26.60}, {45.21, 48.36}, {52.35, 44.23}, {52.38, 38.99}, {56.92, 41.60}, {64.05, 37.48}, {45.21, 26.60}},
	}},
	{color: 1, minX: 7.11, maxX: 33.07, minY: 4.60, maxY: 70.36, loops: [][]pt{
		{{17.34, 10.51}, {17.34, 10.51}, {7.11, 4.60}, {7.11, 70.36}, {14.28, 66.21}, {14.28, 17.00}, {17.34, 18.75}, {17.34, 64.44}, {24.52, 60.30}, {24.52, 22.93}, {29.48, 25.81}, {33.07, 19.59}, {17.34, 10.51}},
	}},
	{color: 1, minX: 64.60, maxX: 90.14, minY: 18.55, maxY: 57.00, loops: [][]pt{
		{{73.74, 25.77}, {64.60, 25.77}, {64.60, 18.55}, {90.14, 18.55}, {90.14, 25.77}, {81.00, 25.77}, {81.00, 57.00}, {73.74, 57.00}},
	}},
	{color: 1, minX: 92.23, maxX: 114.23, minY: 27.70, maxY: 57.00, loops: [][]pt{
		{{99.59, 49.70}, {114.23, 49.70}, {114.23, 57.00}, {92.23, 57.00}, {92.23, 27.70}, {114.23, 27.70}, {114.23, 45.58}, {99.59, 45.58}},
		{{99.59, 35.00}, {99.59, 40.00}, {106.90, 40.00}, {106.90, 35.00}},
	}},
	{color: 1, minX: 116.31, maxX: 138.31, minY: 27.70, maxY: 57.00, loops: [][]pt{
		{{131.00, 45.77}, {138.31, 45.77}, {138.31, 57.00}, {116.31, 57.00}, {116.31, 27.70}, {138.31, 27.70}, {138.31, 39.00}, {131.00, 39.00}, {131.00, 35.00}, {123.67, 35.00}, {123.67, 49.70}, {131.00, 49.70}},
	}},
	{color: 1, minX: 140.44, maxX: 162.42, minY: 18.55, maxY: 57.00, loops: [][]pt{
		{{140.44, 18.55}, {147.75, 18.55}, {147.75, 27.70}, {162.42, 27.70}, {162.42, 57.00}, {155.11, 57.00}, {155.11, 35.00}, {147.78, 35.00}, {147.78, 57.00}, {140.45, 57.00}},
	}},
	{color: 1, minX: 164.53, maxX: 186.53, minY: 27.70, maxY: 57.00, loops: [][]pt{
		{{171.86, 57.00}, {164.53, 57.00}, {164.53, 27.70}, {186.53, 27.70}, {186.53, 57.00}, {179.20, 57.00}, {179.20, 35.00}, {171.87, 35.00}},
	}},
	{color: 1, minX: 188.59, maxX: 210.59, minY: 27.70, maxY: 57.00, loops: [][]pt{
		{{210.59, 27.70}, {210.59, 57.00}, {188.59, 57.00}, {188.59, 27.70}},
		{{196.00, 35.00}, {196.00, 49.70}, {203.33, 49.70}, {203.33, 35.00}},
	}},
	{color: 1, minX: 212.71, maxX: 238.25, minY: 18.55, maxY: 57.00, loops: [][]pt{
		{{221.86, 25.77}, {212.71, 25.77}, {212.71, 18.55}, {238.25, 18.55}, {238.25, 25.77}, {229.10, 25.77}, {229.10, 57.00}, {221.85, 57.00}},
	}},
	{color: 1, minX: 240.37, maxX: 265.91, minY: 18.55, maxY: 57.00, loops: [][]pt{
		{{258.69, 18.55}, {265.91, 18.55}, {265.91, 57.00}, {240.37, 57.00}, {240.37, 18.55}, {247.62, 18.55}, {247.62, 49.78}, {258.69, 49.78}},
	}},
	{color: 1, minX: 268.00, maxX: 293.54, minY: 18.55, maxY: 57.00, loops: [][]pt{
		{{277.18, 25.77}, {268.00, 25.77}, {268.00, 18.55}, {293.54, 18.55}, {293.54, 25.77}, {284.39, 25.77}, {284.39, 57.00}, {277.14, 57.00}},
	}},
}

type LogoGenerator struct {
	canvas   config.CanvasConfig
	displays []config.DisplayConfig
	width    int
	height   int
	fps      int

	logoW       int
	logoH       int
	logoPixels  []logoPixel
	outlineMask []bool

	frameIdx int
	mu       sync.Mutex
}

type logoPixel struct {
	b, g, r byte
	a       byte
}

func NewLogoGenerator(canvas config.CanvasConfig, displays []config.DisplayConfig) *LogoGenerator {
	w := canvas.Width
	if w <= 0 {
		w = 5792
	}
	h := canvas.Height
	if h <= 0 {
		h = 540
	}
	fps := canvas.FPS
	if fps <= 0 {
		fps = 30
	}

	g := &LogoGenerator{
		canvas:   canvas,
		displays: displays,
		width:    w,
		height:   h,
		fps:      fps,
	}

	g.loadLogo()
	return g
}

func pointInPoly(px, py float64, poly []pt) bool {
	inside := false
	n := len(poly)
	for i := 0; i < n; i++ {
		p1 := poly[i]
		p2 := poly[(i+1)%n]
		if (p1.y > py) != (p2.y > py) {
			if px < (p2.x-p1.x)*(py-p1.y)/(p2.y-p1.y)+p1.x {
				inside = !inside
			}
		}
	}
	return inside
}

func (g *LogoGenerator) loadLogo() {
	scale := 7.0
	g.logoW = int(300.41*scale) + 1
	g.logoH = int(74.96*scale) + 1
	total := g.logoW * g.logoH

	g.logoPixels = make([]logoPixel, total)
	g.outlineMask = make([]bool, total)

	for _, s := range rawLogoShapes {
		ix0 := int(s.minX * scale)
		if ix0 < 0 {
			ix0 = 0
		}
		ix1 := int(s.maxX*scale) + 1
		if ix1 > g.logoW {
			ix1 = g.logoW
		}
		iy0 := int(s.minY * scale)
		if iy0 < 0 {
			iy0 = 0
		}
		iy1 := int(s.maxY*scale) + 1
		if iy1 > g.logoH {
			iy1 = g.logoH
		}

		var p logoPixel
		if s.color == 2 {
			// Red #c7000a -> BGR: [10, 0, 199]
			p = logoPixel{b: 10, g: 0, r: 199, a: 255}
		} else {
			// White -> BGR: [255, 255, 255]
			p = logoPixel{b: 255, g: 255, r: 255, a: 255}
		}

		for y := iy0; y < iy1; y++ {
			sy := float64(y) / scale
			rowIdx := y * g.logoW
			for x := ix0; x < ix1; x++ {
				sx := float64(x) / scale
				hits := 0
				for _, loop := range s.loops {
					if pointInPoly(sx, sy, loop) {
						hits++
					}
				}
				if hits%2 == 1 {
					g.logoPixels[rowIdx+x] = p
				}
			}
		}
	}

	// Compute sharp 1-pixel edge outline
	for y := 0; y < g.logoH; y++ {
		for x := 0; x < g.logoW; x++ {
			idx := y*g.logoW + x
			if g.logoPixels[idx].a > 0 {
				isEdge := false
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						nx, ny := x+dx, y+dy
						if nx < 0 || nx >= g.logoW || ny < 0 || ny >= g.logoH {
							isEdge = true
							break
						}
						nidx := ny*g.logoW + nx
						if g.logoPixels[nidx].a == 0 {
							isEdge = true
							break
						}
					}
					if isEdge {
						break
					}
				}
				g.outlineMask[idx] = isEdge
			}
		}
	}
}

func (g *LogoGenerator) NextFrame() []byte {
	g.mu.Lock()
	defer g.mu.Unlock()

	frame := make([]byte, g.width*g.height*4)

	// Set dark tech-navy background (BGR0: [10, 8, 5, 0])
	for i := 0; i < len(frame); i += 4 {
		frame[i+0] = 10
		frame[i+1] = 8
		frame[i+2] = 5
		frame[i+3] = 0
	}

	if g.logoW == 0 || g.logoH == 0 {
		return frame
	}

	centerY := g.height / 2
	top := centerY - g.logoH/2
	if top < 0 {
		top = 0
	}

	// Gap between logos and total period (pitch)
	gap := 280
	pitch := g.logoW + gap

	// Smooth slow crawl from right to left
	pixelsMoved := int(float64(g.frameIdx) * 8)
	g.frameIdx++

	// Base offset within pitch cycle
	offset := -(pixelsMoved % pitch)
	if offset > 0 {
		offset -= pitch
	}

	// Draw repeating logos across the screen width (from left margin to right edge)
	for startX := offset - pitch; startX < g.width+pitch; startX += pitch {
		if startX+g.logoW < 0 || startX >= g.width {
			continue
		}
		g.renderInstance(frame, startX, top)
	}

	return frame
}

func (g *LogoGenerator) renderInstance(frame []byte, left, top int) {
	for ly := 0; ly < g.logoH; ly++ {
		fy := top + ly
		if fy < 0 || fy >= g.height {
			continue
		}
		rowOffset := fy * g.width * 4

		for lx := 0; lx < g.logoW; lx++ {
			fx := left + lx
			if fx < 0 || fx >= g.width {
				continue
			}

			idx := ly*g.logoW + lx
			p := g.logoPixels[idx]
			if p.a == 0 {
				continue
			}

			isEdge := g.outlineMask[idx]
			fidx := rowOffset + fx*4

			var valB, valG, valR float64
			if isEdge {
				// Crisp glowing neon edge
				valB = math.Min(255.0, float64(p.b)*1.25+35.0)
				valG = math.Min(255.0, float64(p.g)*1.25+35.0)
				valR = math.Min(255.0, float64(p.r)*1.25+35.0)
			} else {
				valB = float64(p.b)
				valG = float64(p.g)
				valR = float64(p.r)
			}

			if byte(valB) > frame[fidx+0] {
				frame[fidx+0] = byte(valB)
			}
			if byte(valG) > frame[fidx+1] {
				frame[fidx+1] = byte(valG)
			}
			if byte(valR) > frame[fidx+2] {
				frame[fidx+2] = byte(valR)
			}
		}
	}
}

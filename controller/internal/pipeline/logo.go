package pipeline

import (
	"bytes"
	_ "embed"
	"image/png"
	"math"
	"sync"

	"booth-display/controller/internal/config"
)

//go:embed logo.png
var defaultLogoPNG []byte

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

func (g *LogoGenerator) loadLogo() {
	img, err := png.Decode(bytes.NewReader(defaultLogoPNG))
	if err != nil {
		return
	}

	bounds := img.Bounds()
	g.logoW = bounds.Dx()
	g.logoH = bounds.Dy()
	total := g.logoW * g.logoH

	g.logoPixels = make([]logoPixel, total)
	g.outlineMask = make([]bool, total)

	for y := 0; y < g.logoH; y++ {
		for x := 0; x < g.logoW; x++ {
			c := img.At(bounds.Min.X+x, bounds.Min.Y+y)
			r, gr, b, a := c.RGBA()
			idx := y*g.logoW + x
			if a > 0 {
				// Convert from premultiplied alpha
				alphaVal := byte(a >> 8)
				rVal := byte((r * 255) / a)
				gVal := byte((gr * 255) / a)
				bVal := byte((b * 255) / a)
				g.logoPixels[idx] = logoPixel{b: bVal, g: gVal, r: rVal, a: alphaVal}
			}
		}
	}

	// Compute sharp 1-pixel to 2-pixel edge outline
	for y := 0; y < g.logoH; y++ {
		for x := 0; x < g.logoW; x++ {
			idx := y*g.logoW + x
			if g.logoPixels[idx].a > 40 {
				isEdge := false
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						nx, ny := x+dx, y+dy
						if nx < 0 || nx >= g.logoW || ny < 0 || ny >= g.logoH {
							isEdge = true
							break
						}
						nidx := ny*g.logoW + nx
						if g.logoPixels[nidx].a <= 40 {
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

	cycleFrames := int(float64(g.fps) * 4.5) // 4.5s loop
	frameInCycle := g.frameIdx % cycleFrames
	g.frameIdx++

	// 3 Display centers (Display 1: 960, Display 2: 2880, Display 3: 4800)
	displayCentersX := []int{960, 2880, 4800}
	if len(g.displays) > 0 {
		displayCentersX = make([]int, 0, len(g.displays))
		for _, d := range g.displays {
			displayCentersX = append(displayCentersX, d.CropX+d.Width/2)
		}
	}

	centerY := g.height / 2
	top := centerY - g.logoH/2
	if top < 0 {
		top = 0
	}

	// Render animated logo patch
	phaseTime := float64(frameInCycle) / float64(g.fps)

	for _, cx := range displayCentersX {
		left := cx - g.logoW/2
		if left < 0 || left+g.logoW > g.width {
			continue
		}

		g.renderLogoPatch(frame, left, top, phaseTime)
	}

	return frame
}

func (g *LogoGenerator) renderLogoPatch(frame []byte, left, top int, t float64) {
	// Animation Phases:
	// Phase 1: 0.0s - 1.2s : Laser scanning edge-trace reveal
	// Phase 2: 1.2s - 2.0s : Solid body fill-in
	// Phase 3: 2.0s - 3.0s : Subtle breathing glow hold
	// Phase 4: 3.0s - 3.8s : Solid fadeout to outline and dissolve
	// Phase 5: 3.8s - 4.5s : Dark rest

	var scanX int = -999
	var edgeAlpha, solidAlpha float64
	var edgeBoost float64 = 1.0

	if t < 1.2 {
		p := t / 1.2
		scanX = int(p * float64(g.logoW+80)) - 40
		edgeAlpha = 1.0
		solidAlpha = 0.0
		edgeBoost = 1.4
	} else if t < 2.0 {
		fillP := (t - 1.2) / 0.8
		edgeAlpha = 1.0
		solidAlpha = fillP
		edgeBoost = 1.4 - fillP*0.4
	} else if t < 3.0 {
		holdP := (t - 2.0) / 1.0
		pulse := 1.0 + 0.08*math.Sin(holdP*math.Pi)
		edgeAlpha = 1.0
		solidAlpha = pulse
		if solidAlpha > 1.0 {
			solidAlpha = 1.0
		}
	} else if t < 3.8 {
		dissolveP := (t - 3.0) / 0.8
		if dissolveP < 0.4 {
			solidAlpha = 1.0 - dissolveP/0.4
			edgeAlpha = 1.0
		} else {
			solidAlpha = 0.0
			edgeAlpha = 1.0 - (dissolveP-0.4)/0.6
		}
	} else {
		// Dark rest
		return
	}

	for ly := 0; ly < g.logoH; ly++ {
		fy := top + ly
		if fy >= g.height {
			break
		}
		rowOffset := fy * g.width * 4

		for lx := 0; lx < g.logoW; lx++ {
			fx := left + lx
			if fx >= g.width {
				break
			}

			idx := ly*g.logoW + lx
			p := g.logoPixels[idx]
			if p.a == 0 {
				continue
			}

			isEdge := g.outlineMask[idx]

			// Scanline masking in Phase 1
			if scanX != -999 {
				if lx > scanX {
					continue
				}
			}

			var valB, valG, valR float64
			baseB := float64(p.b)
			baseG := float64(p.g)
			baseR := float64(p.r)

			if isEdge {
				// Spark highlight near scan front
				spark := 0.0
				if scanX != -999 {
					dist := math.Abs(float64(lx - scanX))
					if dist < 20 {
						spark = (1.0 - dist/20.0) * 120.0
					}
				}
				valB = (baseB*edgeBoost + 30.0 + spark) * edgeAlpha
				valG = (baseG*edgeBoost + 30.0 + spark) * edgeAlpha
				valR = (baseR*edgeBoost + 30.0 + spark) * edgeAlpha
			} else if solidAlpha > 0 {
				valB = baseB * solidAlpha
				valG = baseG * solidAlpha
				valR = baseR * solidAlpha
			} else {
				continue
			}

			fidx := rowOffset + fx*4
			// Alpha composite over background
			if valB > 255 {
				valB = 255
			}
			if valG > 255 {
				valG = 255
			}
			if valR > 255 {
				valR = 255
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

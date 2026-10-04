package pipeline

import (
	"math"
	"sync"

	"booth-display/controller/internal/config"
)

// TestPatternGenerator generates dynamic 5792x540 BGR0 test pattern frames in memory.
// It includes SMPTE-style color bars, display boundary markers, display labels,
// and a smoothly animated moving marker to easily verify synchronization and latency.
type TestPatternGenerator struct {
	width     int
	height    int
	fps       int
	displays  []config.DisplayConfig
	baseFrame []byte // cached static background (color bars, grid, boundaries)
	mu        sync.Mutex
	frameIdx  int
}

func NewTestPatternGenerator(canvas config.CanvasConfig, displays []config.DisplayConfig) *TestPatternGenerator {
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

	g := &TestPatternGenerator{
		width:    w,
		height:   h,
		fps:      fps,
		displays: displays,
	}
	g.initBaseFrame()
	return g
}

func (g *TestPatternGenerator) initBaseFrame() {
	g.baseFrame = make([]byte, g.width*g.height*4)

	// 8 SMPTE Colors (BGR0 format)
	colors := [][4]byte{
		{220, 220, 220, 0}, // Light Grey / White
		{0, 220, 220, 0},   // Yellow
		{220, 220, 0, 0},   // Cyan
		{0, 220, 0, 0},     // Green
		{220, 0, 220, 0},   // Magenta
		{0, 0, 220, 0},     // Red
		{220, 0, 0, 0},     // Blue
		{20, 20, 20, 0},    // Near Black
	}

	barHeight := int(float64(g.height) * 0.72)
	barWidth := g.width / len(colors)

	// Draw color bars
	for y := 0; y < barHeight; y++ {
		rowOffset := y * g.width * 4
		for x := 0; x < g.width; x++ {
			cIdx := x / barWidth
			if cIdx >= len(colors) {
				cIdx = len(colors) - 1
			}
			c := colors[cIdx]
			idx := rowOffset + x*4
			g.baseFrame[idx+0] = c[0]
			g.baseFrame[idx+1] = c[1]
			g.baseFrame[idx+2] = c[2]
			g.baseFrame[idx+3] = 0
		}
	}

	// Draw bottom gradient & display sections
	for y := barHeight; y < g.height; y++ {
		rowOffset := y * g.width * 4
		for x := 0; x < g.width; x++ {
			// Horizontal grayscale gradient
			val := byte((x * 255) / g.width)
			idx := rowOffset + x*4
			g.baseFrame[idx+0] = val
			g.baseFrame[idx+1] = val
			g.baseFrame[idx+2] = val
			g.baseFrame[idx+3] = 0
		}
	}

	// Draw display boundary guide lines and corner marks
	for _, d := range g.displays {
		dx := d.CropX
		dw := d.Width
		if dx+dw > g.width {
			continue
		}
		// Vertical boundary line at crop start and end
		g.drawVerticalLine(dx, 0, g.height, [4]byte{255, 255, 255, 0})
		if dx+dw-1 < g.width {
			g.drawVerticalLine(dx+dw-1, 0, g.height, [4]byte{255, 255, 255, 0})
		}
	}
}

func (g *TestPatternGenerator) drawVerticalLine(x, yStart, yEnd int, col [4]byte) {
	if x < 0 || x >= g.width {
		return
	}
	for y := yStart; y < yEnd; y++ {
		if (y/8)%2 == 0 { // dashed line
			idx := (y*g.width + x) * 4
			g.baseFrame[idx+0] = col[0]
			g.baseFrame[idx+1] = col[1]
			g.baseFrame[idx+2] = col[2]
			g.baseFrame[idx+3] = 0
		}
	}
}

// NextFrame copies the base frame and renders an animated moving bar + timestamp
func (g *TestPatternGenerator) NextFrame() []byte {
	g.mu.Lock()
	defer g.mu.Unlock()

	frame := make([]byte, len(g.baseFrame))
	copy(frame, g.baseFrame)

	// Animate a vertical scanner bar moving across the whole canvas
	speed := float64(g.width) / float64(g.fps*3) // Full sweep in 3 seconds
	barX := int(math.Mod(float64(g.frameIdx)*speed, float64(g.width)))
	g.frameIdx++

	barWidth := 24
	barHeight := g.height
	for y := 0; y < barHeight; y++ {
		rowOffset := y * g.width * 4
		for bx := 0; bx < barWidth; bx++ {
			x := (barX + bx) % g.width
			idx := rowOffset + x*4
			// Bright cyan-white scanner bar with inverted contrast
			frame[idx+0] = 255
			frame[idx+1] = 255
			frame[idx+2] = 255
			frame[idx+3] = 0
		}
	}

	return frame
}

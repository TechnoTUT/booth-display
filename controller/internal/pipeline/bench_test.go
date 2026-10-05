package pipeline

import (
	"testing"

	"booth-display/controller/internal/config"
)

func benchMixer() *CanvasMixer {
	c := config.CanvasConfig{Width: 5760, Height: 540, FPS: 30}
	d := []config.DisplayConfig{{ID: "a", Width: 1920, Height: 540}, {ID: "b", Width: 1920, Height: 540, CropX: 1920}, {ID: "c", Width: 1920, Height: 540, CropX: 3840}}
	return NewCanvasMixer(c, d)
}

func BenchmarkRenderTestPattern(b *testing.B) {
	m := benchMixer()
	out := make([]byte, m.frameSize)
	for i := 0; i < b.N; i++ {
		m.renderFrame(out)
	}
}

func BenchmarkBroadcast(b *testing.B) {
	m := benchMixer()
	for i := 0; i < 3; i++ {
		m.Subscribe()
	}
	for i := 0; i < b.N; i++ {
		m.broadcastFrame(m.getFrame())
	}
}

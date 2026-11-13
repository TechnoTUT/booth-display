package pipeline

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"booth-display/controller/internal/config"
)

// TestNDIMixerLatency measures the time from an NDI sender stamping a frame
// until that frame leaves the canvas mixer (the point where it is handed to
// the encoder). It needs a sender that stamps frames as described below, so it
// is opt-in. tools/ndi-stamp-sender is such a sender; from the repository root:
//
//	make ndi-stamp-sender
//	tools/ndi-stamp-sender/ndi-stamp-sender booth-latency-test &
//	cd controller && NDI_LATENCY_SOURCE="booth-latency-test" go test ./internal/pipeline -run TestNDIMixerLatency -v
//
// The sender must send 1920x1080 frames with a row of 48 black/white blocks,
// each 40x60 px, at rows 510-569: bit i (block i, white = 1) of the value
// carries wall-clock milliseconds mod 2^32 in bits 0-31. With the default
// 5760x540 canvas and center crop, block i lands at x = 120*i+60, y = 270.
// Sender and controller must run on the same host (same clock).
func TestNDIMixerLatency(t *testing.T) {
	source := os.Getenv("NDI_LATENCY_SOURCE")
	if source == "" {
		t.Skip("set NDI_LATENCY_SOURCE to a stamping NDI sender to run")
	}
	// Run from the repository root, where the controller normally starts.
	if root, err := filepath.Abs("../../.."); err == nil {
		if err := os.Chdir(root); err == nil {
			defer os.Chdir("controller/internal/pipeline")
		}
	}

	canvas := config.CanvasConfig{Width: 5760, Height: 540, FPS: 30}
	m := NewCanvasMixer(canvas, nil)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	r := m.Subscribe()
	if err := m.SwitchSource("ndi", source, TransitionCut, 0); err != nil {
		t.Fatal(err)
	}

	frame := make([]byte, canvas.Width*canvas.Height*4)
	var lat []float64
	deadline := time.Now().Add(20 * time.Second)
	var last uint32
	for len(lat) < 300 && time.Now().Before(deadline) {
		if _, err := io.ReadFull(r, frame); err != nil {
			t.Fatal(err)
		}
		now := uint32(time.Now().UnixMilli())
		var v uint32
		for i := range 32 {
			if frame[(270*canvas.Width+120*i+60)*4] > 128 {
				v |= 1 << i
			}
		}
		if v == 0 || v == last { // no NDI frame yet, or a repeated frame
			continue
		}
		last = v
		d := int32(now - v)
		if d < 0 || d > 2000 { // not a stamped frame
			continue
		}
		lat = append(lat, float64(d))
	}
	if err := m.SourceErr(); err != nil {
		t.Fatal(err)
	}
	if len(lat) < 30 {
		t.Fatalf("only %d stamped frames received", len(lat))
	}
	lat = lat[len(lat)/10:] // skip connection warm-up
	sort.Float64s(lat)
	t.Logf("NDI send -> mixer output: %d frames, median %.0f ms, p90 %.0f ms, max %.0f ms",
		len(lat), lat[len(lat)/2], lat[len(lat)*9/10], lat[len(lat)-1])
}

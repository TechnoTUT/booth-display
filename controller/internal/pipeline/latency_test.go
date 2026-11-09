package pipeline

import (
	"os"
	"os/exec"
	"sort"
	"testing"
	"time"

	"booth-display/controller/internal/config"
)

// TestEncoderLatency measures how long a raw canvas frame takes to come back
// out of the MultiEncoder ffmpeg process as a complete Access Unit, for the
// real argument list built by buildMultiArgs.
//
// It is opt-in because it takes several seconds and depends on the machine:
//
//	LATENCY_TEST=1 go test ./internal/pipeline -run TestEncoderLatency -v
//
// LIBVA_DRIVER_NAME=i965 exercises the legacy driver used on Ivy Bridge
// (e.g. Core i5-3470), and LATENCY_IVY=1 masks CPU features that Ivy Bridge
// lacks (AVX2, FMA3, BMI) in ffmpeg and x264.
func TestEncoderLatency(t *testing.T) {
	if os.Getenv("LATENCY_TEST") == "" {
		t.Skip("set LATENCY_TEST=1 to run")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	t.Run("libx264", func(t *testing.T) { measureEncoderLatency(t, false) })
	if _, err := os.Stat("/dev/dri/renderD128"); err == nil {
		t.Run("h264_vaapi", func(t *testing.T) { measureEncoderLatency(t, true) })
	}
}

func measureEncoderLatency(t *testing.T, vaapi bool) {
	canvas := config.CanvasConfig{Width: 5760, Height: 540, FPS: 30}
	displays := []config.DisplayConfig{
		{ID: "a", Width: 1920, Height: 540},
		{ID: "b", Width: 1920, Height: 540, CropX: 1920},
		{ID: "c", Width: 1920, Height: 540, CropX: 3840},
	}
	args := buildMultiArgs(canvas, displays, vaapi, "/dev/dri/renderD128")
	if os.Getenv("LATENCY_IVY") != "" {
		args = append([]string{"-cpuflags", "mmx+mmxext+sse+sse2+sse3+ssse3+sse4.1+sse4.2+avx+cmov"}, args...)
		for i, a := range args {
			if a == "-x264-params" {
				args[i+1] = "asm=MMX2,SSE,SSE2,SSE2Fast,SSSE3,SSE4.1,SSE4.2,AVX,LZCNT:" + args[i+1]
			}
		}
	}

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	readers := make([]*os.File, len(displays))
	for i := range displays {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		readers[i] = r
		cmd.ExtraFiles = append(cmd.ExtraFiles, w)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for _, w := range cmd.ExtraFiles {
		w.Close()
	}
	for _, r := range readers[1:] {
		go func(r *os.File) {
			fr := NewFLVReader(r)
			for {
				if _, _, err := fr.Next(); err != nil {
					return
				}
			}
		}(r)
	}

	const frames = 150
	sent := make([]time.Time, frames)
	go func() {
		frame := make([]byte, canvas.Width*canvas.Height*4)
		start := time.Now()
		for i := 0; i < frames; i++ {
			time.Sleep(time.Until(start.Add(time.Duration(i) * time.Second / 30)))
			for j := 0; j < len(frame); j += 4 {
				frame[j] = byte(i*3 + j>>4)
				frame[j+1] = byte(i + j>>10)
			}
			sent[i] = time.Now()
			if _, err := stdin.Write(frame); err != nil {
				return
			}
		}
		stdin.Close()
	}()

	var got []time.Time
	fr := NewFLVReader(readers[0])
	for {
		if _, _, err := fr.Next(); err != nil {
			break
		}
		got = append(got, time.Now())
	}
	_ = cmd.Wait()

	if len(got) < frames-5 {
		t.Fatalf("got %d access units, want about %d", len(got), frames)
	}
	var lat []float64
	for i := 15; i < len(got) && i < frames; i++ {
		lat = append(lat, float64(got[i].Sub(sent[i]).Microseconds())/1000)
	}
	sort.Float64s(lat)
	cpu := float64(cmd.ProcessState.UserTime()+cmd.ProcessState.SystemTime()) / float64(time.Duration(frames)*time.Second/30)
	t.Logf("frame written -> access unit ready: median %.1f ms, p90 %.1f ms; ffmpeg CPU %.0f%% of one core",
		lat[len(lat)/2], lat[len(lat)*9/10], cpu*100)
}

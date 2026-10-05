package pipeline

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"booth-display/controller/internal/config"
	"booth-display/controller/internal/protocol"
	"booth-display/controller/internal/streamer"
)

// MultiEncoder runs a single ffmpeg process that reads the full-canvas raw
// BGRX stream once, splits it, crops one region per display and encodes each
// to its own H.264 elementary stream. Every encoded stream is delivered to the
// matching display sender through an extra pipe (fd 3, 4, ...).
//
// Compared with one ffmpeg per display this transfers the 12MB/frame canvas
// over the stdin pipe once instead of once per display.
type MultiEncoder struct {
	displays []config.DisplayConfig
	senders  []*streamer.DisplaySender

	cancelFn context.CancelFunc
	cmd      *exec.Cmd
	wg       sync.WaitGroup
	running  bool
	mu       sync.Mutex
}

func NewMultiEncoder(displays []config.DisplayConfig, senders []*streamer.DisplaySender) *MultiEncoder {
	return &MultiEncoder{displays: displays, senders: senders}
}

// buildMultiArgs builds the ffmpeg arguments for a single-process, N-output encode.
func buildMultiArgs(canvas config.CanvasConfig, displays []config.DisplayConfig) []string {
	fps := canvas.FPS
	if fps <= 0 {
		fps = 30
	}
	n := len(displays)

	var fc strings.Builder
	fmt.Fprintf(&fc, "[0:v]split=%d", n)
	for i := range displays {
		fmt.Fprintf(&fc, "[s%d]", i)
	}
	for i, d := range displays {
		fmt.Fprintf(&fc, ";[s%d]crop=%d:%d:%d:%d[o%d]", i, d.Width, d.Height, d.CropX, d.CropY, i)
	}

	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo",
		"-pix_fmt", "bgr0",
		"-s", fmt.Sprintf("%dx%d", canvas.Width, canvas.Height),
		"-r", fmt.Sprintf("%d", fps),
		"-i", "pipe:0",
		"-filter_complex", fc.String(),
	}
	for i := range displays {
		args = append(args,
			"-map", fmt.Sprintf("[o%d]", i),
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-pix_fmt", "yuv420p",
			"-b:v", "4000k",
			"-maxrate", "4000k",
			"-bufsize", "1000k",
			"-g", fmt.Sprintf("%d", fps),
			// Receiver-friendly H.264: one slice per frame, AUD at every access
			// unit boundary, in-band SPS/PPS, Baseline profile.
			"-profile:v", "baseline",
			"-x264-params", "slices=1:sliced-threads=0:aud=1:repeat-headers=1",
			"-an",
			"-f", "h264",
			fmt.Sprintf("pipe:%d", 3+i),
		)
	}
	return args
}

// Start launches the ffmpeg process reading frames from inputReader.
func (m *MultiEncoder) Start(canvas config.CanvasConfig, inputReader io.Reader) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		m.stopInternal()
	}
	if len(m.displays) == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ffmpeg", buildMultiArgs(canvas, m.displays)...)
	cmd.Stdin = inputReader
	cmd.Stderr = os.Stderr

	readers := make([]*os.File, len(m.displays))
	writers := make([]*os.File, len(m.displays))
	closeAll := func() {
		for i := range readers {
			if readers[i] != nil {
				readers[i].Close()
			}
			if writers[i] != nil {
				writers[i].Close()
			}
		}
	}
	for i := range m.displays {
		r, w, err := os.Pipe()
		if err != nil {
			closeAll()
			cancel()
			return fmt.Errorf("failed to create output pipe: %w", err)
		}
		readers[i], writers[i] = r, w
	}
	cmd.ExtraFiles = writers

	if err := cmd.Start(); err != nil {
		closeAll()
		cancel()
		return fmt.Errorf("failed to start ffmpeg encoder: %w", err)
	}
	// The child holds its own copies of the write ends.
	for _, w := range writers {
		w.Close()
	}

	m.cancelFn = cancel
	m.cmd = cmd
	m.running = true

	for i, d := range m.displays {
		m.wg.Add(1)
		go func(r *os.File, d config.DisplayConfig, s *streamer.DisplaySender) {
			defer m.wg.Done()
			defer r.Close()
			pumpH264(ctx, r, d.ID, s)
		}(readers[i], d, m.senders[i])
	}

	go func() { _ = cmd.Wait() }()
	return nil
}

func (m *MultiEncoder) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopInternal()
}

func (m *MultiEncoder) stopInternal() {
	if !m.running {
		return
	}
	if m.cancelFn != nil {
		m.cancelFn()
	}
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
	m.wg.Wait()
	m.running = false
}

// pumpH264 parses an Annex-B H.264 stream into Access Units and sends each one
// to the display sender.
func pumpH264(ctx context.Context, r io.Reader, id string, sender *streamer.DisplaySender) {
	parser := NewNALUParser()
	assembler := NewAccessUnitAssembler()
	bufReader := bufio.NewReaderSize(r, 256*1024)
	chunk := make([]byte, 64*1024)
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := bufReader.Read(chunk)
		if n > 0 {
			for _, nal := range parser.Push(chunk[:n]) {
				if au := assembler.Push(nal); au != nil {
					ts := uint32(time.Since(startTime).Milliseconds())
					sender.SendFrame(au.Data, protocol.PayloadTypeH264, au.IsKeyframe, ts)
				}
			}
		}
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				log.Printf("[%s] ffmpeg pipe read error: %v", id, err)
			}
			return
		}
	}
}

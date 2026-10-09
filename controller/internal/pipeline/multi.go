package pipeline

import (
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
// Supports both Intel VA-API hardware encoding (h264_vaapi) for ultra-low CPU
// usage and software encoding (libx264) with automatic fallback.
type MultiEncoder struct {
	displays    []config.DisplayConfig
	senders     []*streamer.DisplaySender
	pipelineCfg config.PipelineConfig

	cancelFn context.CancelFunc
	cmd      *exec.Cmd
	input    io.ReadCloser // frame stream feeding the current ffmpeg process
	done     chan struct{} // closed once the current ffmpeg process has been reaped
	wg       sync.WaitGroup
	running  bool
	mu       sync.Mutex

	// exitErr is set by the monitor goroutine when ffmpeg exits without being stopped.
	// It is guarded by errMu, not mu, because the monitor must never wait on mu.
	exitErr error
	errMu   sync.Mutex
}

// encoderStartupProbe is how long a freshly started ffmpeg must keep running before
// it is considered healthy. An encoder that cannot open its device (e.g. VA-API)
// exits within a few hundred milliseconds.
const encoderStartupProbe = 2 * time.Second

func NewMultiEncoder(displays []config.DisplayConfig, senders []*streamer.DisplaySender, pipelineCfg config.PipelineConfig) *MultiEncoder {
	return &MultiEncoder{
		displays:    displays,
		senders:     senders,
		pipelineCfg: pipelineCfg,
	}
}

// determineEncoder decides whether to use VA-API based on config and device presence.
func determineEncoder(cfg config.PipelineConfig) (useVAAPI bool, device string) {
	device = cfg.VAAPIDevice
	if device == "" {
		device = "/dev/dri/renderD128"
	}

	enc := strings.ToLower(cfg.Encoder)
	switch enc {
	case "software", "x264", "libx264", "cpu":
		return false, ""
	case "vaapi", "hw", "gpu":
		return true, device
	default: // "auto" or empty
		if _, err := os.Stat(device); err == nil {
			return true, device
		}
		return false, ""
	}
}

// buildMultiArgs builds the ffmpeg arguments for single-process, N-output encode.
func buildMultiArgs(canvas config.CanvasConfig, displays []config.DisplayConfig, useVAAPI bool, vaDevice string) []string {
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

	var args []string
	if useVAAPI {
		for i, d := range displays {
			fmt.Fprintf(&fc, ";[s%d]crop=%d:%d:%d:%d,format=nv12,hwupload[o%d]", i, d.Width, d.Height, d.CropX, d.CropY, i)
		}

		args = []string{
			"-hide_banner", "-loglevel", "error",
			"-fflags", "nobuffer",
			"-flags", "low_delay",
			"-probesize", "32",
			"-analyzeduration", "0",
			"-init_hw_device", fmt.Sprintf("vaapi=va:%s", vaDevice),
			"-filter_hw_device", "va",
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
				"-c:v", "h264_vaapi",
				"-profile:v", "constrained_baseline",
				"-async_depth", "1",
				"-rc_mode", "CBR",
				"-aud", "1",
				"-bf", "0",
				"-g", fmt.Sprintf("%d", fps),
				"-idr_interval", fmt.Sprintf("%d", fps),
				"-b:v", "4000k",
				"-maxrate", "4000k",
				"-bufsize", "150k",
				"-an",
				"-f", "h264",
				fmt.Sprintf("pipe:%d", 3+i),
			)
		}
	} else {
		for i, d := range displays {
			fmt.Fprintf(&fc, ";[s%d]crop=%d:%d:%d:%d[o%d]", i, d.Width, d.Height, d.CropX, d.CropY, i)
		}

		args = []string{
			"-hide_banner", "-loglevel", "error",
			"-fflags", "nobuffer",
			"-flags", "low_delay",
			"-probesize", "32",
			"-analyzeduration", "0",
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
				"-bufsize", "400k",
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
	}
	return args
}

// Start launches the ffmpeg process. subscribe must return a new frame stream on
// every call: a failed attempt closes its stream, so the retry starts again from
// a frame boundary instead of reading the rest of a half-consumed stream.
// If VA-API fails during startup, the encoder falls back to libx264.
func (m *MultiEncoder) Start(canvas config.CanvasConfig, subscribe func() io.ReadCloser) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		m.stopInternal()
	}
	if len(m.displays) == 0 {
		return nil
	}

	useVAAPI, vaDevice := determineEncoder(m.pipelineCfg)
	if useVAAPI {
		log.Printf("[MultiEncoder] Starting hardware encoder: VA-API (%s)", vaDevice)
		err := m.startProcess(canvas, subscribe(), true, vaDevice)
		if err == nil {
			return nil
		}
		log.Printf("[MultiEncoder] VA-API hardware encoding failed: %v. Falling back to software libx264.", err)
	}

	log.Printf("[MultiEncoder] Starting software encoder: libx264 (ultrafast)")
	return m.startProcess(canvas, subscribe(), false, "")
}

// Err returns the reason the ffmpeg process exited unexpectedly, or nil while it is healthy.
func (m *MultiEncoder) Err() error {
	m.errMu.Lock()
	defer m.errMu.Unlock()
	return m.exitErr
}

func (m *MultiEncoder) startProcess(canvas config.CanvasConfig, input io.ReadCloser, useVAAPI bool, vaDevice string) error {
	ctx, cancel := context.WithCancel(context.Background())
	args := buildMultiArgs(canvas, m.displays, useVAAPI, vaDevice)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdin = input
	cmd.Stderr = os.Stderr

	m.errMu.Lock()
	m.exitErr = nil
	m.errMu.Unlock()

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
		_ = input.Close()
		return fmt.Errorf("failed to start ffmpeg encoder: %w", err)
	}
	// The child holds its own copies of the write ends.
	for _, w := range writers {
		w.Close()
	}

	m.cancelFn = cancel
	m.cmd = cmd
	m.input = input
	m.running = true

	done := make(chan struct{})
	m.done = done
	go func() {
		err := cmd.Wait()
		// Only an exit we did not ask for counts as a failure; stopInternal cancels ctx first.
		if ctx.Err() == nil {
			if err == nil {
				err = fmt.Errorf("ffmpeg exited unexpectedly")
			}
			m.errMu.Lock()
			m.exitErr = fmt.Errorf("encoder stopped: %w", err)
			m.errMu.Unlock()
			log.Printf("[MultiEncoder] %v", m.Err())
		}
		close(done)
	}()

	for i, d := range m.displays {
		m.wg.Add(1)
		go func(r *os.File, d config.DisplayConfig, s *streamer.DisplaySender) {
			defer m.wg.Done()
			defer r.Close()
			pumpH264(ctx, r, d.ID, s)
		}(readers[i], d, m.senders[i])
	}

	// Startup probe: an encoder that cannot open its device exits almost at once.
	// Report that as a failure so the caller can fall back to another encoder.
	select {
	case <-done:
		err := m.Err()
		if err == nil {
			err = fmt.Errorf("ffmpeg exited during startup")
		}
		m.stopInternal()
		return err
	case <-time.After(encoderStartupProbe):
		return nil
	}
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
	// Closing the frame stream unblocks ffmpeg's stdin copier so Wait can return.
	if m.input != nil {
		_ = m.input.Close()
	}
	m.wg.Wait()
	// Reap ffmpeg before reporting the encoder as stopped.
	if m.done != nil {
		<-m.done
	}
	m.running = false
}

// pumpH264 parses an Annex-B H.264 stream into Access Units and sends each one
// to the display sender.
func pumpH264(ctx context.Context, r io.Reader, id string, sender *streamer.DisplaySender) {
	parser := NewNALUParser()
	assembler := NewAccessUnitAssembler()
	chunk := make([]byte, 16*1024)
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := r.Read(chunk)
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

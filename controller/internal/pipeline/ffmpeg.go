package pipeline

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"

	"booth-display/controller/internal/config"
	"booth-display/controller/internal/protocol"
	"booth-display/controller/internal/streamer"
)

type DisplayPipeline struct {
	display config.DisplayConfig
	sender  *streamer.DisplaySender

	cancelFn context.CancelFunc
	cmd      *exec.Cmd
	wg       sync.WaitGroup
	running  bool
	mu       sync.Mutex
}

func NewDisplayPipeline(d config.DisplayConfig, sender *streamer.DisplaySender) *DisplayPipeline {
	return &DisplayPipeline{
		display: d,
		sender:  sender,
	}
}

// Start launches the ffmpeg encoding process for this display crop.
func (p *DisplayPipeline) Start(mode string, mediaTarget string, canvas config.CanvasConfig, inputReader io.Reader) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		p.stopInternal()
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancelFn = cancel

	var args []string
	cropFilter := fmt.Sprintf("crop=%d:%d:%d:%d", p.display.Width, p.display.Height, p.display.CropX, p.display.CropY)

	fps := canvas.FPS
	if fps <= 0 {
		fps = 30
	}

	if inputReader != nil {
		// Raw BGRX pipe input mode (fed by CanvasMixer or NDI)
		args = []string{
			"-f", "rawvideo",
			"-pix_fmt", "bgr0",
			"-s", fmt.Sprintf("%dx%d", canvas.Width, canvas.Height),
			"-r", fmt.Sprintf("%d", fps),
			"-i", "pipe:0",
			"-vf", cropFilter,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-pix_fmt", "yuv420p",
			"-b:v", "4000k",
			"-maxrate", "4000k",
			"-bufsize", "1000k",
			"-g", fmt.Sprintf("%d", fps),
			"-an",
			"-f", "h264",
			"pipe:1",
		}
	} else if mode == "video" && mediaTarget != "" {
		// Video file looping mode
		args = []string{
			"-re",
			"-stream_loop", "-1",
			"-i", mediaTarget,
			"-vf", cropFilter,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-pix_fmt", "yuv420p",
			"-b:v", "4000k",
			"-maxrate", "4000k",
			"-bufsize", "1000k",
			"-g", fmt.Sprintf("%d", fps),
			"-an",
			"-f", "h264",
			"pipe:1",
		}
	} else {
		// Built-in animated test pattern mode (testsrc)
		// Uses lavfi testsrc spanning full canvas width, overlaying display label and animated cross-screen motion
		lavfiSource := fmt.Sprintf("testsrc=size=%dx%d:rate=%d", canvas.Width, canvas.Height, fps)
		labelFilter := fmt.Sprintf("%s,drawtext=text='%s [%dx%d]':x=40:y=40:fontsize=36:fontcolor=white:box=1:boxcolor=black@0.6",
			cropFilter, p.display.Name, p.display.Width, p.display.Height)

		args = []string{
			"-re",
			"-f", "lavfi",
			"-i", lavfiSource,
			"-vf", labelFilter,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-pix_fmt", "yuv420p",
			"-b:v", "3000k",
			"-maxrate", "3000k",
			"-bufsize", "800k",
			"-g", fmt.Sprintf("%d", fps),
			"-an",
			"-f", "h264",
			"pipe:1",
		}
	}

	// Receiver-friendly H.264: one slice per frame, AUD at every access unit
	// boundary (used by readAndSendLoop to frame the stream), SPS/PPS repeated
	// in-band, and Baseline profile for maximum hardware decoder compatibility.
	// Inserted just before the trailing output args ("-f", "h264", "pipe:1").
	encoderOpts := []string{
		"-profile:v", "baseline",
		"-x264-params", "slices=1:sliced-threads=0:aud=1:repeat-headers=1",
	}
	outIdx := len(args) - 3
	args = append(args[:outIdx:outIdx], append(encoderOpts, args[outIdx:]...)...)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if inputReader != nil {
		cmd.Stdin = inputReader
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to open ffmpeg stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start ffmpeg for display %s: %w", p.display.ID, err)
	}

	p.cmd = cmd
	p.running = true
	p.wg.Add(1)

	go p.readAndSendLoop(ctx, stdout)

	return nil
}

func (p *DisplayPipeline) readAndSendLoop(ctx context.Context, r io.Reader) {
	defer p.wg.Done()

	parser := NewNALUParser()
	assembler := NewAccessUnitAssembler()
	bufReader := bufio.NewReaderSize(r, 64*1024)
	chunk := make([]byte, 16*1024)
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			n, err := bufReader.Read(chunk)
			if err != nil {
				if err != io.EOF {
					log.Printf("[%s] ffmpeg pipe read error: %v", p.display.ID, err)
				}
				return
			}

			if n > 0 {
				for _, nal := range parser.Push(chunk[:n]) {
					// Send one complete Access Unit (all slices of a frame) per
					// SendFrame call, stamped once per frame.
					if au := assembler.Push(nal); au != nil {
						timestampMs := uint32(time.Since(startTime).Milliseconds())
						p.sender.SendFrame(au.Data, protocol.PayloadTypeH264, au.IsKeyframe, timestampMs)
					}
				}
			}
		}
	}
}

func (p *DisplayPipeline) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopInternal()
}

func (p *DisplayPipeline) stopInternal() {
	if !p.running {
		return
	}
	if p.cancelFn != nil {
		p.cancelFn()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	p.wg.Wait()
	p.running = false
}

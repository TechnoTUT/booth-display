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

	if mode == "ndi" && inputReader != nil {
		// NDI raw BGRX pipe input mode
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

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if inputReader != nil && mode == "ndi" {
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
				nalUnits := parser.Push(chunk[:n])
				timestampMs := uint32(time.Since(startTime).Milliseconds())

				for _, nal := range nalUnits {
					p.sender.SendFrame(nal.Data, protocol.PayloadTypeH264, nal.IsKeyframe, timestampMs)
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

package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"

	"booth-display/controller/internal/config"
)

type PreviewBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[chan []byte]bool
	latestFrame []byte
}

func NewPreviewBroadcaster() *PreviewBroadcaster {
	return &PreviewBroadcaster{
		subscribers: make(map[chan []byte]bool),
	}
}

func (b *PreviewBroadcaster) Subscribe() chan []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan []byte, 5)
	b.subscribers[ch] = true
	if len(b.latestFrame) > 0 {
		ch <- b.latestFrame
	}
	return ch
}

func (b *PreviewBroadcaster) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers, ch)
	close(ch)
}

func (b *PreviewBroadcaster) Broadcast(frame []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.latestFrame = frame
	for ch := range b.subscribers {
		select {
		case ch <- frame:
		default:
			// Slow consumer, skip frame
		}
	}
}

func (b *PreviewBroadcaster) GetLatest() []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.latestFrame
}

type PreviewPipeline struct {
	broadcaster *PreviewBroadcaster
	cancelFn    context.CancelFunc
	cmd         *exec.Cmd
	wg          sync.WaitGroup
	running     bool
	mu          sync.Mutex
}

func NewPreviewPipeline(broadcaster *PreviewBroadcaster) *PreviewPipeline {
	return &PreviewPipeline{
		broadcaster: broadcaster,
	}
}

func (p *PreviewPipeline) Start(mode string, mediaTarget string, canvas config.CanvasConfig, inputReader io.Reader) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		p.stopInternal()
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancelFn = cancel

	var args []string
	previewWidth := 1280
	// Keep aspect ratio of the canvas (e.g. 5792x540 -> 1280x119)
	scaleFilter := fmt.Sprintf("scale=%d:-1", previewWidth)

	fps := 15 // 15 fps preview is smooth and very low CPU

	if mode == "ndi" && inputReader != nil {
		args = []string{
			"-f", "rawvideo",
			"-pix_fmt", "bgr0",
			"-s", fmt.Sprintf("%dx%d", canvas.Width, canvas.Height),
			"-r", fmt.Sprintf("%d", canvas.FPS),
			"-i", "pipe:0",
			"-vf", scaleFilter,
			"-r", fmt.Sprintf("%d", fps),
			"-q:v", "5",
			"-f", "image2pipe",
			"-vcodec", "mjpeg",
			"pipe:1",
		}
	} else if mode == "video" && mediaTarget != "" {
		args = []string{
			"-re",
			"-stream_loop", "-1",
			"-i", mediaTarget,
			"-vf", scaleFilter,
			"-r", fmt.Sprintf("%d", fps),
			"-q:v", "5",
			"-f", "image2pipe",
			"-vcodec", "mjpeg",
			"pipe:1",
		}
	} else {
		lavfiSource := fmt.Sprintf("testsrc=size=%dx%d:rate=30", canvas.Width, canvas.Height)
		textFilter := fmt.Sprintf("%s,drawtext=text='BOOTH-DISPLAY LIVE PREVIEW':x=20:y=20:fontsize=24:fontcolor=white:box=1:boxcolor=black@0.6", scaleFilter)
		args = []string{
			"-re",
			"-f", "lavfi",
			"-i", lavfiSource,
			"-vf", textFilter,
			"-r", fmt.Sprintf("%d", fps),
			"-q:v", "5",
			"-f", "image2pipe",
			"-vcodec", "mjpeg",
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
		return fmt.Errorf("failed to open preview ffmpeg stdout: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start preview ffmpeg: %w", err)
	}

	p.cmd = cmd
	p.running = true
	p.wg.Add(1)

	go p.readLoop(ctx, stdout)

	return nil
}

func (p *PreviewPipeline) readLoop(ctx context.Context, r io.Reader) {
	defer p.wg.Done()

	bufReader := bufio.NewReaderSize(r, 64*1024)
	var buffer []byte
	chunk := make([]byte, 32*1024)

	soi := []byte{0xFF, 0xD8}
	eoi := []byte{0xFF, 0xD9}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			n, err := bufReader.Read(chunk)
			if err != nil {
				if err != io.EOF {
					log.Printf("Preview read error: %v", err)
				}
				return
			}

			if n > 0 {
				buffer = append(buffer, chunk[:n]...)

				for {
					startIdx := bytes.Index(buffer, soi)
					if startIdx == -1 {
						if len(buffer) > 2 {
							buffer = buffer[len(buffer)-2:]
						}
						break
					}

					endIdx := bytes.Index(buffer[startIdx+2:], eoi)
					if endIdx == -1 {
						buffer = buffer[startIdx:]
						break
					}

					// Full JPEG found
					frameEnd := startIdx + 2 + endIdx + 2
					jpegData := make([]byte, frameEnd-startIdx)
					copy(jpegData, buffer[startIdx:frameEnd])

					p.broadcaster.Broadcast(jpegData)

					buffer = buffer[frameEnd:]
				}
			}
		}
	}
}

func (p *PreviewPipeline) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopInternal()
}

func (p *PreviewPipeline) stopInternal() {
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

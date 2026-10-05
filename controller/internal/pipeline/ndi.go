package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"booth-display/controller/internal/config"
)

type NDISource struct {
	Name       string  `json:"name"`
	StreamName *string `json:"stream_name"`
	HostName   *string `json:"host_name"`
}

// The NDI bridge is run with the uv project (.venv) at the repository root
// ("uv run --project ."), so the controller must be started from the repository root.
// Run "make setup-python" once to create the virtual environment.
const ndiBridgeScript = "controller/scripts/ndi_bridge.py"

// DiscoverNDISources runs the Python ndi_bridge via uv to find active NDI senders on the network.
func DiscoverNDISources() ([]NDISource, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "uv", "run", "--project", ".", "python", ndiBridgeScript, "--discover")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to discover NDI sources: %w", err)
	}

	var sources []NDISource
	if err := json.Unmarshal(out, &sources); err != nil {
		return nil, fmt.Errorf("failed to parse NDI sources JSON: %w (output: %s)", err, string(out))
	}

	return sources, nil
}

// NDIDistributor runs the python ndi_bridge process, reads raw BGRX frames of the target canvas,
// and buffers the latest frame using zero-lock double-buffering for glitch-free rendering.
type NDIDistributor struct {
	sourceName string
	canvas     config.CanvasConfig
	cancelFn   context.CancelFunc
	cmd        *exec.Cmd
	wg         sync.WaitGroup
	running    bool
	mu         sync.Mutex

	// Double-buffering for tear-free, non-blocking frame retrieval
	frontBuf []byte
	backBuf  []byte
	hasFrame bool
	bufMu    sync.RWMutex

	subscribers map[io.WriteCloser]bool
	subMu       sync.Mutex
}

func NewNDIDistributor(sourceName string, canvas config.CanvasConfig) *NDIDistributor {
	w := canvas.Width
	if w <= 0 {
		w = 5792
	}
	h := canvas.Height
	if h <= 0 {
		h = 540
	}
	frameBytes := w * h * 4

	return &NDIDistributor{
		sourceName:  sourceName,
		canvas:      canvas,
		frontBuf:    make([]byte, frameBytes),
		backBuf:     make([]byte, frameBytes),
		subscribers: make(map[io.WriteCloser]bool),
	}
}

// expandPipeBuffer attempts to expand the OS pipe buffer capacity on Linux to reduce context switching.
func expandPipeBuffer(rc io.ReadCloser) {
	if f, ok := rc.(interface{ Fd() uintptr }); ok {
		// F_SETPIPE_SZ = 1031 on Linux, set to 512KB (safe for unprivileged user)
		_, _, _ = syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), 1031, 524288)
	}
}

func (d *NDIDistributor) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.running {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.cancelFn = cancel

	fps := d.canvas.FPS
	if fps <= 0 {
		fps = 30
	}

	cmd := exec.CommandContext(ctx, "uv", "run", "--project", ".", "python",
		ndiBridgeScript,
		"--source", d.sourceName,
		"--width", fmt.Sprintf("%d", d.canvas.Width),
		"--height", fmt.Sprintf("%d", d.canvas.Height),
		"--fps", fmt.Sprintf("%d", fps),
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create NDI bridge stdout pipe: %w", err)
	}
	expandPipeBuffer(stdout)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create NDI bridge stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start NDI bridge: %w", err)
	}

	d.cmd = cmd
	d.running = true
	d.wg.Add(2)

	go func() {
		defer d.wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("[ndi_bridge] %s", scanner.Text())
		}
	}()

	go d.readLoop(ctx, stdout)

	return nil
}

// CopyLatestFrame copies the latest received NDI frame into dst.
// Returns true if a valid frame was copied, false if no frame has arrived yet.
func (d *NDIDistributor) CopyLatestFrame(dst []byte) bool {
	d.bufMu.RLock()
	defer d.bufMu.RUnlock()

	if !d.hasFrame {
		return false
	}
	copy(dst, d.frontBuf)
	return true
}

func (d *NDIDistributor) Subscribe() io.ReadCloser {
	d.subMu.Lock()
	defer d.subMu.Unlock()

	pr, pw := io.Pipe()
	d.subscribers[pw] = true
	return pr
}

func (d *NDIDistributor) Unsubscribe(r io.ReadCloser) {
	_ = r.Close()
}

func (d *NDIDistributor) readLoop(ctx context.Context, r io.Reader) {
	defer d.wg.Done()

	frameBytes := len(d.backBuf)
	bufReader := bufio.NewReaderSize(r, frameBytes*2)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, err := io.ReadFull(bufReader, d.backBuf)
			if err != nil {
				if err != io.EOF && ctx.Err() == nil {
					log.Printf("[NDIDistributor] Read frame error: %v", err)
				}
				return
			}

			// Atomic pointer swap of double-buffered frame
			d.bufMu.Lock()
			d.frontBuf, d.backBuf = d.backBuf, d.frontBuf
			d.hasFrame = true
			d.bufMu.Unlock()

			// Broadcast frame to external pipe subscribers if any
			d.subMu.Lock()
			if len(d.subscribers) > 0 {
				d.bufMu.RLock()
				for w := range d.subscribers {
					if _, err := w.Write(d.frontBuf); err != nil {
						_ = w.Close()
						delete(d.subscribers, w)
					}
				}
				d.bufMu.RUnlock()
			}
			d.subMu.Unlock()
		}
	}
}

func (d *NDIDistributor) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.running {
		return
	}

	if d.cancelFn != nil {
		d.cancelFn()
	}

	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
	}

	d.subMu.Lock()
	for w := range d.subscribers {
		_ = w.Close()
	}
	d.subscribers = make(map[io.WriteCloser]bool)
	d.subMu.Unlock()

	d.wg.Wait()
	d.running = false
}

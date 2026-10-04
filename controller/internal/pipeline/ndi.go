package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"booth-display/controller/internal/config"
)

type NDISource struct {
	Name       string  `json:"name"`
	StreamName *string `json:"stream_name"`
	HostName   *string `json:"host_name"`
}

// ndiEnvDir returns the uv project directory used to run the NDI bridge.
// Override with BOOTH_NDI_ENV_DIR (default: $HOME/utone-ndi-utils).
func ndiEnvDir() string {
	if dir := os.Getenv("BOOTH_NDI_ENV_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "utone-ndi-utils")
}

// ndiBridgeScript returns the path of ndi_bridge.py.
// Override with BOOTH_NDI_BRIDGE_SCRIPT (default: controller/scripts/ndi_bridge.py relative to the repository root).
func ndiBridgeScript() string {
	if p := os.Getenv("BOOTH_NDI_BRIDGE_SCRIPT"); p != "" {
		return p
	}
	abs, err := filepath.Abs(filepath.Join("controller", "scripts", "ndi_bridge.py"))
	if err != nil {
		return filepath.Join("controller", "scripts", "ndi_bridge.py")
	}
	return abs
}

// DiscoverNDISources runs the Python ndi_bridge via uv to find active NDI senders on the network.
func DiscoverNDISources() ([]NDISource, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "uv", "run", "--directory", ndiEnvDir(), "python", ndiBridgeScript(), "--discover")
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
// and distributes each frame to multiple subscriber pipes (for displays and preview).
type NDIDistributor struct {
	sourceName string
	canvas     config.CanvasConfig
	cancelFn   context.CancelFunc
	cmd        *exec.Cmd
	wg         sync.WaitGroup
	running    bool
	mu         sync.Mutex

	subscribers map[io.WriteCloser]bool
	subMu       sync.Mutex
}

func NewNDIDistributor(sourceName string, canvas config.CanvasConfig) *NDIDistributor {
	return &NDIDistributor{
		sourceName:  sourceName,
		canvas:      canvas,
		subscribers: make(map[io.WriteCloser]bool),
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

	cmd := exec.CommandContext(ctx, "uv", "run", "--directory", ndiEnvDir(), "python",
		ndiBridgeScript(),
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

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start NDI bridge: %w", err)
	}

	d.cmd = cmd
	d.running = true
	d.wg.Add(1)

	go d.readLoop(ctx, stdout)

	return nil
}

func (d *NDIDistributor) Subscribe() io.ReadCloser {
	d.subMu.Lock()
	defer d.subMu.Unlock()

	pr, pw := io.Pipe()
	d.subscribers[pw] = true
	return pr
}

func (d *NDIDistributor) Unsubscribe(r io.ReadCloser) {
	// r is the pipe reader; subscribers contains pw
	_ = r.Close()
}

func (d *NDIDistributor) readLoop(ctx context.Context, r io.Reader) {
	defer d.wg.Done()

	frameBytes := d.canvas.Width * d.canvas.Height * 4 // BGRX is 4 bytes per pixel
	if frameBytes <= 0 {
		frameBytes = 5792 * 540 * 4
	}

	bufReader := bufio.NewReaderSize(r, frameBytes*2)
	frame := make([]byte, frameBytes)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, err := io.ReadFull(bufReader, frame)
			if err != nil {
				if err != io.EOF && ctx.Err() == nil {
					log.Printf("[NDIDistributor] Read frame error: %v", err)
				}
				return
			}

			// Broadcast frame to all subscribers
			d.subMu.Lock()
			for w := range d.subscribers {
				if _, err := w.Write(frame); err != nil {
					_ = w.Close()
					delete(d.subscribers, w)
				}
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

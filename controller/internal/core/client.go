package core

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
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

// Message kinds on the core's stdout ([kind u8][flags u8][len u32 LE][payload]).
const (
	kindJSON = 0
	kindJPEG = 2
)

const (
	// commandTimeout bounds one command. Starting may wait for the 2 s
	// startup probe of the vaapi-ffmpeg encoder before falling back to libx264.
	commandTimeout = 15 * time.Second
	// discoverTimeout bounds NDI discovery, which settles for up to 4 s.
	discoverTimeout = 15 * time.Second
	restartDelay    = time.Second
	maxMessageSize  = 64 << 20
)

// ErrNotRunning is returned while the core process is down (it is restarted automatically).
var ErrNotRunning = errors.New("media core (booth-core) is not running")

type reply struct {
	OK      bool        `json:"ok"`
	Error   string      `json:"error"`
	Sources []NDISource `json:"sources"`
}

// process is one running booth-core.
type process struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{} // closed once stdout is at EOF and the process is reaped
}

// Client runs booth-core and supervises it: if it exits unexpectedly, it is
// started again and given the last configuration and playback state.
type Client struct {
	bin     string
	preview *PreviewBroadcaster

	mu      sync.Mutex
	proc    *process
	nextID  uint64
	pending map[uint64]chan reply
	closed  bool

	// Replayed after a restart.
	configure map[string]any
	playing   map[string]any // the last start/switch, nil when stopped

	statusMu sync.RWMutex
	status   Status
}

// FindBinary locates booth-core: the given path, next to the controller
// executable, or on PATH.
func FindBinary(path string) (string, error) {
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("booth-core binary: %w", err)
		}
		return path, nil
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "booth-core")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("booth-core"); err == nil {
		return p, nil
	}
	return "", errors.New("booth-core binary not found next to the controller or on PATH (run \"make build\", or pass -core)")
}

func New(bin string) *Client {
	return &Client{
		bin:     bin,
		preview: NewPreviewBroadcaster(),
		pending: make(map[uint64]chan reply),
		status:  Status{Pipeline: PipelineStatus{ActiveMode: "testpattern"}, Scene: SceneStatus{Transition: "cut", DurationMs: 500}},
	}
}

// Start launches the core and keeps it running until Close.
func (c *Client) Start() error {
	p, err := c.spawn()
	if err != nil {
		return err
	}
	go c.supervise(p)
	return nil
}

func (c *Client) spawn() (*process, error) {
	cmd := exec.Command(c.bin, "engine")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start booth-core: %w", err)
	}
	p := &process{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() {
		c.readLoop(stdout)
		_ = cmd.Wait()
		close(p.done)
	}()
	c.mu.Lock()
	c.proc = p
	c.mu.Unlock()
	log.Printf("[core] started %s (pid %d)", c.bin, cmd.Process.Pid)
	return p, nil
}

func (c *Client) supervise(p *process) {
	for {
		<-p.done
		c.mu.Lock()
		closed := c.closed
		c.proc = nil
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if closed {
			return
		}
		log.Printf("[core] booth-core exited unexpectedly (%v); restarting", p.cmd.ProcessState)
		c.setStatusError("media core restarting")
		c.preview.clear()
		for {
			time.Sleep(restartDelay)
			var err error
			if p, err = c.spawn(); err == nil {
				break
			}
			log.Printf("[core] %v", err)
		}
		c.mu.Lock()
		cfg, playing := c.configure, c.playing
		c.mu.Unlock()
		if cfg != nil {
			if _, err := c.request(cfg, commandTimeout); err != nil {
				log.Printf("[core] restoring configuration failed: %v", err)
			}
		}
		if playing != nil {
			if _, err := c.request(playing, commandTimeout); err != nil {
				log.Printf("[core] restoring playback failed: %v", err)
			}
		}
	}
}

func (c *Client) setStatusError(msg string) {
	c.statusMu.Lock()
	c.status.Pipeline.Error = msg
	c.statusMu.Unlock()
}

func (c *Client) readLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 256*1024)
	var hdr [6]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return
		}
		n := binary.LittleEndian.Uint32(hdr[2:])
		if n > maxMessageSize {
			log.Printf("[core] message of %d bytes too large; dropping connection", n)
			return
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return
		}
		switch hdr[0] {
		case kindJPEG:
			c.preview.Broadcast(payload)
		case kindJSON:
			c.handleEvent(payload)
		}
	}
}

func (c *Client) handleEvent(b []byte) {
	var ev struct {
		Type string `json:"type"`
		ID   uint64 `json:"id"`
	}
	if err := json.Unmarshal(b, &ev); err != nil {
		log.Printf("[core] bad event: %v", err)
		return
	}
	switch ev.Type {
	case "status":
		var st Status
		if err := json.Unmarshal(b, &st); err != nil {
			log.Printf("[core] bad status: %v", err)
			return
		}
		if st.Metrics == nil {
			st.Metrics = []DisplayMetrics{}
		}
		c.statusMu.Lock()
		c.status = st
		c.statusMu.Unlock()
		if !st.Pipeline.IsRunning {
			c.preview.clear()
		}
	case "reply":
		var r reply
		if err := json.Unmarshal(b, &r); err != nil {
			log.Printf("[core] bad reply: %v", err)
			return
		}
		c.mu.Lock()
		ch := c.pending[ev.ID]
		delete(c.pending, ev.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}
}

// request sends a command and waits for its reply.
func (c *Client) request(cmd map[string]any, timeout time.Duration) (reply, error) {
	c.mu.Lock()
	if c.proc == nil {
		c.mu.Unlock()
		return reply{}, ErrNotRunning
	}
	c.nextID++
	id := c.nextID
	ch := make(chan reply, 1)
	c.pending[id] = ch
	msg := make(map[string]any, len(cmd)+1)
	for k, v := range cmd {
		msg[k] = v
	}
	msg["id"] = id
	line, err := json.Marshal(msg)
	if err == nil {
		_, err = c.proc.stdin.Write(append(line, '\n'))
	}
	if err != nil {
		delete(c.pending, id)
		c.mu.Unlock()
		return reply{}, fmt.Errorf("sending to booth-core: %w", err)
	}
	c.mu.Unlock()

	select {
	case r, ok := <-ch:
		if !ok {
			return reply{}, ErrNotRunning
		}
		if !r.OK {
			return r, errors.New(r.Error)
		}
		return r, nil
	case <-time.After(timeout):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return reply{}, fmt.Errorf("booth-core did not answer %q within %s", cmd["cmd"], timeout)
	}
}

// Configure sends the canvas, display and encoder settings.
func (c *Client) Configure(cfg *config.Config) error {
	snap := cfg.GetSnapshot()
	cmd := map[string]any{"cmd": "configure", "canvas": snap.Canvas, "displays": snap.Displays, "pipeline": snap.Pipeline}
	c.mu.Lock()
	c.configure = cmd
	c.mu.Unlock()
	_, err := c.request(cmd, commandTimeout)
	return err
}

// Play starts streaming mode ("testpattern", "logo", "rainbow", "video", "ndi") to all displays.
func (c *Client) Play(mode, target string) error {
	cmd := map[string]any{"cmd": "start", "mode": mode, "target": target}
	_, err := c.request(cmd, commandTimeout)
	c.mu.Lock()
	if err == nil {
		c.playing = cmd
	}
	c.mu.Unlock()
	return err
}

// Stop stops streaming.
func (c *Client) Stop() error {
	c.mu.Lock()
	c.playing = nil
	c.mu.Unlock()
	_, err := c.request(map[string]any{"cmd": "stop"}, commandTimeout)
	return err
}

// Switch changes the source with a transition ("cut", "fade", "black"),
// starting the stream if it is stopped.
func (c *Client) Switch(sourceType, target, transition string, durationMs int) error {
	if transition == "" {
		transition = "cut"
	}
	cmd := map[string]any{"cmd": "switch", "source_type": sourceType, "target": target, "transition": transition, "duration_ms": max(durationMs, 0)}
	_, err := c.request(cmd, commandTimeout)
	if err == nil {
		c.mu.Lock()
		// After a restart the source is restored with a cut.
		c.playing = map[string]any{"cmd": "start", "mode": sourceType, "target": target}
		c.mu.Unlock()
	}
	return err
}

// DiscoverNDISources lists the NDI senders on the network.
func (c *Client) DiscoverNDISources() ([]NDISource, error) {
	r, err := c.request(map[string]any{"cmd": "discover"}, discoverTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to discover NDI sources: %w", err)
	}
	if r.Sources == nil {
		return []NDISource{}, nil
	}
	return r.Sources, nil
}

// Status returns the latest status reported by the core.
func (c *Client) Status() Status {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	st := c.status
	st.Metrics = append([]DisplayMetrics(nil), c.status.Metrics...)
	if st.Metrics == nil {
		st.Metrics = []DisplayMetrics{}
	}
	return st
}

func (c *Client) Preview() *PreviewBroadcaster { return c.preview }

// Close stops streaming and ends the core process.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	p := c.proc
	c.mu.Unlock()
	if p == nil {
		return
	}
	// Closing stdin makes the core stop and exit; kill it if it hangs.
	_ = p.stdin.Close()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		log.Printf("[core] booth-core did not exit; killing it")
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

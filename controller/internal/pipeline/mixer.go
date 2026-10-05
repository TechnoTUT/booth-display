package pipeline

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"booth-display/controller/internal/config"
)

type TransitionType string

const (
	TransitionCut   TransitionType = "cut"
	TransitionFade  TransitionType = "fade"
	TransitionBlack TransitionType = "black"
)

type SceneStatus struct {
	ActiveSource string         `json:"active_source"`
	ActiveTarget string         `json:"active_target"` // Details like NDI name or video filename
	NextSource   string         `json:"next_source,omitempty"`
	NextTarget   string         `json:"next_target,omitempty"`
	Transition   TransitionType `json:"transition"`
	DurationMs   int            `json:"duration_ms"`
	InTransition bool           `json:"in_transition"`
	Progress     float64        `json:"progress"` // 0.0 to 1.0
}

// sharedFrame is a reference-counted frame buffer shared read-only between
// all subscribers and recycled through the mixer's pool.
type sharedFrame struct {
	data []byte
	refs atomic.Int32
	pool *sync.Pool
}

func (f *sharedFrame) retain() { f.refs.Add(1) }

func (f *sharedFrame) release() {
	if f.refs.Add(-1) == 0 {
		f.pool.Put(f)
	}
}

// getFrame returns a pooled frame owned by the caller (refcount 1).
func (m *CanvasMixer) getFrame() *sharedFrame {
	f := m.framePool.Get().(*sharedFrame)
	f.refs.Store(1)
	return f
}

type mixerSubscriber struct {
	ch chan *sharedFrame
	pw *io.PipeWriter
}

// CanvasMixer maintains a 30fps video frame feed for all displays and preview.
// It receives frames from multiple potential sources (TestPattern, NDI, Video)
// and handles transitions (cut, crossfade, dip to black) seamlessly without restarting encoders.
type CanvasMixer struct {
	canvas    config.CanvasConfig
	displays  []config.DisplayConfig
	frameSize int
	fps       int

	// Subscribers (Display pipelines & preview)
	subscribers map[*mixerSubscriber]bool
	subMu       sync.Mutex
	framePool   sync.Pool

	// Source generators & active processes
	testPattern *TestPatternGenerator
	logoGen     *LogoGenerator
	ndiDist     *NDIDistributor
	videoCmd    *exec.Cmd
	videoCancel context.CancelFunc

	// Frame buffers
	mu           sync.RWMutex
	currentFrame []byte
	prevFrame    []byte
	sourceFront  []byte // double-buffered front frame
	sourceBack   []byte // double-buffered back frame
	blackFrame   []byte
	sourceMu     sync.RWMutex

	// State
	activeSource   string
	activeTarget   string
	nextSource     string
	nextTarget     string
	transition     TransitionType
	duration       time.Duration
	transStartTime time.Time
	inTransition   bool

	running  bool
	cancelFn context.CancelFunc
	wg       sync.WaitGroup
}

func NewCanvasMixer(canvas config.CanvasConfig, displays []config.DisplayConfig) *CanvasMixer {
	w := canvas.Width
	if w <= 0 {
		w = 5792
	}
	h := canvas.Height
	if h <= 0 {
		h = 540
	}
	fps := canvas.FPS
	if fps <= 0 {
		fps = 30
	}

	frameSize := w * h * 4
	mixer := &CanvasMixer{
		canvas:       canvas,
		displays:     displays,
		frameSize:    frameSize,
		fps:          fps,
		subscribers:  make(map[*mixerSubscriber]bool),
		testPattern:  NewTestPatternGenerator(canvas, displays),
		logoGen:      NewLogoGenerator(canvas, displays),
		currentFrame: make([]byte, frameSize),
		prevFrame:    make([]byte, frameSize),
		sourceFront:  make([]byte, frameSize),
		sourceBack:   make([]byte, frameSize),
		blackFrame:   make([]byte, frameSize),
		activeSource: "testpattern",
		transition:   TransitionCut,
		duration:     500 * time.Millisecond,
	}

	mixer.framePool.New = func() any {
		return &sharedFrame{data: make([]byte, frameSize), pool: &mixer.framePool}
	}

	return mixer
}

func (m *CanvasMixer) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelFn = cancel
	m.running = true
	m.wg.Add(1)

	go m.mixerLoop(ctx)
	return nil
}

func (m *CanvasMixer) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	if m.cancelFn != nil {
		m.cancelFn()
	}
	m.stopExternalSourcesLocked()
	m.mu.Unlock()

	// Close all subscriber writers to immediately unblock any pending I/O
	m.subMu.Lock()
	for sub := range m.subscribers {
		_ = sub.pw.Close()
		close(sub.ch)
	}
	m.subscribers = make(map[*mixerSubscriber]bool)
	m.subMu.Unlock()

	m.wg.Wait()
}

func (m *CanvasMixer) Subscribe() io.ReadCloser {
	m.subMu.Lock()
	defer m.subMu.Unlock()

	pr, pw := io.Pipe()
	sub := &mixerSubscriber{
		ch: make(chan *sharedFrame, 2),
		pw: pw,
	}
	m.subscribers[sub] = true

	// Worker goroutine that pumps frames to the pipe
	go func() {
		defer pw.Close()
		for frame := range sub.ch {
			_, err := pw.Write(frame.data)
			frame.release()
			if err != nil {
				return
			}
		}
	}()

	return pr
}

func (m *CanvasMixer) GetStatus() SceneStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	progress := 0.0
	if m.inTransition && m.duration > 0 {
		elapsed := time.Since(m.transStartTime)
		progress = float64(elapsed) / float64(m.duration)
		if progress > 1.0 {
			progress = 1.0
		}
	}

	return SceneStatus{
		ActiveSource: m.activeSource,
		ActiveTarget: m.activeTarget,
		NextSource:   m.nextSource,
		NextTarget:   m.nextTarget,
		Transition:   m.transition,
		DurationMs:   int(m.duration / time.Millisecond),
		InTransition: m.inTransition,
		Progress:     progress,
	}
}

// SwitchSource triggers a transition to a new source.
func (m *CanvasMixer) SwitchSource(sourceType string, target string, trans TransitionType, durationMs int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if trans == "" {
		trans = TransitionCut
	}
	if durationMs <= 0 {
		durationMs = 500
	}

	// Capture current frame as previous frame for crossfade
	copy(m.prevFrame, m.currentFrame)

	// Start receiving new source if external
	if sourceType == "ndi" {
		if target == "" {
			return fmt.Errorf("no NDI source name specified")
		}
		if err := m.startNDILocked(target); err != nil {
			return err
		}
	} else if sourceType == "video" {
		if target == "" {
			return fmt.Errorf("no video file path specified")
		}
		if err := m.startVideoLocked(target); err != nil {
			return err
		}
	} else if sourceType == "rainbow" || (sourceType == "testpattern" && target == "rainbow") {
		sourceType = "rainbow"
		target = "testsrc"
		if err := m.startRainbowLocked(); err != nil {
			return err
		}
	} else if sourceType == "logo" {
		sourceType = "logo"
		target = "technotut"
		m.stopExternalSourcesLocked()
	} else {
		sourceType = "testpattern" // SMPTE Color Bars
		target = "bars"
		m.stopExternalSourcesLocked()
	}

	if trans == TransitionCut || durationMs < 50 {
		m.activeSource = sourceType
		m.activeTarget = target
		m.nextSource = ""
		m.nextTarget = ""
		m.inTransition = false
	} else {
		m.nextSource = sourceType
		m.nextTarget = target
		m.transition = trans
		m.duration = time.Duration(durationMs) * time.Millisecond
		m.transStartTime = time.Now()
		m.inTransition = true
	}

	return nil
}

func (m *CanvasMixer) startNDILocked(sourceName string) error {
	if m.ndiDist != nil && m.activeTarget == sourceName {
		return nil
	}

	m.stopExternalSourcesLocked()

	dist := NewNDIDistributor(sourceName, m.canvas)
	if err := dist.Start(); err != nil {
		return fmt.Errorf("failed to start NDI: %w", err)
	}
	m.ndiDist = dist

	return nil
}

func (m *CanvasMixer) startVideoLocked(filePath string) error {
	m.stopExternalSourcesLocked()

	ctx, cancel := context.WithCancel(context.Background())
	m.videoCancel = cancel

	fps := m.fps
	args := []string{
		"-re",
		"-stream_loop", "-1",
		"-i", filePath,
		"-f", "rawvideo",
		"-pix_fmt", "bgr0",
		"-s", fmt.Sprintf("%dx%d", m.canvas.Width, m.canvas.Height),
		"-r", fmt.Sprintf("%d", fps),
		"pipe:1",
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to open video stdout: %w", err)
	}
	expandPipeBuffer(stdout)

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start video ffmpeg: %w", err)
	}

	m.videoCmd = cmd

	go func() {
		bufReader := bufio.NewReaderSize(stdout, m.frameSize*2)
		for {
			_, err := io.ReadFull(bufReader, m.sourceBack)
			if err != nil {
				return
			}
			m.sourceMu.Lock()
			m.sourceFront, m.sourceBack = m.sourceBack, m.sourceFront
			m.sourceMu.Unlock()
		}
	}()

	return nil
}

func (m *CanvasMixer) startRainbowLocked() error {
	m.stopExternalSourcesLocked()

	ctx, cancel := context.WithCancel(context.Background())
	m.videoCancel = cancel

	fps := m.fps
	lavfiSource := fmt.Sprintf("testsrc=size=%dx%d:rate=%d", m.canvas.Width, m.canvas.Height, fps)
	args := []string{
		"-re",
		"-f", "lavfi",
		"-i", lavfiSource,
		"-f", "rawvideo",
		"-pix_fmt", "bgr0",
		"-s", fmt.Sprintf("%dx%d", m.canvas.Width, m.canvas.Height),
		"-r", fmt.Sprintf("%d", fps),
		"pipe:1",
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to open testsrc stdout: %w", err)
	}
	expandPipeBuffer(stdout)

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start testsrc ffmpeg: %w", err)
	}

	m.videoCmd = cmd

	go func() {
		bufReader := bufio.NewReaderSize(stdout, m.frameSize*2)
		for {
			_, err := io.ReadFull(bufReader, m.sourceBack)
			if err != nil {
				return
			}
			m.sourceMu.Lock()
			m.sourceFront, m.sourceBack = m.sourceBack, m.sourceFront
			m.sourceMu.Unlock()
		}
	}()

	return nil
}

func (m *CanvasMixer) stopExternalSourcesLocked() {
	if m.ndiDist != nil {
		m.ndiDist.Stop()
		m.ndiDist = nil
	}
	if m.videoCancel != nil {
		m.videoCancel()
		m.videoCancel = nil
	}
	if m.videoCmd != nil && m.videoCmd.Process != nil {
		_ = m.videoCmd.Process.Kill()
		_ = m.videoCmd.Wait()
		m.videoCmd = nil
	}
}

// mixerLoop outputs frames driven by NDI frame arrival (zero latency) or timer ticker.
func (m *CanvasMixer) mixerLoop(ctx context.Context) {
	defer m.wg.Done()

	interval := time.Second / time.Duration(m.fps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		// If active source is NDI and distributor is running, listen to incoming frames directly
		var ndiNotify <-chan struct{}
		m.mu.RLock()
		if m.ndiDist != nil && (m.activeSource == "ndi" || m.nextSource == "ndi") {
			ndiNotify = m.ndiDist.FrameNotifier()
		}
		m.mu.RUnlock()

		select {
		case <-ctx.Done():
			return
		case <-ndiNotify:
			// Frame arrived from NDI: render and broadcast immediately without waiting for ticker!
			f := m.getFrame()
			m.renderFrame(f.data)
			m.broadcastFrame(f)
			// Reset ticker to maintain proper timing fallback without double-pulsing
			ticker.Reset(interval)
		case <-ticker.C:
			// Fallback or generator mode (test pattern, video, logo)
			f := m.getFrame()
			m.renderFrame(f.data)
			m.broadcastFrame(f)
		}
	}
}

func (m *CanvasMixer) renderFrame(out []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var nextRaw []byte
	sourceToFetch := m.activeSource
	if m.inTransition {
		sourceToFetch = m.nextSource
	}

	if sourceToFetch == "ndi" {
		if m.ndiDist != nil && m.ndiDist.CopyLatestFrame(m.sourceFront) {
			nextRaw = m.sourceFront
		} else {
			nextRaw = m.blackFrame
		}
	} else if sourceToFetch == "video" || sourceToFetch == "rainbow" {
		m.sourceMu.RLock()
		nextRaw = m.sourceFront
		m.sourceMu.RUnlock()
	} else if sourceToFetch == "logo" {
		nextRaw = m.logoGen.NextFrame()
	} else {
		nextRaw = m.testPattern.NextFrame()
	}

	if !m.inTransition {
		copy(out, nextRaw)
		copy(m.currentFrame, out)
		return
	}

	// Calculate transition progress
	elapsed := time.Since(m.transStartTime)
	progress := float64(elapsed) / float64(m.duration)

	if progress >= 1.0 {
		// Transition finished
		m.activeSource = m.nextSource
		m.activeTarget = m.nextTarget
		m.nextSource = ""
		m.nextTarget = ""
		m.inTransition = false
		copy(out, nextRaw)
		copy(m.currentFrame, out)
		return
	}

	switch m.transition {
	case TransitionFade:
		// Linear crossfade: out = (1-p)*prev + p*next
		alpha := progress
		invAlpha := 1.0 - alpha
		for i := 0; i < m.frameSize; i++ {
			out[i] = byte(float64(m.prevFrame[i])*invAlpha + float64(nextRaw[i])*alpha)
		}
	case TransitionBlack:
		// Dip to black: 0.0 -> 0.5 fade out prev to black, 0.5 -> 1.0 fade in next from black
		if progress < 0.5 {
			factor := (0.5 - progress) * 2.0 // 1.0 down to 0.0
			for i := 0; i < m.frameSize; i++ {
				out[i] = byte(float64(m.prevFrame[i]) * factor)
			}
		} else {
			factor := (progress - 0.5) * 2.0 // 0.0 up to 1.0
			for i := 0; i < m.frameSize; i++ {
				out[i] = byte(float64(nextRaw[i]) * factor)
			}
		}
	default: // Cut
		copy(out, nextRaw)
		m.activeSource = m.nextSource
		m.activeTarget = m.nextTarget
		m.nextSource = ""
		m.nextTarget = ""
		m.inTransition = false
	}

	copy(m.currentFrame, out)
}

// broadcastFrame fans a single shared, ref-counted frame out to all subscribers
// (no per-subscriber copy). The mixer's own reference is released on return.
func (m *CanvasMixer) broadcastFrame(f *sharedFrame) {
	m.subMu.Lock()
	defer m.subMu.Unlock()

	for sub := range m.subscribers {
		f.retain()
		select {
		case sub.ch <- f:
		default:
			// Consumer queue full, drop frame to maintain realtime rate
			f.release()
		}
	}
	f.release()
}

package pipeline

import (
	"fmt"
	"sync"

	"booth-display/controller/internal/config"
	"booth-display/controller/internal/streamer"
)

type PipelineStatus struct {
	IsRunning  bool   `json:"is_running"`
	ActiveMode string `json:"active_mode"`
	VideoFile  string `json:"video_file"`
}

type PipelineEngine struct {
	cfgPool     *config.Config
	streamers   *streamer.StreamerPool
	pipelines   map[string]*DisplayPipeline
	previewHub  *PreviewBroadcaster
	previewPipe *PreviewPipeline
	mixer       *CanvasMixer

	isRunning  bool
	activeMode string
	videoFile  string
	ndiSource  string
	mu         sync.RWMutex
}

func NewPipelineEngine(cfg *config.Config, streamers *streamer.StreamerPool) *PipelineEngine {
	snap := cfg.GetSnapshot()
	hub := NewPreviewBroadcaster()
	engine := &PipelineEngine{
		cfgPool:     cfg,
		streamers:   streamers,
		pipelines:   make(map[string]*DisplayPipeline),
		previewHub:  hub,
		previewPipe: NewPreviewPipeline(hub),
		mixer:       NewCanvasMixer(snap.Canvas, snap.Displays),
		activeMode:  snap.Media.DefaultMode,
		videoFile:   snap.Media.VideoFile,
	}

	for _, d := range snap.Displays {
		sender, ok := streamers.GetSender(d.ID)
		if ok {
			engine.pipelines[d.ID] = NewDisplayPipeline(d, sender)
		}
	}

	return engine
}

// Start launches streaming for all configured displays through CanvasMixer.
func (e *PipelineEngine) Start(mode string, mediaTarget string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if mode == "" {
		mode = e.activeMode
	}
	if mode == "" {
		mode = "testpattern"
	}

	snap := e.cfgPool.GetSnapshot()

	// Ensure mixer is stopped first
	if e.mixer != nil {
		e.mixer.Stop()
	}

	// Create and start new mixer
	e.mixer = NewCanvasMixer(snap.Canvas, snap.Displays)
	if err := e.mixer.Start(); err != nil {
		return fmt.Errorf("failed to start canvas mixer: %w", err)
	}

	// Set initial source
	if mode == "ndi" {
		if mediaTarget == "" {
			mediaTarget = e.ndiSource
		}
		if mediaTarget == "" {
			return fmt.Errorf("no NDI source selected")
		}
		e.ndiSource = mediaTarget
	} else if mode == "video" {
		if mediaTarget == "" {
			mediaTarget = e.videoFile
		}
		e.videoFile = mediaTarget
	}

	_ = e.mixer.SwitchSource(mode, mediaTarget, TransitionCut, 0)

	// Launch DisplayPipelines fed by mixer.Subscribe()
	for id, pipe := range e.pipelines {
		inputReader := e.mixer.Subscribe()
		if err := pipe.Start("raw", "", snap.Canvas, inputReader); err != nil {
			return fmt.Errorf("failed to start pipeline for %s: %w", id, err)
		}
	}

	// Start canvas preview stream fed by mixer
	previewReader := e.mixer.Subscribe()
	_ = e.previewPipe.Start("raw", "", snap.Canvas, previewReader)

	e.isRunning = true
	e.activeMode = mode
	return nil
}

// SwitchScene changes source with a transition without restarting FFmpeg encoders.
func (e *PipelineEngine) SwitchScene(sourceType string, target string, trans TransitionType, durationMs int) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.isRunning {
		// If not running, start pipeline directly with target source
		e.mu.Unlock()
		err := e.Start(sourceType, target)
		e.mu.Lock()
		return err
	}

	if sourceType == "ndi" {
		e.ndiSource = target
	} else if sourceType == "video" {
		e.videoFile = target
	}
	e.activeMode = sourceType

	return e.mixer.SwitchSource(sourceType, target, trans, durationMs)
}

// GetSceneStatus returns detailed transition and source information.
func (e *PipelineEngine) GetSceneStatus() SceneStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.mixer == nil {
		return SceneStatus{
			ActiveSource: e.activeMode,
			Transition:   TransitionCut,
			DurationMs:   500,
		}
	}
	return e.mixer.GetStatus()
}

// Stop stops all active pipelines and mixer.
func (e *PipelineEngine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, pipe := range e.pipelines {
		pipe.Stop()
	}
	e.previewPipe.Stop()
	if e.mixer != nil {
		e.mixer.Stop()
	}
	e.isRunning = false
}

// GetPreviewBroadcaster returns the preview broadcaster hub.
func (e *PipelineEngine) GetPreviewBroadcaster() *PreviewBroadcaster {
	return e.previewHub
}

// GetStatus returns the current pipeline status.
func (e *PipelineEngine) GetStatus() PipelineStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return PipelineStatus{
		IsRunning:  e.isRunning,
		ActiveMode: e.activeMode,
		VideoFile:  e.videoFile,
	}
}

// RebuildPipelines stops existing pipelines, reloads displays from config, and restarts if running.
func (e *PipelineEngine) RebuildPipelines(displays []config.DisplayConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	wasRunning := e.isRunning
	for _, pipe := range e.pipelines {
		pipe.Stop()
	}
	e.pipelines = make(map[string]*DisplayPipeline)

	snap := e.cfgPool.GetSnapshot()
	for _, d := range displays {
		sender, ok := e.streamers.GetSender(d.ID)
		if ok {
			pipe := NewDisplayPipeline(d, sender)
			e.pipelines[d.ID] = pipe
			if wasRunning && e.mixer != nil {
				inputReader := e.mixer.Subscribe()
				if err := pipe.Start("raw", "", snap.Canvas, inputReader); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

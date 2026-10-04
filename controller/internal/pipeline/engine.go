package pipeline

import (
	"fmt"
	"io"
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
	ndiDist     *NDIDistributor

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

// Start launches streaming for all configured displays.
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

	if e.ndiDist != nil {
		e.ndiDist.Stop()
		e.ndiDist = nil
	}

	var ndiDist *NDIDistributor
	if mode == "ndi" {
		if mediaTarget == "" {
			mediaTarget = e.ndiSource
		}
		if mediaTarget == "" {
			return fmt.Errorf("no NDI source selected")
		}
		ndiDist = NewNDIDistributor(mediaTarget, snap.Canvas)
		if err := ndiDist.Start(); err != nil {
			return fmt.Errorf("failed to start NDI receiver: %w", err)
		}
		e.ndiDist = ndiDist
		e.ndiSource = mediaTarget
	} else if mode == "video" {
		if mediaTarget == "" {
			mediaTarget = e.videoFile
		}
		e.videoFile = mediaTarget
	}

	for id, pipe := range e.pipelines {
		var inputReader io.Reader
		if mode == "ndi" && ndiDist != nil {
			inputReader = ndiDist.Subscribe()
		}
		if err := pipe.Start(mode, mediaTarget, snap.Canvas, inputReader); err != nil {
			return fmt.Errorf("failed to start pipeline for %s: %w", id, err)
		}
	}

	// Start canvas preview stream
	var previewReader io.Reader
	if mode == "ndi" && ndiDist != nil {
		previewReader = ndiDist.Subscribe()
	}
	_ = e.previewPipe.Start(mode, mediaTarget, snap.Canvas, previewReader)

	e.isRunning = true
	e.activeMode = mode
	return nil
}

// Stop stops all active pipelines.
func (e *PipelineEngine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, pipe := range e.pipelines {
		pipe.Stop()
	}
	e.previewPipe.Stop()
	if e.ndiDist != nil {
		e.ndiDist.Stop()
		e.ndiDist = nil
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
			if wasRunning {
				var inputReader io.Reader
				target := e.videoFile
				if e.activeMode == "ndi" {
					target = e.ndiSource
					if e.ndiDist != nil {
						inputReader = e.ndiDist.Subscribe()
					}
				}
				if err := pipe.Start(e.activeMode, target, snap.Canvas, inputReader); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

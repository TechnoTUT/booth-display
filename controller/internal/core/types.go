// Package core runs booth-core, the Rust media core, as a child process and
// talks to it over its stdin/stdout control protocol (see core/src/main.rs).
package core

import (
	"encoding/json"
	"time"
)

// PipelineStatus is the state of the streaming pipeline.
type PipelineStatus struct {
	IsRunning  bool   `json:"is_running"`
	ActiveMode string `json:"active_mode"`
	VideoFile  string `json:"video_file"`
	// Error is set when the encoder or the NDI input stopped unexpectedly while running.
	Error string `json:"error,omitempty"`
}

// SceneStatus describes the active source and any transition in progress.
type SceneStatus struct {
	ActiveSource string  `json:"active_source"`
	ActiveTarget string  `json:"active_target"`
	NextSource   string  `json:"next_source,omitempty"`
	NextTarget   string  `json:"next_target,omitempty"`
	Transition   string  `json:"transition"`
	DurationMs   int     `json:"duration_ms"`
	InTransition bool    `json:"in_transition"`
	Progress     float64 `json:"progress"`
}

// DisplayMetrics are the per-display streaming statistics.
type DisplayMetrics struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Target      string    `json:"target"`
	IsConnected bool      `json:"is_connected"`
	FPS         float64   `json:"fps"`
	BitrateKbps float64   `json:"bitrate_kbps"`
	PacketsSent uint64    `json:"packets_sent"`
	BytesSent   uint64    `json:"bytes_sent"`
	FramesSent  uint64    `json:"frames_sent"`
	Drops       uint64    `json:"drops"`
	LastSentAt  time.Time `json:"last_sent_at"`
}

// UnmarshalJSON reads the core's last_sent_unix_ms into LastSentAt.
func (m *DisplayMetrics) UnmarshalJSON(b []byte) error {
	type plain DisplayMetrics
	var aux struct {
		plain
		LastSentUnixMs int64 `json:"last_sent_unix_ms"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*m = DisplayMetrics(aux.plain)
	if aux.LastSentUnixMs > 0 {
		m.LastSentAt = time.UnixMilli(aux.LastSentUnixMs)
	}
	return nil
}

// Status is the periodic status event of the core.
type Status struct {
	Pipeline PipelineStatus   `json:"pipeline"`
	Scene    SceneStatus      `json:"scene"`
	Metrics  []DisplayMetrics `json:"metrics"`
}

// NDISource is an NDI sender found on the network.
type NDISource struct {
	Name       string  `json:"name"`
	StreamName *string `json:"stream_name"`
	HostName   *string `json:"host_name"`
}

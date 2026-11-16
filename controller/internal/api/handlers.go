package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"booth-display/controller/internal/config"
	"booth-display/controller/internal/core"
)

type APIHandler struct {
	cfg  *config.Config
	core *core.Client
}

func NewAPIHandler(cfg *config.Config, c *core.Client) *APIHandler {
	return &APIHandler{cfg: cfg, core: c}
}

func (h *APIHandler) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snap := h.cfg.GetSnapshot()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&snap)
}

type UpdateConfigRequest struct {
	Displays []config.DisplayConfig `json:"displays"`
}

func (h *APIHandler) HandlePostConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req UpdateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Displays) > 0 {
		if err := h.cfg.UpdateDisplays(req.Displays); err != nil {
			http.Error(w, "Failed to save displays: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Rebuild the senders and, if streaming, the encoder in the core
		if err := h.core.Configure(h.cfg); err != nil {
			http.Error(w, "Saved, but failed to apply displays: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type PlaybackRequest struct {
	Action    string `json:"action"`     // "play", "stop"
	Mode      string `json:"mode"`       // "testpattern", "video", "ndi"
	VideoFile string `json:"video_file"`
	NDISource string `json:"ndi_source"`
}

func (h *APIHandler) HandlePlayback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PlaybackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	switch req.Action {
	case "play":
		target := req.VideoFile
		if req.Mode == "ndi" {
			target = req.NDISource
		}
		if err := h.core.Play(req.Mode, target); err != nil {
			http.Error(w, "Failed to start pipeline: "+err.Error(), http.StatusInternalServerError)
			return
		}
	case "stop":
		if err := h.core.Stop(); err != nil {
			http.Error(w, "Failed to stop pipeline: "+err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "Unknown action: "+req.Action, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "ok",
		"pipeline": h.core.Status().Pipeline,
	})
}

func (h *APIHandler) HandleNDISources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Discovery can run for up to 15s, longer than the server-wide WriteTimeout,
	// so extend the write deadline for this response only.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))

	sources, err := h.core.DiscoverNDISources()
	resp := map[string]interface{}{}
	if err != nil {
		// Return an empty list and the reason so the GUI can show it instead of failing silently
		log.Printf("[NDI] source discovery failed: %v", err)
		sources = []core.NDISource{}
		resp["error"] = err.Error()
	}
	resp["sources"] = sources

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *APIHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	st := h.core.Status()
	resp := map[string]interface{}{
		"pipeline": st.Pipeline,
		"metrics":  st.Metrics,
		"scene":    st.Scene,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *APIHandler) HandleSceneStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.core.Status().Scene)
}

type SceneSwitchRequest struct {
	SourceType string `json:"source_type"` // "testpattern", "ndi", "video"
	Target     string `json:"target"`      // NDI source name or video file path
	Transition string `json:"transition"`  // "cut", "fade", "black"
	DurationMs int    `json:"duration_ms"` // duration in milliseconds
}

func (h *APIHandler) HandleSceneSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SceneSwitchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.core.Switch(req.SourceType, req.Target, req.Transition, req.DurationMs); err != nil {
		http.Error(w, "Failed to switch scene: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"scene":  h.core.Status().Scene,
	})
}

type AssetFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (h *APIHandler) HandleListAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	assets := make([]AssetFile, 0)
	entries, err := os.ReadDir("assets")
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || entry.Name() == ".gitkeep" {
				continue
			}
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if ext == ".mp4" || ext == ".mov" || ext == ".mkv" || ext == ".webm" || ext == ".avi" {
				info, _ := entry.Info()
				var size int64
				if info != nil {
					size = info.Size()
				}
				assets = append(assets, AssetFile{
					Name: entry.Name(),
					Path: filepath.Join("assets", entry.Name()),
					Size: size,
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"assets": assets,
	})
}

func (h *APIHandler) HandlePreviewMJPEG(w http.ResponseWriter, r *http.Request) {
	broadcaster := h.core.Preview()
	sub := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(sub)

	rc := http.NewResponseController(w)
	// Clear the 15-second write timeout for long-lived streaming
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Connection", "keep-alive")

	flusher, hasFlusher := w.(http.Flusher)

	for {
		select {
		case <-r.Context().Done():
			return
		case frame, ok := <-sub:
			if !ok {
				return
			}
			header := fmt.Sprintf("--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
			if _, err := w.Write([]byte(header)); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := w.Write([]byte("\r\n")); err != nil {
				return
			}
			if hasFlusher {
				flusher.Flush()
			}
		}
	}
}

func (h *APIHandler) HandlePreviewSnapshot(w http.ResponseWriter, r *http.Request) {
	broadcaster := h.core.Preview()
	frame := broadcaster.GetLatest()

	if len(frame) == 0 {
		w.Header().Set("Content-Type", "image/svg+xml")
		svg := `<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="120" viewBox="0 0 1280 120">
			<rect width="100%" height="100%" fill="#0f172a"/>
			<text x="50%" y="50%" fill="#64748b" font-family="sans-serif" font-size="20" font-weight="bold" text-anchor="middle" dominant-baseline="middle">
				STREAM OFFLINE - PRESS START STREAM TO PREVIEW
			</text>
		</svg>`
		_, _ = w.Write([]byte(svg))
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(frame)
}

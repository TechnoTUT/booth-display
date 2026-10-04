package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"booth-display/controller/internal/config"
	"booth-display/controller/internal/pipeline"
	"booth-display/controller/internal/streamer"
	"booth-display/controller/web"
)

type Server struct {
	httpServer *http.Server
	wsManager  *WSManager
}

func NewServer(cfg *config.Config, engine *pipeline.PipelineEngine, streamers *streamer.StreamerPool) *Server {
	snap := cfg.GetSnapshot()
	mux := http.NewServeMux()

	handler := NewAPIHandler(cfg, engine, streamers)
	wsMgr := NewWSManager(handler)

	// API routes
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handler.HandleGetConfig(w, r)
		case http.MethodPost:
			handler.HandlePostConfig(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/status", handler.HandleStatus)
	mux.HandleFunc("/api/playback", handler.HandlePlayback)
	mux.HandleFunc("/api/scene/status", handler.HandleSceneStatus)
	mux.HandleFunc("/api/scene/switch", handler.HandleSceneSwitch)
	mux.HandleFunc("/api/assets", handler.HandleListAssets)
	mux.HandleFunc("/api/ndi/sources", handler.HandleNDISources)
	mux.HandleFunc("/api/preview/mjpeg", handler.HandlePreviewMJPEG)
	mux.HandleFunc("/api/preview/snapshot.jpg", handler.HandlePreviewSnapshot)
	mux.HandleFunc("/ws", wsMgr.HandleWS)

	// Static Web-GUI file server
	fileServer := http.FileServer(web.GetFileSystem())
	mux.Handle("/", fileServer)

	addr := fmt.Sprintf(":%d", snap.Server.HTTPPort)
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return &Server{
		httpServer: srv,
		wsManager:  wsMgr,
	}
}

func (s *Server) Start() error {
	log.Printf("Starting HTTP & Web-GUI server on %s", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server failed: %w", err)
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.wsManager.Close()
	return s.httpServer.Shutdown(ctx)
}

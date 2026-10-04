package controller_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"booth-display/controller/internal/api"
	"booth-display/controller/internal/config"
	"booth-display/controller/internal/pipeline"
	"booth-display/controller/internal/protocol"
	"booth-display/controller/internal/streamer"
)

func TestControllerEndToEndUDPStreaming(t *testing.T) {
	// 1. Setup UDP Receiver on 127.0.0.1:18554
	recvAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:18554")
	if err != nil {
		t.Fatalf("failed to resolve UDP receiver addr: %v", err)
	}
	conn, err := net.ListenUDP("udp", recvAddr)
	if err != nil {
		t.Fatalf("failed to listen UDP: %v", err)
	}
	defer conn.Close()

	// 2. Setup mock config
	cfg := &config.Config{
		Server: config.ServerConfig{
			HTTPPort: 18080,
		},
		Canvas: config.CanvasConfig{
			Width:  1920,
			Height: 540,
			FPS:    30,
		},
		Displays: []config.DisplayConfig{
			{
				ID:     "test-disp",
				Name:   "Test Display",
				IP:     "127.0.0.1",
				Port:   18554,
				Width:  1920,
				Height: 540,
				CropX:  0,
				CropY:  0,
			},
		},
		Media: config.MediaConfig{
			DefaultMode: "testpattern",
		},
	}

	// 3. Init streamer pool & pipeline engine
	streamers, err := streamer.NewStreamerPool(cfg.Displays)
	if err != nil {
		t.Fatalf("failed to init streamers: %v", err)
	}
	defer streamers.Close()

	engine := pipeline.NewPipelineEngine(cfg, streamers)
	defer engine.Stop()

	// 4. Start HTTP Server
	srv := api.NewServer(cfg, engine, streamers)
	go func() {
		_ = srv.Start()
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	// Allow server to bind
	time.Sleep(100 * time.Millisecond)

	// Verify HTTP GET /api/config
	resp, err := http.Get("http://127.0.0.1:18080/api/config")
	if err != nil {
		t.Fatalf("GET /api/config failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 5. Start streaming
	if err := engine.Start("testpattern", ""); err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}

	// 6. Listen for UDP packets from the pipeline
	buf := make([]byte, 2048)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	packetsReceived := 0
	for packetsReceived < 5 {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			t.Fatalf("failed to receive UDP packet: %v", err)
		}

		hdr, payload, err := protocol.ParsePacket(buf[:n])
		if err != nil {
			t.Fatalf("failed to parse received packet: %v", err)
		}

		if hdr.Magic != protocol.MagicByte {
			t.Errorf("magic byte mismatch: got 0x%02X", hdr.Magic)
		}
		if hdr.PayloadType != protocol.PayloadTypeH264 {
			t.Errorf("expected H.264 payload, got %d", hdr.PayloadType)
		}
		if len(payload) == 0 {
			t.Errorf("expected non-empty payload")
		}

		packetsReceived++
	}

	metrics := streamers.GetAllMetrics()
	if len(metrics) == 0 {
		t.Fatalf("expected at least 1 metric entry")
	}
	if metrics[0].PacketsSent == 0 {
		t.Errorf("expected packets_sent > 0, got %d", metrics[0].PacketsSent)
	}

	t.Logf("E2E Test Passed! Received %d verified H.264 UDP packets. Metrics: %+v", packetsReceived, metrics[0])
}

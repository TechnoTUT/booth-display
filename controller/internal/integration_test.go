package controller_test

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"booth-display/controller/internal/api"
	"booth-display/controller/internal/config"
	"booth-display/controller/internal/core"
)

// coreBinary finds a built booth-core: BOOTH_CORE_BIN, or the cargo output in core/target.
func coreBinary(t *testing.T) string {
	if p := os.Getenv("BOOTH_CORE_BIN"); p != "" {
		return p
	}
	for _, p := range []string{"../../core/target/release/booth-core", "../../core/target/debug/booth-core"} {
		if abs, err := filepath.Abs(p); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	t.Skip("booth-core not built (run \"make build-core\" or set BOOTH_CORE_BIN)")
	return ""
}

// TestControllerEndToEndUDPStreaming runs the controller with the real media
// core and checks that H.264 packets reach a display over UDP.
func TestControllerEndToEndUDPStreaming(t *testing.T) {
	bin := coreBinary(t)

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 18554})
	if err != nil {
		t.Fatalf("failed to listen UDP: %v", err)
	}
	defer conn.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{HTTPPort: 18080},
		Canvas: config.CanvasConfig{Width: 1920, Height: 540, FPS: 30},
		Displays: []config.DisplayConfig{
			{ID: "test-disp", Name: "Test Display", IP: "127.0.0.1", Port: 18554, Width: 1920, Height: 540},
		},
		Media:    config.MediaConfig{DefaultMode: "testpattern"},
		Pipeline: config.PipelineConfig{Encoder: "software"},
	}

	c := core.New(bin)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Configure(cfg); err != nil {
		t.Fatalf("configure: %v", err)
	}

	srv := api.NewServer(cfg, c)
	go func() { _ = srv.Start() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:18080/api/config")
	if err != nil {
		t.Fatalf("GET /api/config failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	if err := c.Play("testpattern", ""); err != nil {
		t.Fatalf("failed to start streaming: %v", err)
	}

	buf := make([]byte, 2048)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for received := 0; received < 5; received++ {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			t.Fatalf("failed to receive UDP packet: %v", err)
		}
		// 16-byte header: magic 0xBD, version 1, payload type 1 (H.264), flags, seq, ts, frag idx/total
		if n <= 16 || buf[0] != 0xBD || buf[1] != 0x01 || buf[2] != 0x01 {
			t.Fatalf("unexpected packet header % x", buf[:min(n, 16)])
		}
		if idx, total := binary.BigEndian.Uint16(buf[12:]), binary.BigEndian.Uint16(buf[14:]); idx >= total {
			t.Fatalf("fragment %d of %d", idx, total)
		}
	}

	// Metrics arrive with the next status event.
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := c.Status()
		if st.Pipeline.IsRunning && len(st.Metrics) == 1 && st.Metrics[0].PacketsSent > 0 {
			if st.Metrics[0].LastSentAt.IsZero() {
				t.Errorf("last_sent_at not set")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no metrics reported: %+v", st)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The preview reaches the controller as JPEG.
	deadline = time.Now().Add(3 * time.Second)
	for {
		if f := c.Preview().GetLatest(); len(f) > 2 && f[0] == 0xFF && f[1] == 0xD8 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no preview JPEG received")
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := c.Switch("logo", "", "fade", 200); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if c.Status().Pipeline.IsRunning {
		t.Error("still running after stop")
	}
}

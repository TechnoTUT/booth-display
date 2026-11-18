package controller_test

import (
	"context"
	"encoding/binary"
	"fmt"
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
// core and checks that H.264 packets reach a display over UDP, with the
// software encoder and, when a render node exists, the VA-API one.
func TestControllerEndToEndUDPStreaming(t *testing.T) {
	bin := coreBinary(t)
	encoders := []string{"libx264"}
	if _, err := os.Stat("/dev/dri/renderD128"); err == nil {
		encoders = append(encoders, "vaapi")
	}
	for i, enc := range encoders {
		t.Run(enc, func(t *testing.T) { runEndToEnd(t, bin, enc, i) })
	}
}

func runEndToEnd(t *testing.T, bin, encoder string, n int) {
	udpPort, httpPort := 18554+n, 18080+n
	cfgEncoder := encoder
	if encoder == "libx264" {
		cfgEncoder = "software"
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: udpPort})
	if err != nil {
		t.Fatalf("failed to listen UDP: %v", err)
	}
	defer conn.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{HTTPPort: httpPort},
		Canvas: config.CanvasConfig{Width: 1920, Height: 540, FPS: 30},
		Displays: []config.DisplayConfig{
			{ID: "test-disp", Name: "Test Display", IP: "127.0.0.1", Port: udpPort, Width: 1920, Height: 540},
		},
		Media:    config.MediaConfig{DefaultMode: "testpattern"},
		Pipeline: config.PipelineConfig{Encoder: cfgEncoder},
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

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/config", httpPort))
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
		// 16-byte header: magic 0xBD, version 2, payload type 1 (H.264), flags, seq, ts, frag idx/total
		if n <= 16 || buf[0] != 0xBD || buf[1] != 0x02 || buf[2] != 0x01 {
			t.Fatalf("unexpected packet header % x", buf[:min(n, 16)])
		}
		if idx, total := binary.BigEndian.Uint16(buf[12:]), binary.BigEndian.Uint16(buf[14:]); idx >= total {
			t.Fatalf("fragment %d of %d", idx, total)
		}
	}

	checkControlMessages(t, conn)

	// Metrics arrive with the next status event.
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := c.Status()
		// Wait for a status that includes the keyframe request sent above.
		if st.Pipeline.IsRunning && len(st.Metrics) == 1 && st.Metrics[0].KeyframeRequests > 0 {
			if st.Pipeline.Encoder != encoder {
				t.Fatalf("encoder = %q, want %q (fell back?)", st.Pipeline.Encoder, encoder)
			}
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

// checkControlMessages acts as a display: in the middle of a GOP it asks for a
// keyframe, which must arrive well before the next regular IDR (GOP 1 s), and
// it sends a PING, which must come back as a PONG echoing its timestamp.
func checkControlMessages(t *testing.T, conn *net.UDPConn) {
	t.Helper()
	const (
		payloadH264    = 0x01
		payloadRequest = 0x10
		payloadPing    = 0x11
		payloadPong    = 0x12
		flagKeyframe   = 0x02
	)
	buf := make([]byte, 2048)
	var controller net.Addr
	// next returns the payload type, the keyframe flag of a frame's first
	// fragment, and the packet.
	next := func() (byte, bool, []byte) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			t.Fatalf("failed to receive UDP packet: %v", err)
		}
		if n < 16 || buf[0] != 0xBD || buf[1] != 0x02 {
			t.Fatalf("unexpected packet header % x", buf[:min(n, 16)])
		}
		controller = from
		first := binary.BigEndian.Uint16(buf[12:]) == 0
		return buf[2], buf[2] == payloadH264 && first && buf[3]&flagKeyframe != 0, buf[:n]
	}
	send := func(payloadType byte, ts uint32) {
		pkt := make([]byte, 16)
		pkt[0], pkt[1], pkt[2] = 0xBD, 0x02, payloadType
		binary.BigEndian.PutUint32(pkt[8:], ts)
		binary.BigEndian.PutUint16(pkt[14:], 1)
		if _, err := conn.WriteTo(pkt, controller); err != nil {
			t.Fatalf("send control message: %v", err)
		}
	}

	// Find a regular IDR, then move 300 ms into its GOP.
	for _, key, _ := next(); !key; _, key, _ = next() {
	}
	for idr := time.Now(); time.Since(idr) < 300*time.Millisecond; {
		next()
	}
	asked := time.Now()
	send(payloadRequest, 0)
	for _, key, _ := next(); !key; _, key, _ = next() {
	}
	if d := time.Since(asked); d > 400*time.Millisecond {
		t.Errorf("keyframe came %v after the request, want it within 400 ms", d)
	}

	send(payloadPing, 0xCAFE)
	for {
		typ, _, pkt := next()
		if typ != payloadPong {
			continue
		}
		if len(pkt) != 20 || binary.BigEndian.Uint32(pkt[16:]) != 0xCAFE {
			t.Errorf("PONG % x does not echo the PING timestamp", pkt)
		}
		return
	}
}

package streamer

import (
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"booth-display/controller/internal/config"
	"booth-display/controller/internal/protocol"
)

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

type DisplaySender struct {
	cfg        config.DisplayConfig
	conn       *net.UDPConn
	targetAddr *net.UDPAddr
	queue      chan []byte
	stopCh     chan struct{}
	wg         sync.WaitGroup

	packetsSent uint64
	bytesSent   uint64
	framesSent  uint64
	drops       uint64

	fpsCounter    uint64
	byteCounter   uint64
	currentFPS    float64
	currentKbps   float64
	lastSentAt    time.Time
	mu            sync.RWMutex
	seqNumber     uint32
}

func NewDisplaySender(cfg config.DisplayConfig) (*DisplaySender, error) {
	raddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", cfg.IP, cfg.Port))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve UDP target %s:%d: %w", cfg.IP, cfg.Port, err)
	}

	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial UDP to %s: %w", raddr, err)
	}

	s := &DisplaySender{
		cfg:        cfg,
		conn:       conn,
		targetAddr: raddr,
		queue:      make(chan []byte, 1024),
		stopCh:     make(chan struct{}),
	}

	s.wg.Add(2)
	go s.sendLoop()
	go s.metricsLoop()

	return s, nil
}

func (s *DisplaySender) sendLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopCh:
			return
		case pkt, ok := <-s.queue:
			if !ok {
				return
			}
			n, err := s.conn.Write(pkt)
			if err == nil {
				atomic.AddUint64(&s.packetsSent, 1)
				atomic.AddUint64(&s.bytesSent, uint64(n))
				atomic.AddUint64(&s.byteCounter, uint64(n))
				s.mu.Lock()
				s.lastSentAt = time.Now()
				s.mu.Unlock()
			}
		}
	}
}

func (s *DisplaySender) metricsLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			fps := atomic.SwapUint64(&s.fpsCounter, 0)
			bytes := atomic.SwapUint64(&s.byteCounter, 0)

			s.mu.Lock()
			s.currentFPS = float64(fps)
			s.currentKbps = float64(bytes*8) / 1000.0
			s.mu.Unlock()
		}
	}
}

// SendFrame splits frame into UDP packets and queues them.
func (s *DisplaySender) SendFrame(data []byte, payloadType byte, isKeyframe bool, timestampMs uint32) {
	packets := protocol.Packetize(data, payloadType, isKeyframe, &s.seqNumber, timestampMs, protocol.DefaultMaxPayloadSize)
	atomic.AddUint64(&s.framesSent, 1)
	atomic.AddUint64(&s.fpsCounter, 1)

	for _, pkt := range packets {
		select {
		case s.queue <- pkt:
		default:
			// Queue full - drop packet to avoid latency buildup
			atomic.AddUint64(&s.drops, 1)
		}
	}
}

func (s *DisplaySender) GetMetrics() DisplayMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return DisplayMetrics{
		ID:          s.cfg.ID,
		Name:        s.cfg.Name,
		Target:      s.targetAddr.String(),
		IsConnected: true,
		FPS:         s.currentFPS,
		BitrateKbps: s.currentKbps,
		PacketsSent: atomic.LoadUint64(&s.packetsSent),
		BytesSent:   atomic.LoadUint64(&s.bytesSent),
		FramesSent:  atomic.LoadUint64(&s.framesSent),
		Drops:       atomic.LoadUint64(&s.drops),
		LastSentAt:  s.lastSentAt,
	}
}

func (s *DisplaySender) Close() {
	close(s.stopCh)
	s.conn.Close()
	s.wg.Wait()
}

// StreamerPool manages multiple DisplaySenders.
type StreamerPool struct {
	mu      sync.RWMutex
	senders map[string]*DisplaySender
}

func NewStreamerPool(displays []config.DisplayConfig) (*StreamerPool, error) {
	pool := &StreamerPool{
		senders: make(map[string]*DisplaySender),
	}

	for _, d := range displays {
		sender, err := NewDisplaySender(d)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("failed to init sender for display %s: %w", d.ID, err)
		}
		pool.senders[d.ID] = sender
	}

	return pool, nil
}

func (p *StreamerPool) GetSender(displayID string) (*DisplaySender, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.senders[displayID]
	return s, ok
}

func (p *StreamerPool) GetAllMetrics() []DisplayMetrics {
	p.mu.RLock()
	defer p.mu.RUnlock()

	metrics := make([]DisplayMetrics, 0, len(p.senders))
	for _, s := range p.senders {
		metrics = append(metrics, s.GetMetrics())
	}

	sort.Slice(metrics, func(i, j int) bool {
		return metrics[i].ID < metrics[j].ID
	})

	return metrics
}

func (p *StreamerPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, s := range p.senders {
		s.Close()
	}
	p.senders = make(map[string]*DisplaySender)
}

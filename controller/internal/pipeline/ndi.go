package pipeline

import (
	"fmt"
	"log"
	"sync"
	"time"

	"booth-display/controller/internal/config"
	"booth-display/controller/internal/ndi"
)

type NDISource struct {
	Name       string  `json:"name"`
	StreamName *string `json:"stream_name"`
	HostName   *string `json:"host_name"`
}

const (
	// ndiDiscoverTimeout bounds how long discovery waits for NDI senders to settle.
	ndiDiscoverTimeout = 4 * time.Second
	ndiDiscoverSettle  = time.Second
	// ndiFindTimeout is how long Start keeps looking for the selected source.
	ndiFindTimeout = 5 * time.Second
	// ndiCaptureTimeout bounds how long the receive loop blocks, and so how
	// quickly Stop takes effect.
	ndiCaptureTimeout = 100 * time.Millisecond
)

// DiscoverNDISources finds active NDI senders on the network.
func DiscoverNDISources() ([]NDISource, error) {
	list, err := ndi.Discover(ndiDiscoverTimeout, ndiDiscoverSettle)
	if err != nil {
		return nil, fmt.Errorf("failed to discover NDI sources: %w", err)
	}
	sources := make([]NDISource, 0, len(list))
	for _, s := range list {
		host, stream := ndi.HostName(s.Name), ndi.StreamName(s.Name)
		src := NDISource{Name: s.Name, HostName: &host}
		if stream != "" {
			src.StreamName = &stream
		}
		sources = append(sources, src)
	}
	return sources, nil
}

// NDIDistributor receives an NDI source through libndi, scales each frame onto
// the canvas (center crop, BGRX) and keeps the latest one double-buffered for
// the mixer.
//
// Frames are forwarded the moment they arrive: there is no polling clock, and
// when frames queue up only the newest is used. A source faster than the canvas
// rate (e.g. 60 fps) is thinned to roughly the canvas rate so frames that the
// mixer would drop anyway are not scaled.
type NDIDistributor struct {
	sourceName string
	width      int
	height     int
	fps        int

	stopCh  chan struct{}
	wg      sync.WaitGroup
	running bool
	mu      sync.Mutex

	// Double-buffering for tear-free, non-blocking frame retrieval
	frontBuf []byte
	backBuf  []byte
	hasFrame bool
	bufMu    sync.RWMutex

	frameNotify chan struct{}

	// exitErr is set when receiving stops without Stop being called.
	exitErr error
	errMu   sync.Mutex
}

func NewNDIDistributor(sourceName string, canvas config.CanvasConfig) *NDIDistributor {
	w := canvas.Width
	if w <= 0 {
		w = config.DefaultCanvasWidth
	}
	h := canvas.Height
	if h <= 0 {
		h = 540
	}
	fps := canvas.FPS
	if fps <= 0 {
		fps = 30
	}
	frameBytes := w * h * 4

	return &NDIDistributor{
		sourceName:  sourceName,
		width:       w,
		height:      h,
		fps:         fps,
		frontBuf:    make([]byte, frameBytes),
		backBuf:     make([]byte, frameBytes),
		frameNotify: make(chan struct{}, 1),
	}
}

// FrameNotifier returns a channel that signals whenever a new frame arrives from NDI.
func (d *NDIDistributor) FrameNotifier() <-chan struct{} {
	return d.frameNotify
}

// Start loads libndi and begins receiving in the background. It returns quickly:
// finding the source and connecting happen in the receive goroutine, and a
// failure there is reported through Err.
func (d *NDIDistributor) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.running {
		return nil
	}
	if err := ndi.Load(); err != nil {
		return err
	}
	d.stopCh = make(chan struct{})
	d.running = true
	d.wg.Add(1)
	go d.receiveLoop(d.stopCh)
	return nil
}

func (d *NDIDistributor) fail(err error) {
	d.errMu.Lock()
	d.exitErr = fmt.Errorf("NDI input stopped: %w", err)
	d.errMu.Unlock()
	log.Printf("[NDIDistributor] %v", d.Err())
}

func (d *NDIDistributor) receiveLoop(stop <-chan struct{}) {
	defer d.wg.Done()

	log.Printf("[NDIDistributor] Searching for NDI source %q (libndi %s)...", d.sourceName, ndi.Version())
	src, err := ndi.FindSource(d.sourceName, ndiFindTimeout, stop)
	if err != nil {
		select {
		case <-stop:
		default:
			d.fail(err)
		}
		return
	}
	recv, err := ndi.NewReceiver(src, "booth-display")
	if err != nil {
		d.fail(err)
		return
	}
	defer recv.Close()
	log.Printf("[NDIDistributor] Connected to NDI source: %s (%s)", src.Name, src.URL)

	// Accept a frame once at least 60% of the canvas interval has passed since
	// the last one: a 30 fps source passes every frame despite jitter, a 60 fps
	// source passes every other one.
	minGap := time.Second * 6 / time.Duration(d.fps*10)
	var (
		scaler    *bgrxScaler
		lastOut   time.Time
		dropped   int
		lastErr   string
		statsTime = time.Now()
		frames    int
	)
	for {
		select {
		case <-stop:
			return
		default:
		}

		f, ok, err := recv.Capture(ndiCaptureTimeout)
		if err != nil {
			if msg := err.Error(); msg != lastErr {
				log.Printf("[NDIDistributor] %v", err)
				lastErr = msg
			}
			continue
		}
		if !ok {
			continue
		}
		lastErr = ""
		dropped += f.Dropped
		now := time.Now()
		if now.Sub(lastOut) < minGap {
			dropped++
			continue
		}
		lastOut = now

		if scaler == nil || scaler.srcW != f.Width || scaler.srcH != f.Height {
			scaler = newBGRXScaler(f.Width, f.Height, d.width, d.height, true)
			log.Printf("[NDIDistributor] Source resolution %dx%d -> canvas %dx%d (crop %dx%d at %d,%d)",
				f.Width, f.Height, d.width, d.height, scaler.cropW, scaler.cropH, scaler.cropX, scaler.cropY)
		}
		scaler.Scale(d.backBuf, f.Data, f.Stride)

		d.bufMu.Lock()
		d.frontBuf, d.backBuf = d.backBuf, d.frontBuf
		d.hasFrame = true
		d.bufMu.Unlock()

		select {
		case d.frameNotify <- struct{}{}:
		default:
		}

		frames++
		if since := now.Sub(statsTime); since >= time.Minute {
			log.Printf("[NDIDistributor] %.1f fps forwarded, %d frames skipped in the last %.0fs",
				float64(frames)/since.Seconds(), dropped, since.Seconds())
			frames, dropped, statsTime = 0, 0, now
		}
	}
}

// Err returns the reason NDI input stopped unexpectedly, or nil while it is healthy.
func (d *NDIDistributor) Err() error {
	d.errMu.Lock()
	defer d.errMu.Unlock()
	return d.exitErr
}

// CopyLatestFrame copies the latest received NDI frame into dst.
// Returns true if a valid frame was copied, false if no frame has arrived yet.
func (d *NDIDistributor) CopyLatestFrame(dst []byte) bool {
	d.bufMu.RLock()
	defer d.bufMu.RUnlock()

	if !d.hasFrame {
		return false
	}
	copy(dst, d.frontBuf)
	return true
}

// Stop ends receiving and waits for the receive goroutine, at most about one
// capture timeout.
func (d *NDIDistributor) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.running {
		return
	}
	close(d.stopCh)
	d.wg.Wait()
	d.running = false
}

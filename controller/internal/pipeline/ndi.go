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
	// ndiFindTimeout is how long the source may be missing before it is
	// reported as an error; it is still looked for afterwards.
	ndiFindTimeout = 5 * time.Second
	// ndiCaptureTimeout bounds how long one wait for a frame blocks, and so how
	// quickly Stop takes effect and how often a silent source is checked.
	ndiCaptureTimeout = 20 * time.Millisecond
	// ndiLookupInterval is how often a missing source is looked up again.
	ndiLookupInterval = 100 * time.Millisecond
	// ndiStallTimeout: a source that delivers no new frame for this long gets a
	// fresh receiver (see receiveLoop). It doubles up to ndiMaxStallInterval
	// while the source stays silent.
	ndiStallTimeout     = 2 * time.Second
	ndiMaxStallInterval = 30 * time.Second
	// ndiLiveWindow: a receiver that delivered a frame this recently is healthy
	// and can be disconnected and reused when NDI input stops.
	ndiLiveWindow = 500 * time.Millisecond
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
// It waits inside the SDK for each frame and forwards it the moment it
// arrives; when frames have queued up, only the newest is used. Frames arrive
// as UYVY and only the cropped part is converted. A source
// faster than the canvas rate (e.g. 60 fps) is thinned to roughly the canvas
// rate so frames that the mixer would drop anyway are not scaled.
//
// The receiver is taken from and returned to a pool (ndi.AcquireReceiver /
// ndi.Release), so switching sources does not create and destroy receivers.
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
	return &NDIDistributor{
		sourceName:  sourceName,
		width:       w,
		height:      h,
		fps:         fps,
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
	n := d.width * d.height * 4
	d.bufMu.Lock()
	d.frontBuf, d.backBuf, d.hasFrame = getCanvasBuf(n), getCanvasBuf(n), false
	d.bufMu.Unlock()
	d.stopCh = make(chan struct{})
	d.running = true
	d.wg.Add(1)
	go d.receiveLoop(d.stopCh)
	return nil
}

func (d *NDIDistributor) fail(err error) {
	d.errMu.Lock()
	d.exitErr = fmt.Errorf("NDI input: %w", err)
	d.errMu.Unlock()
	log.Printf("[NDIDistributor] %v", d.Err())
}

func (d *NDIDistributor) clearErr() {
	d.errMu.Lock()
	d.exitErr = nil
	d.errMu.Unlock()
}

// receiveLoop finds the source, connects and forwards frames until stop is
// closed. It recovers on its own, following moip-toolkit's receiver handling:
//
//   - Source not found: it keeps looking (the error is reported after
//     ndiFindTimeout and cleared once frames arrive).
//   - No new frame for the stall interval although NDI reports a connection:
//     after a network outage a dead session can look connected for minutes,
//     and tearing it down blocks inside the SDK for ~15 s. So the receiver is
//     not disconnected; it is discarded (destroyed in the background) and a
//     fresh one connects, which takes well under a second. The last frame stays
//     on the canvas meanwhile.
//   - No connection: NDI retries by itself; if the sender reappears under
//     another address, a fresh receiver connects to it.
func (d *NDIDistributor) receiveLoop(stop <-chan struct{}) {
	defer d.wg.Done()

	log.Printf("[NDIDistributor] Looking for NDI source %q (libndi %s)...", d.sourceName, ndi.Version())
	recv, err := ndi.AcquireReceiver("booth-display")
	if err != nil {
		d.fail(err)
		return
	}
	var (
		src       ndi.Source
		attached  bool
		started   = time.Now()
		lastFrame time.Time // last new frame, or when the current connection started
		nextCheck time.Time // next lookup (unattached) or address check (unconnected)
		stallIvl  = ndiStallTimeout
		reported  bool // "not found" error reported
		gone      bool // the connected source dropped off the source list
	)
	defer func() {
		if attached && time.Since(lastFrame) > ndiLiveWindow {
			ndi.Discard(recv)
		} else {
			ndi.Release(recv)
		}
	}()

	// replace swaps in a fresh receiver connected to s; the old one is torn
	// down in the background.
	replace := func(s ndi.Source) bool {
		fresh, err := ndi.NewReceiver("booth-display")
		if err != nil {
			log.Printf("[NDIDistributor] %v", err)
			return false
		}
		ndi.Discard(recv)
		recv, src = fresh, s
		recv.Connect(s)
		lastFrame = time.Now()
		return true
	}

	// Accept a frame once at least 60% of the canvas interval has passed since
	// the last one: a 30 fps source passes every frame despite jitter, a 60 fps
	// source passes every other one.
	minGap := time.Second * 6 / time.Duration(d.fps*10)
	var (
		scaler    *bgrxScaler
		scalerFmt uint32
		lastOut   time.Time
		skipped   int
		statsTime = time.Now()
		frames    int
		connected bool // a frame arrived on the current connection
	)
	var lastErr error
	for {
		select {
		case <-stop:
			return
		default:
		}

		if !attached {
			if wait := time.Until(nextCheck); wait > 0 {
				select {
				case <-stop:
					return
				case <-time.After(wait):
				}
			}
			now := time.Now()
			nextCheck = now.Add(ndiLookupInterval)
			s, ok, err := ndi.LookupSource(d.sourceName)
			if err != nil {
				d.fail(err)
				return
			}
			if !ok {
				if !reported && now.Sub(started) >= ndiFindTimeout {
					d.fail(fmt.Errorf("NDI source %q not found on the network; still looking", d.sourceName))
					reported = true
				}
				continue
			}
			src, attached, lastFrame = s, true, now
			recv.Connect(s)
			log.Printf("[NDIDistributor] Connecting to NDI source: %s (%s)", s.Name, s.URL)
			continue
		}

		f, dropped, ok, err := recv.Capture(ndiCaptureTimeout)
		now := time.Now()
		if err != nil && err != lastErr {
			log.Printf("[NDIDistributor] %v", err)
		}
		lastErr = err
		if !ok {
			silent := now.Sub(lastFrame)
			if silent < ndiLiveWindow || now.Before(nextCheck) {
				continue
			}
			nextCheck = now.Add(2 * ndiLookupInterval)
			if recv.Connections() > 0 {
				if silent < stallIvl {
					continue
				}
				// Connected but silent: replace the receiver (see above).
				nextCheck = now.Add(time.Second)
				s, found, _ := ndi.LookupSource(d.sourceName)
				if !found {
					s = src
				}
				if replace(s) {
					log.Printf("[NDIDistributor] No new frames for %.1fs on a connected receiver; reconnecting to %s (%s)",
						silent.Seconds(), s.Name, s.URL)
					stallIvl = min(stallIvl*2, ndiMaxStallInterval)
					connected = false
				}
				continue
			}
			// Not connected. NDI retries by itself, but backs off to ~10 s
			// between attempts. Connect afresh as soon as the sender is back:
			// listed again after it had dropped off the list, or listed
			// under another address.
			s, found, _ := ndi.LookupSource(d.sourceName)
			if !found {
				gone = true
				continue
			}
			if gone || s.URL != src.URL {
				gone = false
				if replace(s) {
					log.Printf("[NDIDistributor] NDI source is back; connecting to %s (%s)", s.Name, s.URL)
					connected = false
				}
			}
			continue
		}

		lastFrame = now
		stallIvl = ndiStallTimeout
		gone = false
		skipped += dropped
		if !connected {
			connected = true
			log.Printf("[NDIDistributor] Receiving %s: %dx%d %s", src.Name, f.Width, f.Height, fourCCString(f.FourCC))
			if reported {
				d.clearErr()
				reported = false
			}
		}
		if now.Sub(lastOut) < minGap {
			skipped++
			continue
		}
		lastOut = now

		if scaler == nil || scaler.srcW != f.Width || scaler.srcH != f.Height || scalerFmt != f.FourCC {
			if f.FourCC == ndi.FourCCUYVY {
				scaler = newUYVYScaler(f.Width, f.Height, d.width, d.height, true)
			} else {
				scaler = newBGRXScaler(f.Width, f.Height, d.width, d.height, true)
			}
			scalerFmt = f.FourCC
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
				float64(frames)/since.Seconds(), skipped, since.Seconds())
			frames, skipped, statsTime = 0, 0, now
		}
	}
}

func fourCCString(c uint32) string {
	return string([]byte{byte(c), byte(c >> 8), byte(c >> 16), byte(c >> 24)})
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

// Stop ends receiving and waits for the receive goroutine, which exits within
// about a millisecond; the receiver is released in the background.
func (d *NDIDistributor) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.running {
		return
	}
	close(d.stopCh)
	d.wg.Wait()
	d.running = false

	d.bufMu.Lock()
	putCanvasBuf(d.frontBuf)
	putCanvasBuf(d.backBuf)
	d.frontBuf, d.backBuf, d.hasFrame = nil, nil, false
	d.bufMu.Unlock()
}

// Canvas-sized frame buffers of stopped distributors, reused by the next one:
// every source switch would otherwise leave two canvas frames (12 MB each on
// the default canvas) for the garbage collector.
var (
	canvasBufMu sync.Mutex
	canvasBufs  [][]byte
)

func getCanvasBuf(n int) []byte {
	canvasBufMu.Lock()
	defer canvasBufMu.Unlock()
	for i, b := range canvasBufs {
		if len(b) == n {
			canvasBufs = append(canvasBufs[:i], canvasBufs[i+1:]...)
			return b
		}
	}
	return make([]byte, n)
}

func putCanvasBuf(b []byte) {
	if b == nil {
		return
	}
	canvasBufMu.Lock()
	defer canvasBufMu.Unlock()
	if len(canvasBufs) < 2 {
		canvasBufs = append(canvasBufs, b)
	}
}

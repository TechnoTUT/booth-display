package core

import "sync"

// PreviewBroadcaster fans the core's preview JPEGs out to MJPEG clients.
type PreviewBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[chan []byte]bool
	latestFrame []byte
}

func NewPreviewBroadcaster() *PreviewBroadcaster {
	return &PreviewBroadcaster{subscribers: make(map[chan []byte]bool)}
}

func (b *PreviewBroadcaster) Subscribe() chan []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan []byte, 5)
	b.subscribers[ch] = true
	if len(b.latestFrame) > 0 {
		ch <- b.latestFrame
	}
	return ch
}

func (b *PreviewBroadcaster) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers, ch)
	close(ch)
}

func (b *PreviewBroadcaster) Broadcast(frame []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.latestFrame = frame
	for ch := range b.subscribers {
		select {
		case ch <- frame:
		default: // slow consumer, skip frame
		}
	}
}

func (b *PreviewBroadcaster) GetLatest() []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.latestFrame
}

// clear forgets the last frame, so a stopped stream shows the offline placeholder.
func (b *PreviewBroadcaster) clear() {
	b.mu.Lock()
	b.latestFrame = nil
	b.mu.Unlock()
}

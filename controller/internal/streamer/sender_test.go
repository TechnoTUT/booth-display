package streamer

import (
	"sync/atomic"
	"testing"
	"time"

	"booth-display/controller/internal/protocol"
)

// newTestSender returns a sender with no send loop, so the queue only drains when
// the test reads from it.
func newTestSender(queueCap int) *DisplaySender {
	return &DisplaySender{
		queue:  make(chan []byte, queueCap),
		stopCh: make(chan struct{}),
	}
}

func frameOf(packets int) []byte {
	return make([]byte, packets*protocol.DefaultMaxPayloadSize)
}

func drain(s *DisplaySender) int {
	n := 0
	for {
		select {
		case <-s.queue:
			n++
		default:
			return n
		}
	}
}

func TestSendFrameQueuesWholeFrame(t *testing.T) {
	s := newTestSender(8)
	s.SendFrame(frameOf(3), protocol.PayloadTypeH264, true, 0)

	if got := len(s.queue); got != 3 {
		t.Fatalf("queued packets = %d, want 3", got)
	}
	if got := atomic.LoadUint64(&s.drops); got != 0 {
		t.Fatalf("drops = %d, want 0", got)
	}
}

func TestSendFrameDropsWholeFrameAndResyncsOnKeyframe(t *testing.T) {
	s := newTestSender(4)
	s.SendFrame(frameOf(3), protocol.PayloadTypeH264, true, 0)

	// Only 1 slot left: the 2-packet frame is dropped as a whole.
	s.SendFrame(frameOf(2), protocol.PayloadTypeH264, false, 1)
	if got := len(s.queue); got != 3 {
		t.Fatalf("queued packets after overflow = %d, want 3", got)
	}
	if got := atomic.LoadUint64(&s.drops); got != 2 {
		t.Fatalf("drops after overflow = %d, want 2", got)
	}

	// After the queue drains, P-frames are still skipped until a keyframe.
	drain(s)
	s.SendFrame(frameOf(1), protocol.PayloadTypeH264, false, 2)
	if got := len(s.queue); got != 0 {
		t.Fatalf("P-frame queued while waiting for keyframe: %d packets", got)
	}

	s.SendFrame(frameOf(2), protocol.PayloadTypeH264, true, 3)
	s.SendFrame(frameOf(1), protocol.PayloadTypeH264, false, 4)
	if got := len(s.queue); got != 3 {
		t.Fatalf("queued packets after keyframe = %d, want 3", got)
	}
	if got := atomic.LoadUint64(&s.drops); got != 3 {
		t.Fatalf("total drops = %d, want 3", got)
	}
}

func TestSendFrameAdmitsOversizedFrameWhenQueueEmpty(t *testing.T) {
	s := newTestSender(4)
	const packets = 10

	received := make(chan int)
	go func() {
		n := 0
		for n < packets {
			<-s.queue
			n++
		}
		received <- n
	}()

	s.SendFrame(frameOf(packets), protocol.PayloadTypeH264, true, 0)

	select {
	case n := <-received:
		if n != packets {
			t.Fatalf("received %d packets, want %d", n, packets)
		}
	case <-time.After(time.Second):
		t.Fatal("oversized keyframe was not delivered")
	}
	if got := atomic.LoadUint64(&s.drops); got != 0 {
		t.Fatalf("drops = %d, want 0", got)
	}
	if s.needKeyframe {
		t.Fatal("needKeyframe set after delivering an oversized keyframe")
	}
}

func TestSendFrameDropsOversizedFrameWhenQueueBusy(t *testing.T) {
	s := newTestSender(4)
	s.SendFrame(frameOf(1), protocol.PayloadTypeH264, true, 0)
	s.SendFrame(frameOf(10), protocol.PayloadTypeH264, true, 1)

	if got := len(s.queue); got != 1 {
		t.Fatalf("queued packets = %d, want 1", got)
	}
	if got := atomic.LoadUint64(&s.drops); got != 10 {
		t.Fatalf("drops = %d, want 10", got)
	}
}

func TestSendFrameDoesNotBlockAfterStop(t *testing.T) {
	s := newTestSender(4)
	close(s.stopCh)

	done := make(chan struct{})
	go func() {
		s.SendFrame(frameOf(10), protocol.PayloadTypeH264, true, 0)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SendFrame blocked after the sender was stopped")
	}
}

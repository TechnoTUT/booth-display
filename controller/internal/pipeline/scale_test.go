package pipeline

import (
	"encoding/binary"
	"testing"
)

func TestCenterCropRect(t *testing.T) {
	cases := []struct {
		srcW, srcH, dstW, dstH int
		x, y, w, h             int
	}{
		{1920, 1080, 5760, 540, 0, 450, 1920, 180}, // 16:9 into 32:3: crop top/bottom
		{5760, 540, 5760, 540, 0, 0, 5760, 540},    // same size
		{1920, 1080, 1920, 1080, 0, 0, 1920, 1080}, // same aspect
		{1000, 100, 100, 100, 450, 0, 100, 100},    // wider: crop left/right
	}
	for _, c := range cases {
		x, y, w, h := centerCropRect(c.srcW, c.srcH, c.dstW, c.dstH)
		if x != c.x || y != c.y || w != c.w || h != c.h {
			t.Errorf("centerCropRect(%d,%d,%d,%d) = %d,%d,%d,%d, want %d,%d,%d,%d",
				c.srcW, c.srcH, c.dstW, c.dstH, x, y, w, h, c.x, c.y, c.w, c.h)
		}
	}
}

func makeBGRX(w, h, stride int, f func(x, y int) uint32) []byte {
	b := make([]byte, stride*h)
	for y := range h {
		for x := range w {
			binary.LittleEndian.PutUint32(b[y*stride+x*4:], f(x, y))
		}
	}
	return b
}

func pixel(b []byte, w, x, y int) uint32 {
	return binary.LittleEndian.Uint32(b[(y*w+x)*4:])
}

func TestBGRXScalerIdentityAndCrop(t *testing.T) {
	// Source with a padded stride; destination equals the center crop exactly.
	srcW, srcH, stride := 8, 8, 8*4+16
	src := makeBGRX(srcW, srcH, stride, func(x, y int) uint32 { return uint32(x<<16 | y<<8 | 0x7F) })
	s := newBGRXScaler(srcW, srcH, 8, 2, true) // crop rows 3..4
	dst := make([]byte, 8*2*4)
	s.Scale(dst, src, stride)
	for y := range 2 {
		for x := range 8 {
			want := uint32(x<<16 | (y+3)<<8 | 0x7F)
			if got := pixel(dst, 8, x, y) & 0xFFFFFF; got != want&0xFFFFFF {
				t.Fatalf("pixel (%d,%d) = %06x, want %06x", x, y, got, want)
			}
		}
	}
}

func TestBGRXScalerFlatAndGradient(t *testing.T) {
	// A flat color must stay exactly flat (the X byte is ignored).
	src := makeBGRX(64, 36, 64*4, func(x, y int) uint32 { return 0xFF336699 })
	s := newBGRXScaler(64, 36, 192, 18, true)
	dst := make([]byte, 192*18*4)
	s.Scale(dst, src, 64*4)
	for i := 0; i < len(dst); i += 4 {
		if got := binary.LittleEndian.Uint32(dst[i:]) & 0xFFFFFF; got != 0x336699 {
			t.Fatalf("flat pixel %d = %06x, want 336699", i/4, got)
		}
	}

	// A horizontal gradient must stay monotonic and within its range.
	src = makeBGRX(64, 4, 64*4, func(x, y int) uint32 { return uint32(x * 4) })
	s = newBGRXScaler(64, 4, 640, 4, false)
	dst = make([]byte, 640*4*4)
	s.Scale(dst, src, 64*4)
	prev := uint32(0)
	for x := range 640 {
		v := pixel(dst, 640, x, 1) & 0xFF
		if v < prev || v > 252 {
			t.Fatalf("gradient broken at x=%d: %d after %d", x, v, prev)
		}
		prev = v
	}
	if prev < 248 {
		t.Errorf("gradient end = %d, want close to 252", prev)
	}
}

// BenchmarkScaleNDI1080pToCanvas is the common case: a 1080p NDI source
// center-cropped and stretched onto the 5760x540 canvas.
func BenchmarkScaleNDI1080pToCanvas(b *testing.B) {
	src := makeBGRX(1920, 1080, 1920*4, func(x, y int) uint32 { return uint32(x*y) * 2654435761 })
	s := newBGRXScaler(1920, 1080, 5760, 540, true)
	dst := make([]byte, 5760*540*4)
	b.SetBytes(int64(len(dst)))
	for b.Loop() {
		s.Scale(dst, src, 1920*4)
	}
}

func BenchmarkScaleNDI1080pToCanvasSingleCore(b *testing.B) {
	src := makeBGRX(1920, 1080, 1920*4, func(x, y int) uint32 { return uint32(x*y) * 2654435761 })
	s := newBGRXScaler(1920, 1080, 5760, 540, true)
	s.workers = 1
	dst := make([]byte, 5760*540*4)
	b.SetBytes(int64(len(dst)))
	for b.Loop() {
		s.Scale(dst, src, 1920*4)
	}
}

func BenchmarkScaleNDI4KToCanvas(b *testing.B) {
	src := makeBGRX(3840, 2160, 3840*4, func(x, y int) uint32 { return uint32(x*y) * 2654435761 })
	s := newBGRXScaler(3840, 2160, 5760, 540, true)
	dst := make([]byte, 5760*540*4)
	b.SetBytes(int64(len(dst)))
	for b.Loop() {
		s.Scale(dst, src, 3840*4)
	}
}

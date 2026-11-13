package pipeline

import (
	"encoding/binary"
	"testing"
	"unsafe"
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

// makeUYVY builds a UYVY frame whose macropixel m of row y is f(m, y).
func makeUYVY(w, h, stride int, f func(m, y int) uint32) []byte {
	b := make([]byte, stride*h)
	for y := range h {
		for m := range (w + 1) / 2 {
			binary.LittleEndian.PutUint32(b[y*stride+m*4:], f(m, y))
		}
	}
	return b
}

func uyvy(u, y0, v, y1 uint32) uint32 { return u | y0<<8 | v<<16 | y1<<24 }

func TestYUVConvertKnownColors(t *testing.T) {
	near := func(got, want uint32) bool {
		for s := 0; s < 32; s += 8 {
			d := int(got>>s&0xFF) - int(want>>s&0xFF)
			if d < -2 || d > 2 {
				return false
			}
		}
		return true
	}
	t709 := newYUVTables(false)
	t601 := newYUVTables(true)
	cases := []struct {
		name string
		tab  *yuvTables
		mp   uint32
		want uint32 // BGRX
	}{
		{"white", t709, uyvy(128, 235, 128, 235), 0xFFFFFFFF},
		{"black", t709, uyvy(128, 16, 128, 16), 0xFF000000},
		{"709 red", t709, uyvy(102, 63, 240, 63), 0xFFFF0000},
		{"709 green", t709, uyvy(42, 173, 26, 173), 0xFF00FF00},
		{"709 blue", t709, uyvy(240, 32, 118, 32), 0xFF0000FF},
		{"601 red", t601, uyvy(90, 81, 240, 81), 0xFFFF0000},
	}
	for _, c := range cases {
		out := make([]uint32, 2)
		c.tab.convertRow(out, []uint32{c.mp}, 0)
		for i, got := range out {
			if !near(got, c.want) {
				t.Errorf("%s pixel %d = %08x, want %08x", c.name, i, got, c.want)
			}
		}
	}
}

// TestUYVYScalerMatchesBGRX checks that converting only the cropped part of
// each row (here starting on an odd pixel) gives exactly what converting the
// whole frame first and scaling it as BGRX gives.
func TestUYVYScalerMatchesBGRX(t *testing.T) {
	const srcW, srcH, dstW, dstH = 1003, 120, 300, 300 // crop 120x120 at x=441
	stride := (srcW+1)/2*4 + 8
	src := makeUYVY(srcW, srcH, stride, func(m, y int) uint32 {
		h := uint32(m*7919+y*104729) * 2654435761
		return uyvy(h&0xFF, 16+(h>>8&0xFF)%220, h>>16&0xFF, 16+(h>>24)%220)
	})
	us := newUYVYScaler(srcW, srcH, dstW, dstH, true)
	if us.cropX%2 != 1 {
		t.Fatalf("test wants an odd crop start, got %d", us.cropX)
	}

	// Reference: whole rows converted to BGRX, then the BGRX scaler.
	tab := newYUVTables(true)
	ref := make([]byte, srcW*4*srcH)
	for y := range srcH {
		row := unsafe.Slice((*uint32)(unsafe.Pointer(&src[y*stride])), (srcW+1)/2)
		out := unsafe.Slice((*uint32)(unsafe.Pointer(&ref[y*srcW*4])), srcW)
		tab.convertRow(out, row, 0)
	}
	bs := newBGRXScaler(srcW, srcH, dstW, dstH, true)

	got := make([]byte, dstW*dstH*4)
	want := make([]byte, dstW*dstH*4)
	us.Scale(got, src, stride)
	bs.Scale(want, ref, srcW*4)
	for y := range dstH {
		for x := range dstW {
			if g, w := pixel(got, dstW, x, y), pixel(want, dstW, x, y); g != w {
				t.Fatalf("pixel %d,%d = %08x, want %08x", x, y, g, w)
			}
		}
	}
}

// BenchmarkScaleNDIUYVY1080pToCanvas is the NDI receive path: a UYVY 1080p
// frame, converted (cropped rows only) and scaled onto the 5760x540 canvas.
func BenchmarkScaleNDIUYVY1080pToCanvas(b *testing.B) {
	src := makeUYVY(1920, 1080, 1920*2, func(m, y int) uint32 { return uint32(m*y) * 2654435761 })
	s := newUYVYScaler(1920, 1080, 5760, 540, true)
	dst := make([]byte, 5760*540*4)
	b.SetBytes(int64(len(dst)))
	for b.Loop() {
		s.Scale(dst, src, 1920*2)
	}
}

func BenchmarkScaleNDIUYVY1080pToCanvasSingleCore(b *testing.B) {
	src := makeUYVY(1920, 1080, 1920*2, func(m, y int) uint32 { return uint32(m*y) * 2654435761 })
	s := newUYVYScaler(1920, 1080, 5760, 540, true)
	s.workers = 1
	dst := make([]byte, 5760*540*4)
	b.SetBytes(int64(len(dst)))
	for b.Loop() {
		s.Scale(dst, src, 1920*2)
	}
}

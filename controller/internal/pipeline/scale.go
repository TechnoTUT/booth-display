package pipeline

import (
	"runtime"
	"sync"
	"unsafe"
)

// bgrxScaler crops and bilinearly resizes 32-bit BGRX frames. The lookup tables
// are built once per source geometry, and output rows are split across all cores.
//
// The horizontal pass runs on source rows and its results are cached, so each
// source row is resampled once instead of once per output row that uses it. For
// the common case, a 1920x180 crop of a 1080p source stretched to 5760x540, that
// is 180 horizontal passes instead of 540. The vertical pass then only blends two
// contiguous rows, two pixels per 64-bit word.
//
// Weights are fixed-point (0..256) and every channel, including X, is blended
// with SWAR arithmetic: one multiply covers two channels per 32-bit pixel.
//
// A scaler made by newUYVYScaler takes UYVY 4:2:2 input instead (as NDI
// delivers it) and converts only the cropped part of the source rows it reads
// to BGRX, just before the horizontal pass. For a 1080p source on the default
// canvas that is 180 of 1080 rows, far less work than having the NDI SDK
// convert every frame to BGRX in full.
type bgrxScaler struct {
	srcW, srcH int
	cropX      int
	cropY      int
	cropW      int
	cropH      int
	dstW, dstH int

	xIdx []int32  // left source column (relative to cropX), at most cropW-2
	xWt  []uint32 // weight of the right column, 0..256
	yIdx []int32  // upper source row (absolute), at most cropY+cropH-2
	yWt  []uint32 // weight of the lower row, 0..256

	workers int
	caches  []rowCache

	uyvy *yuvTables // non-nil for UYVY input
}

// rowCache holds the two most recent horizontally resampled source rows of one worker.
type rowCache struct {
	row  [2][]uint32
	tag  [2]int   // source row index held in row[i], -1 if none
	conv []uint32 // UYVY input: the cropped source row converted to BGRX
}

// newUYVYScaler is newBGRXScaler for UYVY input. Sources with fewer than 720
// lines use BT.601 colors, others BT.709, as NDI does.
func newUYVYScaler(srcW, srcH, dstW, dstH int, centerCrop bool) *bgrxScaler {
	s := newBGRXScaler(srcW, srcH, dstW, dstH, centerCrop)
	s.uyvy = newYUVTables(srcH < 720)
	for i := range s.caches {
		s.caches[i].conv = make([]uint32, s.cropW)
	}
	return s
}

// newBGRXScaler prepares a scaler from a srcW x srcH frame to dstW x dstH.
// With center crop, the source is cropped to the destination aspect ratio first
// (no letterboxing, no distortion); otherwise it is stretched.
func newBGRXScaler(srcW, srcH, dstW, dstH int, centerCrop bool) *bgrxScaler {
	cx, cy, cw, ch := 0, 0, srcW, srcH
	if centerCrop {
		cx, cy, cw, ch = centerCropRect(srcW, srcH, dstW, dstH)
	}
	s := &bgrxScaler{
		srcW: srcW, srcH: srcH,
		cropX: cx, cropY: cy, cropW: cw, cropH: ch,
		dstW: dstW, dstH: dstH,
		workers: runtime.NumCPU(),
	}
	s.xIdx, s.xWt = bilinearTable(cw, dstW, 0)
	s.yIdx, s.yWt = bilinearTable(ch, dstH, cy)
	s.caches = make([]rowCache, s.workers)
	for i := range s.caches {
		c := &s.caches[i]
		for j := range c.row {
			c.row[j] = make([]uint32, dstW)
		}
	}
	return s
}

// centerCropRect returns the largest centered rectangle of the source that has
// the destination's aspect ratio.
func centerCropRect(srcW, srcH, dstW, dstH int) (x, y, w, h int) {
	// Compare srcW/srcH with dstW/dstH without floating point.
	switch {
	case srcW*dstH > dstW*srcH: // source is wider: crop left and right
		w = (srcH*dstW + dstH/2) / dstH
		w = max(1, min(srcW, w))
		return (srcW - w) / 2, 0, w, srcH
	case srcW*dstH < dstW*srcH: // source is taller: crop top and bottom
		h = (srcW*dstH + dstW/2) / dstW
		h = max(1, min(srcH, h))
		return 0, (srcH - h) / 2, srcW, h
	default:
		return 0, 0, srcW, srcH
	}
}

// bilinearTable maps each of dstN output samples to the lower of the two source
// samples it lies between (plus offset) and the weight of the upper one, using
// pixel-center alignment. The lower index never exceeds srcN-2, so index+1 is
// always valid without a bounds branch in the inner loops; past the last sample
// the weight is 256. A single-sample source maps everything to sample 0.
func bilinearTable(srcN, dstN, offset int) ([]int32, []uint32) {
	idx := make([]int32, dstN)
	wt := make([]uint32, dstN)
	for i := range dstN {
		if srcN < 2 {
			idx[i] = int32(offset)
			continue
		}
		// Source coordinate of the output pixel center, in 1/256 pixel units.
		pos := ((2*i+1)*srcN*256)/(2*dstN) - 128
		pos = max(0, pos)
		p, f := pos>>8, pos&0xFF
		if p >= srcN-1 {
			p, f = srcN-2, 256
		}
		idx[i] = int32(p + offset)
		wt[i] = uint32(f)
	}
	return idx, wt
}

// blend32 returns a*(256-w)/256 + b*w/256 for each byte of a BGRX pixel.
func blend32(a, b, w uint32) uint32 {
	const m = 0x00FF00FF
	iw := 256 - w
	lo := ((a&m)*iw + (b&m)*w) >> 8 & m
	hi := (((a>>8)&m)*iw + ((b>>8)&m)*w) &^ m
	return lo | hi
}

// blend64 is blend32 for two pixels packed in a uint64.
func blend64(a, b, w uint64) uint64 {
	const m = 0x00FF00FF00FF00FF
	iw := 256 - w
	lo := ((a&m)*iw + (b&m)*w) >> 8 & m
	hi := (((a>>8)&m)*iw + ((b>>8)&m)*w) &^ m
	return lo | hi
}

// Scale writes the resized frame into dst (dstW*dstH*4 bytes). src holds srcH
// rows of srcStride bytes. Both must be 4-byte aligned, which Go heap slices
// and NDI frame buffers are.
func (s *bgrxScaler) Scale(dst []byte, src []byte, srcStride int) {
	rowBytes := s.srcW * 4
	if s.uyvy != nil {
		rowBytes = (s.srcW + 1) / 2 * 4
	}
	if len(dst) < s.dstW*s.dstH*4 || len(src) < (s.srcH-1)*srcStride+rowBytes || srcStride%4 != 0 {
		return
	}
	d := unsafe.Slice((*uint32)(unsafe.Pointer(&dst[0])), s.dstW*s.dstH)
	src32 := unsafe.Slice((*uint32)(unsafe.Pointer(&src[0])), len(src)/4)
	stride := srcStride / 4

	if s.cropW == s.dstW && s.cropH == s.dstH && s.uyvy == nil {
		for y := range s.dstH {
			row := (s.cropY+y)*stride + s.cropX
			copy(d[y*s.dstW:(y+1)*s.dstW], src32[row:row+s.dstW])
		}
		return
	}

	var wg sync.WaitGroup
	band := (s.dstH + s.workers - 1) / s.workers
	for w := 0; w < s.workers; w++ {
		y0, y1 := w*band, min((w+1)*band, s.dstH)
		if y0 >= y1 {
			break
		}
		wg.Add(1)
		go func(c *rowCache, y0, y1 int) {
			defer wg.Done()
			c.tag = [2]int{-1, -1}
			s.scaleRows(d, src32, stride, c, y0, y1)
		}(&s.caches[w], y0, y1)
	}
	wg.Wait()
}

// hrow returns source row sy resampled to dstW pixels, from the cache if present.
func (s *bgrxScaler) hrow(src []uint32, stride int, c *rowCache, sy int) []uint32 {
	if c.tag[0] == sy {
		return c.row[0]
	}
	if c.tag[1] == sy {
		return c.row[1]
	}
	// Replace the entry that is not the other row in use: rows are requested in
	// increasing order, so the lower tag is the stale one.
	slot := 0
	if c.tag[1] < c.tag[0] {
		slot = 1
	}
	c.tag[slot] = sy
	out := c.row[slot]

	var r []uint32
	if s.uyvy != nil {
		r = c.conv
		s.uyvy.convertRow(r, src[sy*stride:sy*stride+(s.srcW+1)/2], s.cropX)
	} else {
		r = src[sy*stride+s.cropX : sy*stride+s.cropX+s.cropW]
	}
	if len(r) < 2 {
		for x := range out {
			out[x] = r[0]
		}
		return out
	}
	xIdx, xWt := s.xIdx[:len(out)], s.xWt[:len(out)]
	for x := range out {
		i := xIdx[x]
		out[x] = blend32(r[i], r[i+1], xWt[x])
	}
	return out
}

func (s *bgrxScaler) scaleRows(d, src []uint32, stride int, c *rowCache, y0, y1 int) {
	single := s.cropH < 2
	for y := y0; y < y1; y++ {
		out := d[y*s.dstW : (y+1)*s.dstW]
		sy, wy := int(s.yIdx[y]), s.yWt[y]
		switch {
		case single || wy == 0:
			copy(out, s.hrow(src, stride, c, sy))
			continue
		case wy == 256:
			copy(out, s.hrow(src, stride, c, sy+1))
			continue
		}
		a := s.hrow(src, stride, c, sy)
		b := s.hrow(src, stride, c, sy+1)

		n2 := len(out) / 2
		o64 := unsafe.Slice((*uint64)(unsafe.Pointer(&out[0])), n2)
		a64 := unsafe.Slice((*uint64)(unsafe.Pointer(&a[0])), n2)
		b64 := unsafe.Slice((*uint64)(unsafe.Pointer(&b[0])), n2)
		w := uint64(wy)
		for i := range o64 {
			o64[i] = blend64(a64[i], b64[i], w)
		}
		if len(out)%2 == 1 {
			last := len(out) - 1
			out[last] = blend32(a[last], b[last], wy)
		}
	}
}

// yuvTables converts limited-range Y'CbCr to BGRX with 16-bit fixed-point
// lookup tables, so each pixel costs a few table loads and adds and no branches.
type yuvTables struct {
	y  [256]int32 // (Y-16) * 255/219, scaled by 2^16, plus rounding
	rv [256]int32 // Cr contribution to R
	gu [256]int32 // Cb contribution to G (negative)
	gv [256]int32 // Cr contribution to G (negative)
	bu [256]int32 // Cb contribution to B
	// clamp maps an integer result v (-256 <= v < 768; actual results stay
	// within about -250..510) at index v+256 to 0..255.
	clamp [1024]uint32
}

func newYUVTables(bt601 bool) *yuvTables {
	// Kr/Kb of the color matrix; chroma scaled by 255/224 for limited range.
	kr, kb := 0.2126, 0.0722
	if bt601 {
		kr, kb = 0.299, 0.114
	}
	kg := 1 - kr - kb
	const yScale, cScale = 255.0 / 219.0, 255.0 / 224.0
	rv := 2 * (1 - kr) * cScale
	bu := 2 * (1 - kb) * cScale
	gu := 2 * (1 - kb) * kb / kg * cScale
	gv := 2 * (1 - kr) * kr / kg * cScale

	t := &yuvTables{}
	fix := func(f float64) int32 {
		if f < 0 {
			return int32(f*65536 - 0.5)
		}
		return int32(f*65536 + 0.5)
	}
	for i := range 256 {
		c := float64(i - 128)
		t.y[i] = fix(float64(i-16)*yScale) + 1<<15 + 256<<16 // +256: clamp table offset
		t.rv[i] = fix(rv * c)
		t.gu[i] = fix(-gu * c)
		t.gv[i] = fix(-gv * c)
		t.bu[i] = fix(bu * c)
	}
	for i := range t.clamp {
		t.clamp[i] = uint32(min(max(i-256, 0), 255))
	}
	return t
}

// pixel returns the BGRX pixel for luma y and the chroma terms r, g, b.
func (t *yuvTables) pixel(y uint8, r, g, b int32) uint32 {
	yy := t.y[y]
	return t.clamp[uint32(yy+b)>>16&1023] |
		t.clamp[uint32(yy+g)>>16&1023]<<8 |
		t.clamp[uint32(yy+r)>>16&1023]<<16 | 0xFF000000
}

// convertRow converts len(out) pixels of a UYVY row, starting at pixel x0, to
// BGRX. row holds the row's macropixels (U Y0 V Y1, little-endian uint32).
// Chroma is co-sited with even pixels; odd pixels use the average of the two
// neighboring chroma samples.
func (t *yuvTables) convertRow(out, row []uint32, x0 int) {
	n := len(out)
	if n == 0 || len(row) == 0 {
		return
	}
	last := len(row) - 1
	m := x0 >> 1
	i := 0
	if x0&1 == 1 { // starts on an odd pixel: second half of macropixel m
		mp, next := row[m], row[min(m+1, last)]
		u := uint8((mp&0xFF + next&0xFF + 1) >> 1)
		v := uint8((mp>>16&0xFF + next>>16&0xFF + 1) >> 1)
		out[0] = t.pixel(uint8(mp>>24), t.rv[v], t.gu[u]+t.gv[v], t.bu[u])
		i, m = 1, m+1
	}
	// Pairs whose next macropixel exists; the last pair is handled after.
	pairs := min((n-i)/2, last-m)
	if pairs > 0 {
		src := row[m : m+pairs+1]
		dst := out[i : i+2*pairs]
		for k := range pairs {
			mp, next := src[k], src[k+1]
			u0, v0 := uint8(mp), uint8(mp>>16)
			u1 := uint8((uint32(u0) + next&0xFF + 1) >> 1)
			v1 := uint8((uint32(v0) + next>>16&0xFF + 1) >> 1)
			dst[2*k] = t.pixel(uint8(mp>>8), t.rv[v0], t.gu[u0]+t.gv[v0], t.bu[u0])
			dst[2*k+1] = t.pixel(uint8(mp>>24), t.rv[v1], t.gu[u1]+t.gv[v1], t.bu[u1])
		}
		i, m = i+2*pairs, m+pairs
	}
	for ; i < n; i, m = i+2, m+1 { // at most one pair at the row end
		mp := row[m]
		u, v := uint8(mp), uint8(mp>>16)
		out[i] = t.pixel(uint8(mp>>8), t.rv[v], t.gu[u]+t.gv[v], t.bu[u])
		if i+1 < n {
			out[i+1] = t.pixel(uint8(mp>>24), t.rv[v], t.gu[u]+t.gv[v], t.bu[u])
		}
	}
}

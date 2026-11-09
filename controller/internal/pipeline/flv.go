package pipeline

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// FLVReader turns an FLV stream carrying H.264 (as written by ffmpeg "-f flv")
// into Annex-B Access Units.
//
// The encoder output is read as FLV rather than as a raw Annex-B stream because
// every FLV tag carries its size. An Access Unit is complete the moment its last
// byte arrives. In a raw stream the end of a frame is only known when the next
// frame's start code arrives, which holds every frame back by one frame interval.
type FLVReader struct {
	r          io.Reader
	hdr        [11]byte
	tag        []byte
	out        []byte
	paramSets  []byte // SPS and PPS from the AVC sequence header, in Annex-B form
	nalLenSize int
	gotHeader  bool
}

const (
	flvTagVideo     = 9
	flvCodecAVC     = 7
	flvAVCSeqHeader = 0
	flvAVCNALU      = 1
	maxFLVTagSize   = 16 << 20
)

var annexBStartCode = []byte{0, 0, 0, 1}

func NewFLVReader(r io.Reader) *FLVReader {
	return &FLVReader{r: r, nalLenSize: 4}
}

// Next returns the next Access Unit. The returned data is only valid until the
// following call to Next.
func (f *FLVReader) Next() (data []byte, isKeyframe bool, err error) {
	if !f.gotHeader {
		if err := f.readFileHeader(); err != nil {
			return nil, false, err
		}
		f.gotHeader = true
	}
	for {
		// Tag header: type(1) size(3) timestamp(3) timestamp_ext(1) stream_id(3)
		if _, err := io.ReadFull(f.r, f.hdr[:]); err != nil {
			return nil, false, err
		}
		tagType := f.hdr[0] & 0x1F
		size := int(f.hdr[1])<<16 | int(f.hdr[2])<<8 | int(f.hdr[3])
		if size > maxFLVTagSize {
			return nil, false, fmt.Errorf("flv: tag size %d too large", size)
		}
		// Tag body followed by the 4-byte PreviousTagSize.
		if cap(f.tag) < size+4 {
			f.tag = make([]byte, size+4)
		}
		f.tag = f.tag[:size+4]
		if _, err := io.ReadFull(f.r, f.tag); err != nil {
			return nil, false, err
		}
		body := f.tag[:size]

		if tagType != flvTagVideo || len(body) < 5 || body[0]&0x0F != flvCodecAVC {
			continue
		}
		switch body[1] {
		case flvAVCSeqHeader:
			if err := f.parseAVCConfig(body[5:]); err != nil {
				return nil, false, err
			}
		case flvAVCNALU:
			data, isKeyframe, err := f.toAnnexB(body[5:])
			if err != nil {
				return nil, false, err
			}
			if len(data) > 0 {
				return data, isKeyframe, nil
			}
		}
	}
}

func (f *FLVReader) readFileHeader() error {
	var h [9]byte
	if _, err := io.ReadFull(f.r, h[:]); err != nil {
		return err
	}
	if h[0] != 'F' || h[1] != 'L' || h[2] != 'V' {
		return errors.New("flv: bad signature")
	}
	// Skip any extra header bytes plus PreviousTagSize0.
	skip := int64(binary.BigEndian.Uint32(h[5:9])) - 9 + 4
	if skip < 0 {
		return errors.New("flv: bad header size")
	}
	_, err := io.CopyN(io.Discard, f.r, skip)
	return err
}

// parseAVCConfig reads an AVCDecoderConfigurationRecord and keeps its SPS/PPS.
func (f *FLVReader) parseAVCConfig(b []byte) error {
	if len(b) < 6 {
		return errors.New("flv: short AVC config")
	}
	f.nalLenSize = int(b[4]&0x03) + 1
	var ps []byte
	p := 5
	for _, countMask := range []byte{0x1F, 0xFF} { // SPS count, then PPS count
		if p >= len(b) {
			return errors.New("flv: truncated AVC config")
		}
		n := int(b[p] & countMask)
		p++
		for i := 0; i < n; i++ {
			if p+2 > len(b) {
				return errors.New("flv: truncated AVC config")
			}
			l := int(binary.BigEndian.Uint16(b[p:]))
			p += 2
			if p+l > len(b) {
				return errors.New("flv: truncated AVC config")
			}
			ps = append(ps, annexBStartCode...)
			ps = append(ps, b[p:p+l]...)
			p += l
		}
	}
	f.paramSets = ps
	return nil
}

// toAnnexB converts length-prefixed NAL units to Annex-B. For an IDR access unit
// without in-band SPS/PPS, the parameter sets from the sequence header are
// inserted (after the AUD, which must stay first) so receivers can join on any
// keyframe.
func (f *FLVReader) toAnnexB(b []byte) ([]byte, bool, error) {
	out := f.out[:0]
	isKey, hasSPS := false, false
	afterAUD := 0 // insertion point for parameter sets: just past a leading AUD
	for p := 0; p < len(b); {
		if p+f.nalLenSize > len(b) {
			return nil, false, errors.New("flv: truncated NAL length")
		}
		l := 0
		for i := 0; i < f.nalLenSize; i++ {
			l = l<<8 | int(b[p+i])
		}
		p += f.nalLenSize
		if l == 0 {
			continue
		}
		if p+l > len(b) {
			return nil, false, errors.New("flv: truncated NAL unit")
		}
		nal := b[p : p+l]
		p += l
		switch NALUType(nal[0] & 0x1F) {
		case NALUTypeIDR:
			isKey = true
		case NALUTypeSPS:
			hasSPS = true
		}
		out = append(out, annexBStartCode...)
		out = append(out, nal...)
		if NALUType(nal[0]&0x1F) == NALUTypeAUD && len(out) == 4+len(nal) {
			afterAUD = len(out)
		}
	}
	if isKey && !hasSPS && len(f.paramSets) > 0 {
		n := len(f.paramSets)
		out = append(out, f.paramSets...) // grow by n
		copy(out[afterAUD+n:], out[afterAUD:len(out)-n])
		copy(out[afterAUD:], f.paramSets)
	}
	f.out = out
	return out, isKey, nil
}

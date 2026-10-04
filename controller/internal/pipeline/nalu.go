package pipeline

import (
	"bytes"
)

// NALUType indicates the H.264 NAL unit type.
type NALUType byte

const (
	NALUTypeUnspecified NALUType = 0
	NALUTypeNonIDR      NALUType = 1 // Non-IDR picture slice
	NALUTypeIDR         NALUType = 5 // Coded slice of an IDR picture (keyframe)
	NALUTypeSEI         NALUType = 6 // Supplemental enhancement information
	NALUTypeSPS         NALUType = 7 // Sequence parameter set
	NALUTypePPS         NALUType = 8 // Picture parameter set
	NALUTypeAUD         NALUType = 9 // Access unit delimiter
)

// NALUnit represents a single H.264 NAL unit with its start code.
type NALUnit struct {
	Data       []byte
	Type       NALUType
	IsKeyframe bool
}

// NALUParser extracts Annex-B NAL units from a continuous byte stream.
type NALUParser struct {
	buffer []byte
}

func NewNALUParser() *NALUParser {
	return &NALUParser{
		buffer: make([]byte, 0, 64*1024),
	}
}

// Push appends data to the internal buffer and returns all completely parsed NAL units.
func (p *NALUParser) Push(data []byte) []NALUnit {
	p.buffer = append(p.buffer, data...)

	var units []NALUnit
	for {
		startIdx, startCodeLen := p.findStartCode(p.buffer)
		if startIdx == -1 {
			// No start code found; keep only the last 3 bytes (in case start code is split)
			if len(p.buffer) > 3 {
				p.buffer = p.buffer[len(p.buffer)-3:]
			}
			break
		}

		// Find the NEXT start code after startIdx + startCodeLen
		nextIdx, _ := p.findStartCode(p.buffer[startIdx+startCodeLen:])
		if nextIdx == -1 {
			// Incomplete NALU, keep from startIdx onwards
			p.buffer = p.buffer[startIdx:]
			break
		}

		endIdx := startIdx + startCodeLen + nextIdx
		rawNAL := p.buffer[startIdx:endIdx]

		// The NAL unit payload starts after the start code
		nalPayload := rawNAL[startCodeLen:]
		if len(nalPayload) > 0 {
			nalType := NALUType(nalPayload[0] & 0x1F)
			isKey := (nalType == NALUTypeIDR || nalType == NALUTypeSPS || nalType == NALUTypePPS)

			units = append(units, NALUnit{
				Data:       rawNAL,
				Type:       nalType,
				IsKeyframe: isKey,
			})
		}

		// Advance buffer past this NAL unit
		p.buffer = p.buffer[endIdx:]
	}

	return units
}

func (p *NALUParser) findStartCode(buf []byte) (int, int) {
	fourByte := []byte{0x00, 0x00, 0x00, 0x01}
	threeByte := []byte{0x00, 0x00, 0x01}

	idx4 := bytes.Index(buf, fourByte)
	idx3 := bytes.Index(buf, threeByte)

	if idx4 != -1 && (idx3 == -1 || idx4 <= idx3) {
		return idx4, 4
	}
	if idx3 != -1 {
		return idx3, 3
	}
	return -1, 0
}

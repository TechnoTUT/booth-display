package pipeline

import (
	"bytes"
	"testing"
)

func TestNALUParser(t *testing.T) {
	parser := NewNALUParser()

	// SPS (0x67 = 0x20 | 7), PPS (0x68 = 0x20 | 8), IDR (0x65 = 0x20 | 5)
	stream := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, // SPS
		0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x3c, 0x80, // PPS
		0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x10, // IDR (3-byte start code)
		0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x11, 0x22, // Non-IDR (0x41 & 0x1F = 1)
	}

	// Feed first half
	u1 := parser.Push(stream[:12])
	if len(u1) != 1 {
		t.Fatalf("expected 1 unit (SPS), got %d", len(u1))
	}
	if u1[0].Type != NALUTypeSPS || !u1[0].IsKeyframe {
		t.Fatalf("expected SPS keyframe, got %+v", u1[0])
	}

	// Feed second half
	u2 := parser.Push(stream[12:])
	// Append one more start code to flush the last unit
	u3 := parser.Push([]byte{0x00, 0x00, 0x00, 0x01, 0x00})
	total := append(u2, u3...)

	if len(total) != 3 {
		t.Fatalf("expected 3 remaining units (PPS, IDR, Non-IDR), got %d", len(total))
	}

	if total[0].Type != NALUTypePPS {
		t.Errorf("expected PPS, got %v", total[0].Type)
	}
	if total[1].Type != NALUTypeIDR {
		t.Errorf("expected IDR, got %v", total[1].Type)
	}
	if total[2].Type != NALUTypeNonIDR {
		t.Errorf("expected Non-IDR, got %v", total[2].Type)
	}

	// Verify data integrity
	expectedIDR := []byte{0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x10}
	if !bytes.Equal(total[1].Data, expectedIDR) {
		t.Errorf("IDR data mismatch. Got %x, want %x", total[1].Data, expectedIDR)
	}
}

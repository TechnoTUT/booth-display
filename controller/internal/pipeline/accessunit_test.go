package pipeline

import "testing"

func nal(t NALUType, key bool, payload ...byte) NALUnit {
	d := append([]byte{0, 0, 0, 1, byte(t)}, payload...)
	return NALUnit{Data: d, Type: t, IsKeyframe: key}
}

func TestAccessUnitAssembler(t *testing.T) {
	a := NewAccessUnitAssembler()
	var got []*AccessUnit
	push := func(n NALUnit) {
		if au := a.Push(n); au != nil {
			got = append(got, au)
		}
	}

	// Keyframe AU: AUD SPS PPS SEI IDR(slice1) IDR(slice2)
	push(nal(NALUTypeAUD, false, 0xF0))
	push(nal(NALUTypeSPS, true, 1))
	push(nal(NALUTypePPS, true, 2))
	push(nal(NALUTypeSEI, false, 3))
	push(nal(NALUTypeIDR, true, 4))
	push(nal(NALUTypeIDR, true, 5))
	// P AU: AUD P P
	push(nal(NALUTypeAUD, false, 0xF0))
	push(nal(NALUTypeNonIDR, false, 6))
	push(nal(NALUTypeNonIDR, false, 7))
	// Next AUD flushes the P AU
	push(nal(NALUTypeAUD, false, 0xF0))

	if len(got) != 2 {
		t.Fatalf("expected 2 access units, got %d", len(got))
	}
	if !got[0].IsKeyframe || got[1].IsKeyframe {
		t.Errorf("keyframe flags wrong: %v %v", got[0].IsKeyframe, got[1].IsKeyframe)
	}
	// AUD(6) + SPS(6) + PPS(6) + SEI(6) + IDR(6) + IDR(6) = 36 bytes
	if len(got[0].Data) != 36 {
		t.Errorf("keyframe AU size = %d, want 36", len(got[0].Data))
	}
	// AUD + 2 slices = 18 bytes
	if len(got[1].Data) != 18 {
		t.Errorf("P AU size = %d, want 18", len(got[1].Data))
	}
}

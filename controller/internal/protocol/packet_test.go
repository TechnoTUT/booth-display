package protocol

import (
	"bytes"
	"testing"
)

func TestPacketHeaderMarshalUnmarshal(t *testing.T) {
	original := PacketHeader{
		Magic:          MagicByte,
		Version:        VersionByte,
		PayloadType:    PayloadTypeH264,
		Flags:          FlagMarker | FlagKeyframe,
		SequenceNumber: 123456,
		Timestamp:      987654321,
		FragmentIndex:  2,
		FragmentTotal:  5,
	}

	buf := original.Marshal()
	if len(buf) != HeaderSize {
		t.Fatalf("expected header length %d, got %d", HeaderSize, len(buf))
	}

	parsed, err := UnmarshalHeader(buf)
	if err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if *parsed != original {
		t.Fatalf("parsed header mismatch. Got %+v, want %+v", *parsed, original)
	}
}

func TestPacketizeAndParse(t *testing.T) {
	dummyData := make([]byte, 3500)
	for i := range dummyData {
		dummyData[i] = byte(i % 256)
	}

	var seq uint32 = 100
	timestamp := uint32(50000)
	maxPayload := 1000

	packets := Packetize(dummyData, PayloadTypeH264, true, &seq, timestamp, maxPayload)

	// 3500 bytes with 1000 maxPayload => 4 packets (1000, 1000, 1000, 500)
	if len(packets) != 4 {
		t.Fatalf("expected 4 packets, got %d", len(packets))
	}
	if seq != 104 {
		t.Fatalf("expected seq 104, got %d", seq)
	}

	var reassembled bytes.Buffer
	for i, pkt := range packets {
		h, payload, err := ParsePacket(pkt)
		if err != nil {
			t.Fatalf("failed to parse packet %d: %v", i, err)
		}
		if h.FragmentIndex != uint16(i) {
			t.Errorf("expected fragment index %d, got %d", i, h.FragmentIndex)
		}
		if h.FragmentTotal != 4 {
			t.Errorf("expected fragment total 4, got %d", h.FragmentTotal)
		}
		if h.Timestamp != timestamp {
			t.Errorf("expected timestamp %d, got %d", timestamp, h.Timestamp)
		}
		if (h.Flags & FlagKeyframe) == 0 {
			t.Errorf("expected keyframe flag to be set")
		}
		if i == 3 {
			if (h.Flags & FlagMarker) == 0 {
				t.Errorf("expected marker flag on last packet")
			}
		} else {
			if (h.Flags & FlagMarker) != 0 {
				t.Errorf("unexpected marker flag on packet %d", i)
			}
		}
		reassembled.Write(payload)
	}

	if !bytes.Equal(reassembled.Bytes(), dummyData) {
		t.Fatalf("reassembled data did not match original data")
	}
}

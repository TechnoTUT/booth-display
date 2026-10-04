package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	MagicByte   byte = 0xBD
	VersionByte byte = 0x01

	HeaderSize = 16

	PayloadTypeH264 byte = 0x01
	PayloadTypeJPEG byte = 0x02
	PayloadTypeRAW  byte = 0x03

	FlagMarker   byte = 0x01 // Last packet of the frame
	FlagKeyframe byte = 0x02 // Contains SPS/PPS or IDR slice

	DefaultMaxPayloadSize = 1400 // Keep under standard 1500 Ethernet MTU (1400 + 16 header + 28 UDP/IP = 1444)
)

var (
	ErrPacketTooShort    = errors.New("packet data too short for header")
	ErrInvalidMagic      = errors.New("invalid magic byte")
	ErrUnsupportedVersion = errors.New("unsupported protocol version")
)

type PacketHeader struct {
	Magic          byte
	Version        byte
	PayloadType    byte
	Flags          byte
	SequenceNumber uint32
	Timestamp      uint32
	FragmentIndex  uint16
	FragmentTotal  uint16
}

// MarshalSerializes the header into a 16-byte slice.
func (h *PacketHeader) Marshal() []byte {
	buf := make([]byte, HeaderSize)
	buf[0] = h.Magic
	buf[1] = h.Version
	buf[2] = h.PayloadType
	buf[3] = h.Flags
	binary.BigEndian.PutUint32(buf[4:8], h.SequenceNumber)
	binary.BigEndian.PutUint32(buf[8:12], h.Timestamp)
	binary.BigEndian.PutUint16(buf[12:14], h.FragmentIndex)
	binary.BigEndian.PutUint16(buf[14:16], h.FragmentTotal)
	return buf
}

// UnmarshalHeader parses the 16-byte header.
func UnmarshalHeader(data []byte) (*PacketHeader, error) {
	if len(data) < HeaderSize {
		return nil, ErrPacketTooShort
	}
	if data[0] != MagicByte {
		return nil, fmt.Errorf("%w: got 0x%02X expected 0x%02X", ErrInvalidMagic, data[0], MagicByte)
	}
	if data[1] != VersionByte {
		return nil, fmt.Errorf("%w: got 0x%02X", ErrUnsupportedVersion, data[1])
	}

	h := &PacketHeader{
		Magic:          data[0],
		Version:        data[1],
		PayloadType:    data[2],
		Flags:          data[3],
		SequenceNumber: binary.BigEndian.Uint32(data[4:8]),
		Timestamp:      binary.BigEndian.Uint32(data[8:12]),
		FragmentIndex:  binary.BigEndian.Uint16(data[12:14]),
		FragmentTotal:  binary.BigEndian.Uint16(data[14:16]),
	}
	return h, nil
}

// ParsePacket splits raw UDP packet into header and payload.
func ParsePacket(data []byte) (*PacketHeader, []byte, error) {
	h, err := UnmarshalHeader(data)
	if err != nil {
		return nil, nil, err
	}
	return h, data[HeaderSize:], nil
}

// Packetize splits frame/NAL data into MTU-safe UDP packets.
func Packetize(data []byte, payloadType byte, isKeyframe bool, seq *uint32, timestamp uint32, maxPayload int) [][]byte {
	if maxPayload <= 0 {
		maxPayload = DefaultMaxPayloadSize
	}

	totalLen := len(data)
	if totalLen == 0 {
		return nil
	}

	numFragments := (totalLen + maxPayload - 1) / maxPayload
	packets := make([][]byte, 0, numFragments)

	for i := 0; i < numFragments; i++ {
		start := i * maxPayload
		end := start + maxPayload
		if end > totalLen {
			end = totalLen
		}

		chunk := data[start:end]
		var flags byte
		if isKeyframe {
			flags |= FlagKeyframe
		}
		if i == numFragments-1 {
			flags |= FlagMarker
		}

		h := PacketHeader{
			Magic:          MagicByte,
			Version:        VersionByte,
			PayloadType:    payloadType,
			Flags:          flags,
			SequenceNumber: *seq,
			Timestamp:      timestamp,
			FragmentIndex:  uint16(i),
			FragmentTotal:  uint16(numFragments),
		}
		*seq++

		pkt := make([]byte, HeaderSize+len(chunk))
		copy(pkt[:HeaderSize], h.Marshal())
		copy(pkt[HeaderSize:], chunk)
		packets = append(packets, pkt)
	}

	return packets
}

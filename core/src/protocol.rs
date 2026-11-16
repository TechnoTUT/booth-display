//! Video packet format sent to the displays over UDP (see
//! docs/streaming-protocol.md): a 16-byte big-endian header followed by a
//! fragment of one H.264 Access Unit.

pub const MAGIC: u8 = 0xBD;
pub const VERSION: u8 = 0x01;
pub const HEADER_SIZE: usize = 16;

pub const PAYLOAD_H264: u8 = 0x01;
#[allow(dead_code)]
pub const PAYLOAD_JPEG: u8 = 0x02;
#[allow(dead_code)]
pub const PAYLOAD_RAW: u8 = 0x03;

/// Last packet of the frame.
pub const FLAG_MARKER: u8 = 0x01;
/// The frame contains SPS/PPS or an IDR slice.
pub const FLAG_KEYFRAME: u8 = 0x02;

/// Keeps a packet under the 1500-byte Ethernet MTU (1400 + 16 header + 28 UDP/IP).
pub const MAX_PAYLOAD: usize = 1400;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct PacketHeader {
    pub payload_type: u8,
    pub flags: u8,
    pub sequence: u32,
    pub timestamp: u32,
    pub fragment_index: u16,
    pub fragment_total: u16,
}

impl PacketHeader {
    pub fn write(&self, out: &mut [u8]) {
        out[0] = MAGIC;
        out[1] = VERSION;
        out[2] = self.payload_type;
        out[3] = self.flags;
        out[4..8].copy_from_slice(&self.sequence.to_be_bytes());
        out[8..12].copy_from_slice(&self.timestamp.to_be_bytes());
        out[12..14].copy_from_slice(&self.fragment_index.to_be_bytes());
        out[14..16].copy_from_slice(&self.fragment_total.to_be_bytes());
    }

    #[cfg(test)]
    pub fn parse(data: &[u8]) -> Option<(PacketHeader, &[u8])> {
        if data.len() < HEADER_SIZE || data[0] != MAGIC || data[1] != VERSION {
            return None;
        }
        let u32_at = |i: usize| u32::from_be_bytes(data[i..i + 4].try_into().unwrap());
        let u16_at = |i: usize| u16::from_be_bytes(data[i..i + 2].try_into().unwrap());
        Some((
            PacketHeader {
                payload_type: data[2],
                flags: data[3],
                sequence: u32_at(4),
                timestamp: u32_at(8),
                fragment_index: u16_at(12),
                fragment_total: u16_at(14),
            },
            &data[HEADER_SIZE..],
        ))
    }
}

/// Number of packets `len` bytes are split into.
pub fn packet_count(len: usize) -> usize {
    len.div_ceil(MAX_PAYLOAD)
}

/// Splits one frame into MTU-safe packets, numbering them from `*seq`.
pub fn packetize(data: &[u8], payload_type: u8, keyframe: bool, seq: &mut u32, timestamp: u32) -> Vec<Vec<u8>> {
    let total = packet_count(data.len());
    data.chunks(MAX_PAYLOAD)
        .enumerate()
        .map(|(i, chunk)| {
            let mut flags = 0;
            if keyframe {
                flags |= FLAG_KEYFRAME;
            }
            if i == total - 1 {
                flags |= FLAG_MARKER;
            }
            let mut pkt = vec![0u8; HEADER_SIZE + chunk.len()];
            PacketHeader {
                payload_type,
                flags,
                sequence: *seq,
                timestamp,
                fragment_index: i as u16,
                fragment_total: total as u16,
            }
            .write(&mut pkt);
            pkt[HEADER_SIZE..].copy_from_slice(chunk);
            *seq = seq.wrapping_add(1);
            pkt
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn header_round_trip() {
        let h = PacketHeader {
            payload_type: PAYLOAD_H264,
            flags: FLAG_MARKER | FLAG_KEYFRAME,
            sequence: 0xDEADBEEF,
            timestamp: 123456,
            fragment_index: 3,
            fragment_total: 7,
        };
        let mut buf = [0u8; HEADER_SIZE];
        h.write(&mut buf);
        assert_eq!(buf[0], MAGIC);
        let (got, payload) = PacketHeader::parse(&buf).unwrap();
        assert_eq!(got, h);
        assert!(payload.is_empty());
    }

    #[test]
    fn packetize_splits_at_mtu_and_reassembles() {
        let data: Vec<u8> = (0..(MAX_PAYLOAD * 2 + 100)).map(|i| i as u8).collect();
        let mut seq = 10;
        let pkts = packetize(&data, PAYLOAD_H264, true, &mut seq, 42);
        assert_eq!(pkts.len(), 3);
        assert_eq!(seq, 13);
        let mut joined = Vec::new();
        for (i, p) in pkts.iter().enumerate() {
            let (h, payload) = PacketHeader::parse(p).unwrap();
            assert_eq!(h.fragment_index as usize, i);
            assert_eq!(h.fragment_total, 3);
            assert_eq!(h.sequence, 10 + i as u32);
            assert_eq!(h.timestamp, 42);
            assert_ne!(h.flags & FLAG_KEYFRAME, 0);
            assert_eq!(h.flags & FLAG_MARKER != 0, i == 2);
            assert!(payload.len() <= MAX_PAYLOAD);
            joined.extend_from_slice(payload);
        }
        assert_eq!(joined, data);
    }

    #[test]
    fn packetize_empty() {
        let mut seq = 0;
        assert!(packetize(&[], PAYLOAD_H264, false, &mut seq, 0).is_empty());
        assert_eq!(seq, 0);
    }
}

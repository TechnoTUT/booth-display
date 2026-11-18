# Streaming Protocol & Low Latency Design

This document explains how the streaming protocol was chosen and how packets are laid out, so as to keep latency low and the picture smooth while staying within the bandwidth of a 100BASE-TX network.

---

## 1. Bandwidth vs. latency trade-offs

### (1) Data rate per display (resolution: 1920 × 540)

| Format | Data per frame | Bandwidth at 30 fps | Fits 100BASE-TX (~90 Mbps effective)? |
| :--- | :--- | :--- | :--- |
| Uncompressed (RGB24) | 1920 × 540 × 3 = 3.11 MB | 746.5 Mbps | No (far over the limit) |
| Uncompressed (YUV420p) | 1920 × 540 × 1.5 = 1.55 MB | 373.2 Mbps | No (far over the limit) |
| Light compression: MJPEG (quality 80) | about 80 KB – 120 KB | 19.2 – 28.8 Mbps | Yes |
| H.264, VA-API (default encoder) | 50 KB on average (CBR 12 Mbps, 1 Mbit HRD buffer) | 12 Mbps | Yes |
| H.264, libx264 (software fallback) | about 17 KB on average (4 Mbps, 400 kbit VBV buffer) | 4 Mbps | Yes, with plenty of headroom |

### (2) Decision
- Uncompressed (RAW):
  - Rejected, because it far exceeds the physical limit of a 100BASE-TX LAN (100 Mbps).
- Chosen: low-latency H.264:
  - Constrained Baseline, one slice per frame, no B-frames, no frame reordering, so the decoder can output every frame as soon as its input is complete.
  - Encoded in-process on the controller (VA-API, or libx264 ultrafast/zerolatency). One access unit comes out per frame in; see [architecture.md](architecture.md) for measured encode latency.
  - Decoded with MediaCodec on the displays (Android 7.1, Cortex-A17), rendered straight onto a SurfaceView.
- Role of MJPEG:
  - MJPEG is used only for the Web GUI preview (`/api/preview/mjpeg`). Only H.264 is sent to the displays over UDP.
  - The packet PayloadType `0x02 (JPEG)` is reserved and not used by the current UDP output.

---

## 2. Packet protocol (UDP streaming), version 2

A lightweight UDP-based protocol is used to avoid protocol overhead and the latency of TCP retransmission (head-of-line blocking). Lost packets are not retransmitted; instead the display asks for a keyframe and the controller encodes the next frame as an IDR (see (3)).

### (1) Common header

Every packet, in either direction, starts with the same fixed 16-byte header:

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Magic (0xBD) | Version (0x02)| PayloadType(1)|     Flags     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Sequence Number                         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                   Timestamp (32-bit, microseconds)            |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|          Fragment Index       |        Fragment Total         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Payload                                 |
|                             ....                              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

- Magic: 0xBD (booth-display)
- Version: 0x02. A receiver drops packets of any other version; the display shows a version mismatch instead of a picture.
- All multi-byte fields are big-endian. Offsets: Magic=0, Version=1, PayloadType=2, Flags=3, Sequence Number=4–7, Timestamp=8–11, Fragment Index=12–13, Fragment Total=14–15.
- Timestamps are microseconds on the sender's own monotonic clock, truncated to 32 bits (they wrap every 71.6 minutes). Only differences between timestamps of the same clock, or values converted with the clock offset from PING/PONG, are meaningful. The controller uses one clock for all displays.

PayloadType values:

| Value | Name | Direction | Purpose |
| :--- | :--- | :--- | :--- |
| 0x01 | H264 | controller → display | Fragment of one H.264 access unit |
| 0x02 | JPEG | — | Reserved, not used |
| 0x03 | RAW | — | Reserved, not used |
| 0x10 | KEYFRAME_REQUEST | display → controller | Ask for an IDR after a loss |
| 0x11 | PING | display → controller | Clock offset and round-trip time |
| 0x12 | PONG | controller → display | Answer to a PING |

### (2) Video packets (PayloadType 0x01)

- Payload: one H.264 access unit in Annex-B per frame: an AUD, SPS and PPS in front of every IDR, and the single slice. A display can hand an access unit to the decoder as it is.
- Fragmentation: the access unit is split into fragments of at most 1400 bytes. **Every fragment except the last carries exactly 1400 bytes**, so a receiver can place fragment *i* at offset *i* × 1400 as it arrives. Fragment Index counts from 0; Fragment Total is the number of fragments (at least 1).
- Sequence Number: one counter per display, incremented by one for every video packet. The fragments of a frame have consecutive numbers, so fragment *i* of a frame whose first packet has number *S* has number *S* + *i*, and the next frame starts at *S* + Fragment Total. A gap means lost packets.
- Timestamp: the time the canvas frame reached the encoder, the same in every fragment of the frame.
- Flags (OR-ed):
  - 0x01 (Marker): the last fragment of the frame
  - 0x02 (Keyframe): the frame is an IDR (and carries SPS/PPS)
- Size: 1400 (payload) + 16 (header) + 28 (IPv4 20 + UDP 8) = 1444 bytes, which fits within the standard 1500-byte Ethernet MTU, so no IP fragmentation occurs.

### (3) Control messages (PayloadType 0x10–0x12)

The display sends control messages from the UDP socket it receives video on (the configured display port) to the source address and port of the video packets. The controller's socket for that display only accepts packets from that address and port. Control messages are a header without fragmentation: Flags = 0, Fragment Index = 0, Fragment Total = 1.

- **KEYFRAME_REQUEST (0x10)**, display → controller. No payload.
  - Sequence Number: the last video sequence number the display received. Timestamp: the display's clock.
  - The controller encodes its next frame as an IDR. It forces at most one IDR per 250 ms per display and ignores requests inside that window, so a burst of losses cannot turn into a stream of IDRs.
  - The display repeats the request every 250 ms until a keyframe arrives.
- **PING (0x11)**, display → controller. No payload.
  - Sequence Number: a counter of the display's own. Timestamp: the display's clock when sent (*t0*).
  - The display sends one about once a second.
- **PONG (0x12)**, controller → display. Payload: 4 bytes, the PING's timestamp *t0* (big-endian).
  - Sequence Number: copied from the PING. Timestamp: the controller's clock when answered (*t1*).
  - With *t2* the display's clock on arrival: round-trip time = *t2* − *t0*, and clock offset (controller minus display) = *t1* − (*t0* + *t2*) / 2. The display keeps the offset of the PONG with the shortest round-trip time among recent ones.
  - A frame with timestamp *T* that the display finishes decoding at its time *t* took *t* + offset − *T* from reaching the encoder.

### (4) Rules for the display

- Reassembly: a frame is identified by the sequence number of its first fragment (Sequence Number − Fragment Index). Fragments of the current frame may arrive in any order; a fragment of an older frame is ignored.
- Loss: when a frame cannot be completed (a later frame starts first) or the sequence numbers between two frames have a gap, a reference picture is missing. The display then skips frames up to the next keyframe and sends KEYFRAME_REQUEST. Without the request it would wait for the next regular IDR (once a second).
- Controller restart: a restarted controller numbers its packets from 0 again, timestamps them on a clock with a new origin, and sends from a new source port. When video arrives from a different source address or port than before, or a frame starts more than 1024 packets before the expected sequence number, the display starts over: it discards the frame being assembled and its clock offset, waits for a keyframe, sends KEYFRAME_REQUEST and PING right away. Otherwise it would take the new stream for late fragments until its numbers passed the old ones.
- No backlog: frames go to the decoder as soon as they are complete. When the decoder falls a few frames behind, the waiting frames are discarded and the display waits for (and asks for) a keyframe, instead of showing the picture later and later.

### (5) Rules for the controller

- A frame is queued whole or dropped whole. After a drop, P-frames are skipped and the next frame is encoded as an IDR (subject to the 250 ms limit).
- Pacing: each display's packets leave at no more than `pipeline.send_rate_mbps` (default 80 Mbps; -1 turns pacing off), with a burst allowance of 16 packets. A 1 Gbit/s controller would otherwise put a whole IDR (up to about 90 packets) on the wire in about 1 ms, which a switch has to buffer in front of the display's 100 Mbit/s port. At 80 Mbps a frame takes only slightly longer than the display's own link needs anyway.

### Changes from version 1

- Version 0x02. The header layout is unchanged.
- Timestamps are microseconds on a clock shared by all displays (version 1: milliseconds since each encoder started).
- New control messages: KEYFRAME_REQUEST, PING and PONG.
- Fragments other than the last are exactly 1400 bytes (version 1 senders already did this).
- The controller paces its packets.

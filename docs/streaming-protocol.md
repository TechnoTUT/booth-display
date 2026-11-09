# Streaming Protocol & Low Latency Design

This document explains how the streaming protocol was chosen and how packets are laid out, so as to keep encoding latency low while staying within the bandwidth of a 100BASE-TX network.

---

## 1. Bandwidth vs. latency trade-offs

### (1) Data rate per display (resolution: 1920 × 540)

| Format | Data per frame | Bandwidth at 30 fps | Bandwidth at 60 fps | Fits 100BASE-TX (~90 Mbps effective)? |
| :--- | :--- | :--- | :--- | :--- |
| Uncompressed (RGB24) | 1920 × 540 × 3 = 3.11 MB | 746.5 Mbps | 1,493 Mbps | No (far over the limit) |
| Uncompressed (YUV420p) | 1920 × 540 × 1.5 = 1.55 MB | 373.2 Mbps | 746.5 Mbps | No (far over the limit) |
| Light compression: MJPEG (quality 80) | about 80 KB – 120 KB | 19.2 – 28.8 Mbps | 38.4 – 57.6 Mbps | Yes |
| Low-latency H.264 (Baseline, zerolatency) | about 17 KB on average (default 4000 kbps at 30 fps) | 4.0 Mbps (default; varies with the scene) | 8.0 Mbps (rough figure for the same quality) | Yes, with plenty of headroom |

### (2) Decision
- Uncompressed (RAW):
  - Rejected, because it far exceeds the physical limit of a 100BASE-TX LAN (100 Mbps).
- Chosen: low-latency H.264 (zero-latency settings):
  - Encode latency on the sender: about 5–10 ms with x264 (preset=ultrafast, tune=zerolatency, no B-frames).
  - Decode latency on the receiver: about 3–5 ms with MediaCodec on Android 7.1 (Cortex-A17 VPU).
  - Bandwidth: 4 Mbps per display by default (x264 `-b:v` / `-maxrate` = 4000k, varying with the scene). This is low enough to keep the risk of congestion and packet loss minimal.
  - Low CPU load and heat on the Android devices, and stable even with 1 GB RAM.
- Role of MJPEG:
  - MJPEG is used only for the Web GUI preview (`/api/preview/mjpeg`). Only H.264 is sent to the displays over UDP.
  - The packet PayloadType `0x02 (JPEG)` is reserved and not used by the current UDP output.

---

## 2. Packet protocol (UDP streaming)

A lightweight UDP-based protocol is used to avoid protocol overhead and the latency of TCP retransmission (head-of-line blocking).

### (1) Packet format (custom lightweight header)

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Magic (0xBD) | Version (0x01)| PayloadType(1)| Flags (Mark)  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Sequence Number                         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Timestamp (32-bit ms)                    |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|          Fragment Index       |        Fragment Total         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Payload (NAL Unit / JPEG data)          |
|                             ....                              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

- Magic: 0xBD (booth-display)
- Version: 0x01
- PayloadType:
  - 0x01 = H.264 Access Unit (Annex-B): all NAL units of one frame (AUD, SPS/PPS/SEI when needed, and slices) combined into one unit. The encoder outputs Baseline with `aud=1:slices=1:repeat-headers=1`, and the controller assembles Access Units using the AUD as the boundary before sending. The receiver (MediaCodec) can pass them straight into the decoder's input buffer.
  - 0x02 = JPEG
  - 0x03 = RAW (defined only; a reserved value not used by the current pipeline)
- Flags (bit flags, combined with OR):
  - 0x01 (Marker): the last packet of a frame
  - 0x02 (Keyframe): the frame contains SPS/PPS or an IDR slice
- Sequence Number: detects lost and reordered packets (32-bit counter)
- Timestamp: presentation timestamp from the controller's clock (milliseconds)
- Fragment Index / Total: index for splitting one frame (NAL) at the payload limit and reassembling it (16 bits each)

#### Byte order and size
- The header is a fixed 16 bytes. All multi-byte fields (Sequence Number / Timestamp / Fragment Index / Fragment Total) are in network byte order (big-endian).
  - Offsets: Magic=0, Version=1, PayloadType=2, Flags=3, Sequence Number=4–7, Timestamp=8–11, Fragment Index=12–13, Fragment Total=14–15
- The maximum payload is 1400 bytes (`DefaultMaxPayloadSize`).
  - 1400 (payload) + 16 (custom header) + 28 (IPv4 20 + UDP 8) = 1444 bytes, which fits within the standard 1500-byte Ethernet MTU, so no IP fragmentation occurs.

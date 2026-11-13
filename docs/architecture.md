# Architecture Design

This document describes the overall architecture of booth-display, the tiled booth display system, and how the controller is implemented.

---

## 1. System architecture

The system consists of the following main components.

```mermaid
flowchart TB
    subgraph Controller["Display controller (Go / Linux)"]
        WebGUI["Web GUI (HTML/JS)"]
        APIServer["HTTP / WebSocket API"]
        ConfigMgr["Config manager (config.yaml)"]
        
        subgraph MediaSources["Input sources"]
            TestPattern["Built-in test pattern"]
            VideoFile["Local video file (MP4)"]
            NDISource["NDI network video (libndi via cgo)"]
        end

        CanvasEngine["Virtual canvas &<br/>crop engine"]
        EncoderPool["Unified multi-output encoder (MultiEncoder)<br/>(Intel VA-API / x264 zerolatency)"]
        StreamServer["Streaming server<br/>(UDP Server)"]
        PreviewHub["MJPEG preview hub<br/>(/api/preview/mjpeg)"]
        
        WebGUI <--> APIServer
        APIServer --> ConfigMgr
        MediaSources --> CanvasEngine
        CanvasEngine --> EncoderPool
        CanvasEngine --> PreviewHub
        EncoderPool --> StreamServer
    end

    subgraph Network["LAN (100BASE-TX)"]
        Switch["L2 switch"]
    end

    subgraph Client1["Android Client #1 (1920x540)"]
        Receiver1["UDP Receiver"] --> Codec1["MediaCodec (HW)"] --> Surface1["SurfaceView"]
    end

    subgraph Client2["Android Client #2 (1920x540)"]
        Receiver2["UDP Receiver"] --> Codec2["MediaCodec (HW)"] --> Surface2["SurfaceView"]
    end

    subgraph Client3["Android Client #3 (1920x540)"]
        Receiver3["UDP Receiver"] --> Codec3["MediaCodec (HW)"] --> Surface3["SurfaceView"]
    end

    StreamServer --> Switch
    Switch --> Client1
    Switch --> Client2
    Switch --> Client3
    WebGUI <--> PreviewHub
```

---

## 2. Display controller internals (`controller/`)

### (1) Pipeline
- Input sources:
  - Built-in test pattern (generated natively in Go)
  - Local video file (MP4 / H.264)
  - NDI network video (received in-process through libndi, scaled onto the canvas)
- Virtual canvas mixer (`CanvasMixer`):
  - Composites frames at the canvas rate (30 fps), driven by NDI frame arrival when NDI is active, and handles transitions (cut, crossfade, fade to black).
  - A buffer pool (`sync.Pool`) with reference counting avoids a memory copy per subscriber (zero-copy fan-out).
- Single-process unified encoding (`MultiEncoder`):
  - One FFmpeg process receives a single rawvideo input of the whole canvas (5760x540).
  - `split` and `crop` filters cut it into one region per display.
  - **Hardware acceleration (Intel VA-API)**: when `/dev/dri/renderD128` is available, the canvas is converted to `nv12` once, split, and each crop goes through `hwupload` to `h264_vaapi` (Constrained Baseline, AUD enabled, no B-frames) for low-latency encoding at very low CPU load.
  - **Software fallback**: if VA-API is missing or fails to initialize, it automatically falls back to `libx264` (ultrafast, zerolatency).
  - Each display's output is muxed as FLV and passed through an extra pipe descriptor (`pipe:3`, `pipe:4`, ...) to its own goroutine. FLV carries the size of every frame, so a frame is forwarded as soon as it is complete.
- Packetization and UDP output:
  - Converts each FLV-framed Access Unit (frame) to Annex-B H.264 and splits it into UDP packets with a 16-byte custom header.
  - Sends them asynchronously over UDP to each display's IP and port.

### (2) Web GUI and API
- Web GUI (`controller/frontend/`):
  - Built with Vue 3 (Composition API / `<script setup>`), Vite and Tailwind CSS.
  - Follows the TechnoTUT design system, with dark and light themes.
  - The Vite build output (`controller/web/dist`) is embedded in the single binary through Go's `embed.FS`.
  - Monitors display connection status (real-time FPS, bitrate and drop rate over WebSocket).
  - Shows the virtual canvas topology, a live MJPEG preview, and crop-region overlays.
  - Lets you edit and save each display's IP, port, resolution, crop position and bezel width from the settings table.
- WebSocket control channel:
  - Two-way communication with clients and the Web GUI, with telemetry sent every second.

### (3) NDI input
- `controller/internal/ndi`: calls the NDI SDK (`libndi`, installed with `make setup-ndi`) through cgo. The library is loaded with `dlopen` at runtime (see the README for the search order), so the controller builds and runs without it; only NDI input is unavailable then. The receiver handling follows [moip-toolkit](https://github.com/TechnoTUT/moip-toolkit):
  - One finder lives for the whole process, so it already knows the senders when a source is looked up. Only right after it is created does discovery wait for the list to settle.
  - The receiver asks for video only (UYVY, full bandwidth, progressive). Audio and metadata are not requested, so the SDK discards them instead of queueing them. UYVY is what the SDK decodes to, so it does not convert whole frames to BGRX (sources with alpha arrive as BGRA).
  - Capture waits inside the SDK for the next frame and returns the moment it arrives; frames that queued up meanwhile are released at once and only the newest is used. (moip-toolkit polls a frame synchronizer instead; measured here, waiting for the frame kept p90 latency ~3 ms lower.)
  - Receivers are reused: switching sources reconnects a pooled receiver instead of creating a new one. A receiver whose session died is never torn down on the receive goroutine (that blocks inside the SDK for 10 s or more); it is destroyed in the background, followed by `malloc_trim`.
- `NDIDistributor` (`controller/internal/pipeline/ndi.go`) forwards each frame the moment it arrives: it converts only the cropped rows from UYVY, center-crops and bilinearly scales them onto the canvas (`scale.go`, all cores, no SIMD dependency) and signals the mixer. A source faster than the canvas rate is thinned to about the canvas rate.
  - It recovers by itself: a missing source is looked for until it appears, a source silent for 2 s (doubling up to 30 s) while NDI still reports a connection gets a fresh receiver, and a sender that comes back after dropping off the source list (or under another address) is connected to at once. The last frame stays on the canvas meanwhile.
- The mixer renders on NDI frame arrival; its ticker only paces generator sources and stands in when NDI frames stop, so a 29.97 fps source is not held back by a 30 Hz clock.

---

## 3. Android client internals (`android-client/`)

- Target: Android 7.1 (API 25), ARM Cortex-A17 quad-core, 1 GB RAM
- Responsibilities:
  - Receive UDP packets and reassemble fragments
  - Hardware H.264 decoding with MediaCodec (using the Cortex-A17 VPU)
  - Full-screen rendering to a SurfaceView (immersive mode)

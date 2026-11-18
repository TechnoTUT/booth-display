# Architecture Design

This document describes the overall architecture of booth-display, the tiled booth display system, and how the controller is implemented.

---

## 1. System architecture

The display controller is two processes: the Go backend (`display-controller`) serves the Web GUI and API and runs the Rust media core (`booth-core`) as a child process. The media core does all video work.

```mermaid
flowchart TB
    subgraph Controller["Display controller (Linux)"]
        subgraph Backend["Backend: display-controller (Go)"]
            WebGUI["Web GUI (Vue 3, embedded)"]
            APIServer["HTTP / WebSocket API"]
            ConfigMgr["Config manager (config.yaml)"]
            CoreClient["Core client & supervisor<br/>(internal/core)"]
            PreviewHub["MJPEG preview hub<br/>(/api/preview/mjpeg)"]
        end

        subgraph Core["Media core: booth-core (Rust)"]
            subgraph MediaSources["Input sources"]
                TestPattern["Test pattern / logo"]
                VideoFile["Local video file / testsrc (ffmpeg)"]
                NDISource["NDI (moip-engine ndi crate, libndi)"]
            end
            CanvasMixer["Canvas mixer & transitions"]
            Encoder["Per-display H.264 encoders<br/>(VA-API / libx264, in-process)"]
            Senders["UDP senders (one per display)"]
            Preview["JPEG preview"]
        end

        WebGUI <--> APIServer
        APIServer --> ConfigMgr
        APIServer --> CoreClient
        CoreClient -- "JSON commands (stdin)" --> Core
        Core -- "events + preview JPEGs (stdout)" --> CoreClient
        CoreClient --> PreviewHub
        MediaSources --> CanvasMixer
        CanvasMixer --> Encoder
        CanvasMixer --> Preview
        Encoder --> Senders
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

    Senders --> Switch
    Switch --> Client1
    Switch --> Client2
    Switch --> Client3
    WebGUI <--> PreviewHub
```

---

## 2. Display controller internals

### (1) Backend (`controller/`, Go)
- HTTP / WebSocket API and the Web GUI (`controller/frontend/`: Vue 3 Composition API, Vite, Tailwind CSS, TechnoTUT design system with dark and light themes). The Vite build output (`controller/web/dist`) is embedded through Go's `embed.FS`.
- `config.yaml` loading and saving (`internal/config`).
- `internal/core` runs `booth-core engine` and talks to it:
  - **stdin → core**: one JSON command per line with an `id`: `configure` (canvas, displays, encoder), `start`, `stop`, `switch` (source, transition, duration), `discover` (NDI sources), `quit`. Closing stdin also makes the core stop and exit.
  - **core → stdout**: framed messages `[kind u8][flags u8][len u32 LE][payload]` (the framing of moip-engine's preview output): kind 0 is a JSON event (`ready`, `status`, `reply`), kind 2 a preview JPEG.
  - Every command gets a `reply` event; the core sends a `status` event (pipeline, scene, per-display metrics) just before each reply and every 500 ms. The API and the WebSocket telemetry (once a second) serve the latest status.
  - If the core exits unexpectedly, it is restarted after a second and given the last configuration and playback state again.
- The controller needs no cgo and does not touch NDI or video itself.

### (2) Media core (`core/`, Rust)
- Built on [moip-engine](https://github.com/TechnoTUT/moip-engine), included as a git submodule (`moip-engine/`). booth-core uses its `ndi` crate (runtime-loaded libndi bindings, `find_source`, `Receiver::capture_newest_video`); generic NDI functionality belongs there rather than in booth-core.
- Input sources:
  - Built-in test pattern and logo crawl (generated in Rust)
  - Local video file and ffmpeg `testsrc`, read from an ffmpeg child as raw BGRX
  - NDI network video (received in-process, scaled onto the canvas)
- Canvas mixer (`mixer.rs`):
  - Renders at the canvas rate (30 fps), driven by NDI frame arrival when NDI is active, and handles transitions (cut, crossfade, dip to black) without restarting the encoder. Blends run in 16-bit lanes so they vectorize with SSE2.
  - Frames come from a pool and are shared by reference count between subscribers (no per-subscriber copy); a subscriber that falls two frames behind loses frames instead of delaying the others.
- Encoding (`encoder.rs`) picks the encoder from `pipeline.encoder` (`auto`/`vaapi`, `vaapi-ffmpeg`, `software`) and falls back to libx264 when VA-API cannot start. The status reports which one runs (`pipeline.encoder`). Both regular encoders run in-process on the same per-display threads (`native.rs`): one thread per display takes each canvas frame from the mixer, converts its crop to NV12 (BT.601 limited, `nv12.rs`) straight into the encoder's input and encodes it synchronously. There is no ffmpeg process and no pipe copy of the 12 MB canvas, and the displays encode in parallel.
- **In-process VA-API** (`vaenc.rs`, `vaapi/`), the default when `/dev/dri/renderD128` exists:
  - libva (`libva.so.2`, `libva-drm.so.2`) is loaded at run time with hand-written bindings, so the build needs no libva headers; the struct layouts are tested against the libva 2.22 headers (and the installed ones when `libva-devel` is present).
  - One VA display and encoder per display; the crop is converted straight into the VA input surface.
  - Constrained Baseline, one slice per frame, IDR at the start of every one-second GOP, P-frames with one reference, CBR 12 Mbps with a 1 Mbit HRD buffer, AUD on every access unit. The driver writes slice headers; SPS and PPS are written by booth-core (`vaapi/headers.rs`) and put in front of every IDR, because the drivers emit none without packed headers. The SPS signals no frame reordering, so decoders output each frame at once.
  - Drivers: iHD (Broadwell and newer) and i965. Measured, 3 x 1920x540 at 30 fps, frame in to access unit out: i5-6500 iHD 10.2 ms median at 36% of one core, i965 8.9 ms at 30% (the ffmpeg h264_vaapi path took 18-20 ms at about 66%); i5-2400S (Sandy Bridge) i965 17.1 ms at 51%. Unpaced on the i5-2400S, 1920x540: 109 fps on one display, 96 fps per display with three in parallel at 90% of one core. Ivy Bridge (the i5-3470 target) has not been tested yet.
  - **VT-d**: on the Sandy Bridge machine, with VT-d (IOMMU) active for the integrated GPU, encoding three displays at once froze the whole machine within seconds (no kernel log, not even the NMI watchdog). With VT-d disabled in the BIOS it ran cleanly. Disable VT-d, or boot with `intel_iommu=igfx_off`, on the controller.
- **In-process libx264** (`x264enc.rs`, `x264/`), the software encoder and the fallback:
  - libx264 is loaded at run time (`libx264.so.<build>`, looking up the versioned `x264_encoder_open_<build>`), so the build needs neither the library nor its headers, and booth-core does not ship this GPL library. `x264_param_t` is only touched through its stable leading fields; every other setting goes through `x264_param_parse` by name. The layouts are tested against the installed `x264.h` when present.
  - ultrafast / zerolatency, Baseline, one thread and one slice per display, no lookahead (one access unit out per frame in), IDR at the start of every one-second GOP, AUD and in-band SPS/PPS, 4 Mbps with a 400 kbit VBV buffer.
  - Measured on an i5-2400S, 3 x 1920x540 at 30 fps: 17.2 ms median, 18.0 ms p90, 152% of one core including the NV12 conversion. The previous ffmpeg libx264 process took 24.8 ms median, 27.4 ms p90 at 165% (not counting the pipe writes). Unpaced, 1920x540: 96 fps on one display, 79 fps per display with three in parallel at 299% of one core.
- **ffmpeg h264_vaapi** (`vaapi-ffmpeg`, kept for comparison): one process for all displays:
  - One ffmpeg process receives the whole canvas (5760x540 BGRX) once, converts it to `nv12`, and `split` and `crop` filters cut it into one region per display; each crop goes through `hwupload` to `h264_vaapi` (Constrained Baseline, AUD, no B-frames, every GOP starts with an IDR). If ffmpeg exits within the 2 s startup probe, the in-process libx264 encoder takes over.
  - Each display's output is muxed as FLV and passed through an extra pipe (`pipe:3`, `pipe:4`, ...). FLV carries the size of every frame, so an access unit is forwarded as soon as it is complete (`flv.rs`).
  - On the Sandy Bridge machine (i965) its output does not decode cleanly.
- Packetization and UDP output (`sender.rs`, `protocol.rs`, protocol version 2, see [streaming-protocol.md](streaming-protocol.md)): each access unit is split into packets with the 16-byte header and queued for a send thread, which paces them at `pipeline.send_rate_mbps` (default 80 Mbps per display). A frame is queued whole or dropped whole, and after a drop P-frames are skipped until the next keyframe.
  - Pacing on the device test (one display, a noisy hard-to-encode clip, 30 and 60 fps): 80 Mbps and pacing off gave the same latency and no loss. The dev box's own link is 100 Mbit/s, though, so that test could not show the case pacing is for: a 1 Gbit/s controller bursting into a display's 100 Mbit/s port. The default stays at 80 Mbps until that is measured on the production network.
  - Keyframe requests: a control thread per display reads the display's messages on the same socket. A KEYFRAME_REQUEST (or a frame dropped by the queue) makes the encode thread encode its next frame as an IDR, at most once per 250 ms. The in-process encoders restart their GOP there; the `vaapi-ffmpeg` encoder cannot be asked and waits for its next GOP.
  - PING is answered with a PONG on the controller clock (microseconds, shared by all displays), the clock frame timestamps use, so a display can measure latency.
- Preview (`preview.rs`): every canvas frame is thinned to 15 fps, downscaled to 1280 px wide and JPEG-encoded in-process.

### (3) NDI input (`core/src/ndi_input.rs`)
- One finder lives for the whole process, so it already knows the senders when a source is looked up. Only right after it is created does discovery wait for the list to settle.
- The receiver asks for video only (UYVY, full bandwidth, progressive); audio and metadata are not requested, so the SDK discards them. Sources with alpha arrive as BGRA.
- Capture waits inside the SDK for the next frame and returns the moment it arrives; frames that queued up meanwhile are released at once and only the newest is used.
- Receivers are reused: switching sources reconnects a pooled receiver. A receiver whose session died is never torn down on the receive thread (that blocks inside the SDK for 10 s or more); it is destroyed in the background, followed by `malloc_trim`.
- Each frame is converted (only the cropped UYVY rows), center-cropped and bilinearly scaled onto the canvas on all cores (`scale.rs`, no SIMD dependency) and the mixer is signalled. A source faster than the canvas rate is thinned to about the canvas rate.
- It recovers by itself: a missing source is looked for until it appears, a source silent for 2 s (doubling up to 30 s) while NDI still reports a connection gets a fresh receiver, and a sender that comes back after dropping off the source list (or under another address) is connected to at once. The last frame stays on the canvas meanwhile.

---

## 3. Android client internals (`android-client/`)

- Target: Android 7.1 (API 25), ARM Cortex-A17 quad-core, 1 GB RAM
- Responsibilities:
  - Receive UDP packets and reassemble fragments
  - Hardware H.264 decoding with MediaCodec (using the Cortex-A17 VPU)
  - Full-screen rendering to a SurfaceView (immersive mode)
- Receive thread (`receiver/UdpStreamReceiver.kt`, protocol version 2):
  - One reused header object and a pool of eight 1 MB frame buffers (`protocol/Frame.kt`): each fragment is copied straight to its place in its frame (`protocol/FrameAssembler.kt`), so the receive path allocates nothing per packet or frame. On the device test the largest frame was 91 KB (keyframes up to 88 KB, a noisy clip at 30 fps), so 1 MB per buffer leaves more than ten times the room, and eight buffers were never all in use.
  - Loss (an incomplete frame, a gap in the sequence numbers) makes it skip frames up to the next keyframe and send KEYFRAME_REQUEST, repeated every 250 ms while it waits. It also waits for a keyframe at start, so the first picture comes as soon as the controller answers.
    - Measured on the device by dropping a share of the video packets on the controller host: at 0.1% loss each loss cost 0.9 frames on average (about 30 ms) and the frame rate stayed between 25 and 31 fps at 30 fps; at 1% loss, about 3 frames (100 ms) per loss, longer because losses come faster than the 250 ms limit on forced IDRs and some hit the IDR itself. The picture recovered after every loss.
  - It sends a PING once a second; the PONGs give the controller clock offset (`protocol/ClockSync.kt`).
- Decoder (`decoder/HardwareH264Decoder.kt`): MediaCodec in asynchronous mode with its callbacks on a dedicated thread. A complete access unit goes into the first free input buffer and every decoded picture is rendered at once; nothing waits with a timeout or polls. At most three access units wait for an input buffer; beyond that they are discarded and the receiver waits for a keyframe, instead of the picture falling further and further behind. On the device test this limit was never reached (`backlog` stayed 0 at 30 and 60 fps, and frames waited about 1 ms for an input buffer). The codec is configured with realtime priority and a high operating rate (and `KEY_LOW_LATENCY` on Android 11+), falling back to a plain format and then to a 16-aligned height.
  - Every attempt also sets `rk-immediate-out`, a Rockchip key that turns on the decoder's immediate output mode (`VPU_API_SET_IMMEDIATE_OUT`). Without it the Rockchip decoder on the target display (RK3288, `OMX.rk.video_decoder.avc`) holds each IDR and the frame after it until the next input arrives: measured at 30 fps, decode time went from 14 ms to 48 ms and 42 ms on those two frames, once a second. With it, keyframes take 16 ms like any other frame. The remaining 10–15 ms per frame does not depend on frame size (a 352-byte P-frame takes as long) and is not tied to vsync; it is the codec's own pipeline.
- Vsync pacing (`decoder/VsyncPacer.kt`): each decoded picture is released with a present timestamp. It keeps the vsync it would get anyway, unless the previous picture already has that vsync; then it moves to the next one, never more than two past its own (beyond that the schedule starts over). The vsync phase and period come from `Choreographer` on the decoder's callback thread. Released "now" instead, two pictures arriving within one refresh made SurfaceFlinger drop one and repeat a frame: at 60 fps only about 48 of 60 pictures a second reached the screen (measured with `dumpsys SurfaceFlinger --latency`); paced, all 60 do, one per refresh. A moved picture is shown one refresh (16.7 ms) later than it could have been; at 60 fps about one in six is moved. At 30 fps pictures rarely collide and pacing changes nothing.
- Measurement (`protocol/StreamStats.kt`): every second the app logs one line under the `BoothStats` tag and shows it in the OSD (tap the screen):
  - `fps`, and `stalls`: gaps of more than 50 ms between decoded pictures (stutter)
  - `e2e`, `net`, `wait`, `dec` as median/p90/max in ms: canvas frame reaching the controller's encoder to decoded picture out; first to last fragment received; complete to handed to the decoder; handed to the decoder to decoded picture out. Rendering at the next vsync and the panel come on top of `e2e`.
  - Totals since start: `lost` frames, `skipped` frames while waiting for a keyframe, `kfreq` keyframe requests sent, `backlog` frames discarded because the decoder fell behind; `rtt` to the controller; `paced` as pictures moved to a later vsync / schedule restarts.

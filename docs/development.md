# Development Guide & Notes

This document covers notes, dependencies and testing for developing and maintaining booth-display.

---

## 1. Development environment and tools

- Go: 1.26 or later (matches `go 1.26.7` in `controller/go.mod`)
- Rust: stable toolchain (cargo) for the media core (`core/`)
- The moip-engine submodule: `git submodule update --init` (`make build` / `make build-core` run it when `moip-engine/` is empty)
- Node.js: v20 or later
- uv: Python package manager
- libx264 runtime (`libx264-164` on Debian/Ubuntu, `x264-libs` from RPM Fusion): the software encoder, loaded at run time. `libx264-dev` / `x264-devel` adds a check of the struct layouts against `x264.h`.
- FFmpeg: 6.0 or later, for video-file and rainbow input, the `vaapi-ffmpeg` encoder and the tests that decode the encoders' output
- Intel VA-API driver (`intel-media-driver` / `libva-utils`, etc.)
- JDK 17 or later / Android SDK (for building the Android client)
- NDI library: `libndi.so.6` from the official NDI SDK, installed with `make setup-ndi`. It is needed only at run time; building needs no SDK headers.

### Android client environment
- Requires JDK 17 or later and the Android SDK (`platforms;android-34`, `build-tools;34.0.0`).
- Point to the SDK with `sdk.dir=...` in `android-client/local.properties` (not tracked by git) or with the `ANDROID_HOME` environment variable.
- Build: `cd android-client && ./gradlew assembleDebug` (output: `app/build/outputs/apk/debug/app-debug.apk`)
- Unit tests (JVM, no device): `cd android-client && ./gradlew testDebugUnitTest`. They cover the packet header, reassembly and loss detection, the clock offset, the statistics, and the receive thread against a fake controller socket (keyframe requests, PING/PONG).
- Measuring on a display: `adb logcat -s BoothStats` prints one line per second (fields in [architecture.md](architecture.md#3-android-client-internals-android-client)), e.g. `fps=30.0 e2e=38.2/41.0/45.5 net=2.1/3.0/4.2 wait=0.1/0.1/0.3 dec=14.0/15.2/16.1 stalls=0 lost=0 skipped=0 kfreq=1 backlog=0 rtt=0.80ms paced=12/0`. The controller and the app must speak the same protocol version; on a mismatch the OSD says so.

### Client simulator
- A tool that receives and decodes the UDP stream without real hardware and shows it in a web browser (`tools/simulator/client_simulator.py`).
- Its dependencies (`av` / `opencv-python` / `numpy`) are declared in the script itself (PEP 723 inline metadata); `uv run` installs them into a cached environment.
- Start: `make run-simulator` (or `uv run tools/simulator/client_simulator.py ...`) → `http://localhost:9001`

---

## 2. Development notes

### (1) How Web GUI assets are embedded
- The `dist/` assets built from the Vue 3 frontend (`controller/frontend/`) are compiled into the binary as an `embed.FS` through `controller/web/embed.go` in the Go backend.
- After changing the frontend, run `make build` or `make build-frontend` to regenerate them.
- The dev server (`make dev-frontend`) supports Vite HMR (Hot Module Replacement).

### (2) Dark mode with Tailwind CSS v4
- Tailwind CSS v4 drives dark mode from the media query by default, so `src/style.css` defines the following to enable class-based switching (`<html class="dark">`):
  ```css
  @custom-variant dark (&:where(.dark, .dark *));
  ```
- Do not remove this directive when changing the theme-switching logic.

### (3) Timeouts for HTTP streaming
- The Go `http.Server` has a `WriteTimeout` set. Long-lived connections such as the MJPEG preview (`/api/preview/mjpeg`) clear it with `http.NewResponseController(w).SetWriteDeadline(time.Time{})` so they are not cut off.

### (4) Measuring NDI latency
- The `ndi_latency` test (`core/src/mixer.rs`) measures the time from an NDI sender stamping a frame until the frame leaves the mixer. It runs only when `NDI_LATENCY_SOURCE` names a stamping sender. `tools/ndi-stamp-sender` is one (C, uses the system libndi):
  ```bash
  make ndi-stamp-sender
  tools/ndi-stamp-sender/ndi-stamp-sender booth-latency-test &   # args: [name] [fps] [seconds] [bgrx]
  NDI_LATENCY_SOURCE=booth-latency-test cargo test --release --manifest-path core/Cargo.toml ndi_latency -- --nocapture
  ```
  Sender and controller must run on the same host (the stamp is wall-clock time). Run only one sender per name.
- On a host where mDNS is blocked, point NDI at the sender with `NDI_CONFIG_DIR` and an `ndi-config.v1.json` that lists `127.0.0.1` under `ndi.networks.ips`.

### (5) Hardware acceleration (VA-API) and permissions
- At startup the controller looks for a GPU render node such as `/dev/dri/renderD128` and, if found, picks Intel VA-API hardware encoding automatically.
- If the user lacks access to the GPU device (e.g. a permission error), or on a non-Intel GPU, it automatically falls back to CPU software encoding (libx264 ultrafast, in-process).
- To use VA-API as a regular user on Linux, add the user to the `render` or `video` group:
  ```bash
  sudo usermod -aG render $USER
  ```


---

## 3. Testing

- All tests (builds the media core first):
  ```bash
  make test
  ```
  - `core/` (Rust, `cargo test`): packet header and MTU splitting, sender frame admission and keyframe resync, FLV parsing (including a round trip through real ffmpeg encoders), the scaler and UYVY conversion, mixer transitions and frame rate, encoder arguments
  - `core/` libx264: the struct layouts (against the installed `x264.h` when present), and an encode of a moving pattern that must decode with ffmpeg at > 30 dB luma PSNR with an AUD on every access unit and an IDR (with SPS/PPS) at each GOP start. Skipped when libx264 is not installed.
  - `core/` VA-API: the struct layouts, and an encode of a moving pattern that must decode with ffmpeg at > 35 dB luma PSNR with an IDR (with SPS/PPS) at each GOP start. Run it once more with `LIBVA_DRIVER_NAME=i965` to cover the Ivy Bridge driver; it is skipped without `/dev/dri/renderD128`. `libva-devel` (`libva-dev` on Debian) adds a check of the layouts against the installed headers.
  - `controller/internal` (Go): an end-to-end test that runs the controller with a built `booth-core` and checks UDP output, metrics, preview, switching and stop, with libx264 and (when a render node exists) VA-API. It is skipped when no `booth-core` is built; `BOOTH_CORE_BIN` points it at a binary.
- Opt-in measurements (machine dependent):
  ```bash
  # Scaler speed (NDI receive path)
  cargo test --release --manifest-path core/Cargo.toml scale::tests::bench -- --ignored --nocapture
  # Canvas frame in -> access unit out, with CPU use. LIBVA_DRIVER_NAME=i965 uses the Ivy Bridge driver.
  # In-process VA-API (3 displays in parallel):
  LATENCY_TEST=1 cargo test --release --manifest-path core/Cargo.toml vaenc::tests::latency -- --nocapture
  # In-process libx264 (3 displays in parallel):
  LATENCY_TEST=1 cargo test --release --manifest-path core/Cargo.toml x264enc::tests::latency -- --nocapture
  # ffmpeg h264_vaapi (LATENCY_IVY=1 masks AVX2/FMA3/BMI in ffmpeg):
  LATENCY_TEST=1 cargo test --release --manifest-path core/Cargo.toml encoder::tests::latency -- --nocapture
  # Highest sustained frame rate: encoders run back to back with no 30 fps pacing.
  # THROUGHPUT_DISPLAYS=1..3 sets the number of parallel encoders (default 3).
  THROUGHPUT_TEST=1 cargo test --release --manifest-path core/Cargo.toml vaenc::tests::throughput -- --nocapture
  THROUGHPUT_TEST=1 cargo test --release --manifest-path core/Cargo.toml x264enc::tests::throughput -- --nocapture
  ```
- CI (`.github/workflows/ci.yml`, GitHub-hosted `ubuntu-24.04`) runs `make build-frontend test` on every pull request and push to `main`, with libx264 and ffmpeg installed. The runner has no Intel GPU, so the VA-API tests skip themselves. It then runs the throughput tests for 1 and 3 displays and writes the numbers to the job summary; they measure the runner's CPU and never fail the build. A second job runs the Android unit tests and the debug build.
- Running the core by hand: `controller/bin/booth-core engine` reads JSON commands on stdin (e.g. `{"id":1,"cmd":"start","mode":"testpattern"}`) and writes framed events on stdout; `booth-core discover` lists NDI sources and `booth-core version` prints the NDI runtime version.

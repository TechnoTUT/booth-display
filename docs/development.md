# Development Guide & Notes

This document covers notes, dependencies and testing for developing and maintaining booth-display.

---

## 1. Development environment and tools

- Go: 1.26 or later (matches `go 1.26.7` in `controller/go.mod`)
- Rust: stable toolchain (cargo) for the media core (`core/`)
- The moip-engine submodule: `git submodule update --init` (`make build` / `make build-core` run it when `moip-engine/` is empty)
- Node.js: v20 or later
- uv: Python package manager
- FFmpeg: 6.0 or later (with `libx264`, plus `h264_vaapi` when using VA-API)
- Intel VA-API driver (`intel-media-driver` / `libva-utils`, etc.)
- JDK 17 or later / Android SDK (for building the Android client)
- NDI library: `libndi.so.6` from the official NDI SDK, installed with `make setup-ndi`. It is needed only at run time; building needs no SDK headers.

### Android client environment
- Requires JDK 17 or later and the Android SDK (`platforms;android-34`, `build-tools;34.0.0`).
- Point to the SDK with `sdk.dir=...` in `android-client/local.properties` (not tracked by git) or with the `ANDROID_HOME` environment variable.
- Build: `cd android-client && ./gradlew assembleDebug` (output: `app/build/outputs/apk/debug/app-debug.apk`)

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
- If the user lacks access to the GPU device (e.g. a permission error), or on a non-Intel GPU, it automatically falls back to CPU software encoding (`libx264` ultrafast).
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
  - `controller/internal` (Go): an end-to-end test that runs the controller with a built `booth-core` and checks UDP output, metrics, preview, switching and stop. It is skipped when no `booth-core` is built; `BOOTH_CORE_BIN` points it at a binary.
- Opt-in measurements (machine dependent):
  ```bash
  # Scaler speed (NDI receive path)
  cargo test --release --manifest-path core/Cargo.toml scale::tests::bench -- --ignored --nocapture
  # Canvas frame in -> access unit out, for libx264 and VA-API.
  # LIBVA_DRIVER_NAME=i965 uses the Ivy Bridge driver, LATENCY_IVY=1 masks AVX2/FMA3/BMI.
  LATENCY_TEST=1 cargo test --release --manifest-path core/Cargo.toml encoder::tests::latency -- --nocapture
  ```
- Running the core by hand: `controller/bin/booth-core engine` reads JSON commands on stdin (e.g. `{"id":1,"cmd":"start","mode":"testpattern"}`) and writes framed events on stdout; `booth-core discover` lists NDI sources and `booth-core version` prints the NDI runtime version.

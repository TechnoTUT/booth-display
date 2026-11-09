# Development Guide & Notes

This document covers notes, dependencies and testing for developing and maintaining booth-display.

---

## 1. Development environment and tools

- Go: 1.26 or later (matches `go 1.26.7` in `controller/go.mod`)
- Node.js: v20 or later
- uv: Python package manager
- FFmpeg: 6.0 or later (with `libx264`, plus `h264_vaapi` when using VA-API)
- Intel VA-API driver (`intel-media-driver` / `libva-utils`, etc.)
- JDK 17 or later / Android SDK (for building the Android client)
- NDI library: cyndilib virtual environment (`.venv` in the repository root, created by `make setup-python`)

### Android client environment
- Requires JDK 17 or later and the Android SDK (`platforms;android-34`, `build-tools;34.0.0`).
- Point to the SDK with `sdk.dir=...` in `android-client/local.properties` (not tracked by git) or with the `ANDROID_HOME` environment variable.
- Build: `cd android-client && ./gradlew assembleDebug` (output: `app/build/outputs/apk/debug/app-debug.apk`)

### Client simulator
- A tool that receives and decodes the UDP stream without real hardware and shows it in a web browser (`tools/simulator/client_simulator.py`).
- Its dependencies (`av` / `opencv-python` / `numpy`) are managed in `pyproject.toml` at the repository root and installed into `.venv`.
- Start: `make run-simulator` (runs `make setup-python` automatically) → `http://localhost:9001`

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

### (4) NDI process shutdown
- `cyndilib` holds NDI runtime threads written in C, so the Python process can deadlock in the standard exit hooks. For that reason the bridge script (`controller/scripts/ndi_bridge.py`) exits immediately with an explicit `os._exit(0)`.

### (5) Hardware acceleration (VA-API) and permissions
- At startup the controller looks for a GPU render node such as `/dev/dri/renderD128` and, if found, picks Intel VA-API hardware encoding automatically.
- If the user lacks access to the GPU device (e.g. a permission error), or on a non-Intel GPU, it automatically falls back to CPU software encoding (`libx264` ultrafast).
- To use VA-API as a regular user on Linux, add the user to the `render` or `video` group:
  ```bash
  sudo usermod -aG render $USER
  ```


---

## 3. Testing

- Unit and end-to-end tests:
  ```bash
  make test
  ```
  - `controller/internal/protocol`: packet header marshaling, and splitting/reassembly at the MTU limit
  - `controller/internal/pipeline`: the H.264 NALU parser
  - `controller/internal`: an integration test of the whole controller, from UDP output to checking the received packets

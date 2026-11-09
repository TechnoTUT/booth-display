# booth-display

A controller system that drives a tiled booth display built from LG Display LD290EJS-FPN1 panels (1920x540) and keeps them in sync.

It runs as a standalone single binary with a built-in Web GUI. It crops a single virtual canvas into one region per display and streams all of them at once over low-latency UDP (H.264 Annex-B).

See [docs/](docs/) for the architecture and protocol details:
- System architecture: [docs/architecture.md](docs/architecture.md)
- Video streaming protocol: [docs/streaming-protocol.md](docs/streaming-protocol.md)
- Development notes and environment setup: [docs/development.md](docs/development.md)

## Requirements

- OS: Linux (Debian 12+, Ubuntu 22.04+, AlmaLinux / RHEL 9+, Fedora 39+, etc.)
- Go: 1.26 or later
- Node.js: 20 or later
- FFmpeg: 6.0 or later (with `libx264`, plus `h264_vaapi` when using an Intel GPU)
- Intel VA-API driver (recommended: integrated graphics on 6th-gen Skylake or newer Intel CPUs, for fast encoding at low CPU load)
- uv: Python package manager (needed for NDI input and the client simulator)
- To build the Android client:
  - JDK 17 or later
  - Android SDK (`platforms;android-34`, `build-tools;34.0.0`)
  - Point to the SDK with `sdk.dir=...` in `android-client/local.properties` or with the `ANDROID_HOME` environment variable

---

## FFmpeg and hardware acceleration (VA-API) setup

The controller supports hardware-accelerated encoding through **VA-API (`h264_vaapi`)** on Intel integrated graphics (Intel HD / UHD Graphics). This keeps CPU load to a minimum and makes simultaneous real-time 30 fps output to several displays stable. On systems without VA-API it automatically falls back to CPU software encoding with `libx264`.

### 1. Install packages

#### AlmaLinux / Rocky Linux / RHEL / Fedora
```bash
# Enable the RPM Fusion / EPEL repositories (if not already enabled)
sudo dnf install -y epel-release
sudo dnf install -y --nogpgcheck https://mirrors.rpmfusion.org/free/el/rpmfusion-free-release-$(rpm -E %rhel).noarch.rpm

# Install FFmpeg, the VA-API driver and diagnostic tools
sudo dnf install -y ffmpeg libva-utils intel-media-driver
```

#### Ubuntu / Debian
```bash
sudo apt update
sudo apt install -y ffmpeg vainfo intel-media-va-driver-non-free
# (for the open-source driver, use intel-media-driver or intel-media-va-driver instead)
```

### 2. Verify

```bash
# Check that a VA-API device is present (e.g. renderD128)
ls -l /dev/dri/renderD*

# Check for H.264 encode support (VAEntrypointEncSlice)
vainfo

# Check that FFmpeg has the VA-API encoder
ffmpeg -hide_banner -encoders | grep h264_vaapi
```

> **Note**: The user running the controller needs access to `/dev/dri/renderD128`. If needed, add the user to the `render` (or `video`) group with `sudo usermod -aG render $USER` and log in again.

---

## Building

The Makefile in the repository root builds the frontend (Vue 3) and the backend (Go) together. The frontend build output is embedded into the Go binary.

```bash
# Build frontend and backend
make build

# Run tests
make test
```

The build produces `controller/bin/display-controller`.

Individual targets for development:
```bash
make build-frontend  # Build the frontend only
make build-backend   # Build the Go controller only
make dev-frontend    # Start the frontend dev server (Vite)
```

---

## Running

Run the binary with a config file:

```bash
# Start normally
./controller/bin/display-controller -config config.yaml

# Or start via make
make run
```

Then open the dashboard in a browser:
- Web GUI dashboard: `http://localhost:8080`

---

## Android client app (android-client/)

The app that receives and displays the video on the LG Display LD290EJS-FPN1 (Android 7.1 / 1920x540).

### Features
- Hardware H.264 decoding with MediaCodec, rendering directly to a SurfaceView
- Low-memory design for 1 GB RAM (no intermediate Bitmaps, which avoids OOM)
- Immersive full-screen mode and auto-start on boot (BOOT_COMPLETED)

### Building
Set up the Android SDK listed under Requirements first.

```bash
cd android-client

# Debug build (signed, so it can be installed on the device as is)
./gradlew assembleDebug
```
Output: `android-client/app/build/outputs/apk/debug/app-debug.apk`

```bash
# Install on the device
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

> The release build (`./gradlew assembleRelease`) has no signing config, so it produces `app-release-unsigned.apk`, which cannot be installed until you sign it.

## Python environment (NDI bridge / simulator)

The dependencies for NDI input (`cyndilib`) and the client simulator (`av`) are managed in `pyproject.toml` at the repository root and installed into `.venv`.

```bash
make setup-python   # uv sync
```

Start the controller from the repository root (it uses `uv run --project .` to find `.venv`).

## Client simulator (tools/simulator/)

Lets you check the stream without real hardware: it decodes the H.264 stream received over UDP and shows it in a web browser. The Python environment is created automatically as `.venv` by `uv` from the root `pyproject.toml` (`make setup-python`).

```bash
# Start the simulator for display-1 (UDP :8554), then open http://localhost:9001
make run-simulator
```

## Configuration (config.yaml)

Sets the server ports, the virtual canvas resolution, and each display's destination IP/port and crop position.

> `ip: "127.0.0.1"` in the example below is for local testing with the simulator. For real hardware, change it to the actual IP address of each display (Android client).

```yaml
server:
  http_port: 8080
  default_stream_port: 8554
  ws_ping_interval_sec: 5

canvas:
  width: 5760
  height: 540
  fps: 30

displays:
  - id: "display-1"
    name: "Display Left"
    ip: "127.0.0.1"
    port: 8554
    width: 1920
    height: 540
    crop_x: 0
    crop_y: 0
    bezel_padding_right: 0

  - id: "display-2"
    name: "Display Center"
    ip: "127.0.0.1"
    port: 8555
    width: 1920
    height: 540
    crop_x: 1920
    crop_y: 0
    bezel_padding_right: 0

  - id: "display-3"
    name: "Display Right"
    ip: "127.0.0.1"
    port: 8556
    width: 1920
    height: 540
    crop_x: 3840
    crop_y: 0
    bezel_padding_right: 0

media:
  default_mode: "testpattern" # "testpattern", "video", or "ndi"
  video_file: ""

pipeline:
  encoder: "auto" # "auto" (prefer VA-API, fall back automatically), "vaapi", or "software"
  vaapi_device: "/dev/dri/renderD128" # VA-API device node
```

---

## License

[MIT License](LICENSE)

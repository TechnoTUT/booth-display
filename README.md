# booth-display

A controller system that drives a tiled booth display built from LG Display LD290EJS-FPN1 panels (1920x540) and keeps them in sync.

It runs as a standalone single binary with a built-in Web GUI. It crops a single virtual canvas into one region per display and streams all of them at once over low-latency UDP (H.264 Annex-B).

See [docs/](docs/) for the architecture and protocol details:
- System architecture: [docs/architecture.md](docs/architecture.md)
- Video streaming protocol: [docs/streaming-protocol.md](docs/streaming-protocol.md)
- Development notes and environment setup: [docs/development.md](docs/development.md)

## Requirements

- OS: Linux (Debian 12+, Ubuntu 22.04+, AlmaLinux / RHEL 9+, Fedora 39+, etc.)
- Go: 1.26 or later, with a C compiler (`gcc`) for cgo (the NDI input calls libndi through cgo)
- Node.js: 20 or later
- FFmpeg: 6.0 or later (with `libx264`, plus `h264_vaapi` when using an Intel GPU)
- Intel VA-API driver for fast encoding at low CPU load: `intel-media-driver` (iHD) on 5th-gen Broadwell and newer, or the legacy `i965` driver on 3rd/4th-gen Ivy Bridge / Haswell (the reference target is a Core i5-3470)
- NDI runtime (`libndi.so.6`) for NDI input: installed from the official NDI SDK for Linux with `make setup-ndi` (see [NDI runtime](#ndi-runtime))
- uv: Python package manager (only for the client simulator)
- To build the Android client:
  - JDK 17 or later
  - Android SDK (`platforms;android-34`, `build-tools;34.0.0`)
  - Point to the SDK with `sdk.dir=...` in `android-client/local.properties` or with the `ANDROID_HOME` environment variable

---

## Setup

On a new machine, in this order (each step is explained in its own section below):

```bash
# 1. FFmpeg and the VA-API driver for your GPU
#    → see "FFmpeg and hardware acceleration (VA-API) setup"

# 2. NDI runtime: downloads the official NDI SDK for Linux, shows its license,
#    installs libndi.so.6 to /usr/local/lib and registers it with the dynamic linker (sudo)
make setup-ndi

# 3. Frontend dependencies (once, and after package-lock.json changes)
npm --prefix controller/frontend ci

# 4. Build and run
make build
./controller/bin/display-controller -config config.yaml
```

Then open the Web GUI at `http://<host>:8080/` (the port is `server.http_port` in `config.yaml`).

Optional, for development:

- `make run-simulator`: client simulator, a stand-in for the displays (needs `uv`; its Python dependencies are installed automatically)
- `make ndi-stamp-sender`: NDI test sender for measuring NDI latency (see [docs/development.md](docs/development.md))

Python and a `.venv` are not needed to run the controller.

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

#### Ivy Bridge / Haswell (e.g. Core i5-3470)

The iHD driver does not support these GPUs. Install the legacy i965 driver instead and select it:

```bash
# AlmaLinux / RHEL / Fedora (RPM Fusion)
sudo dnf install -y libva-intel-driver
# Ubuntu / Debian
sudo apt install -y i965-va-driver

# Only needed when both drivers are installed
export LIBVA_DRIVER_NAME=i965
```

`vainfo` should then list `VAProfileH264ConstrainedBaseline : VAEntrypointEncSlice`. The i965 driver prints a `Not using hybrid_drv_video.so` notice; it is harmless. If VA-API cannot start, the controller falls back to libx264, which still keeps up with three 1920x540 outputs on a Core i5-3470 class CPU.

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

## NDI runtime

The controller receives NDI directly through the NDI SDK library (`libndi`), loaded at runtime. Install it once per machine from the official NDI SDK for Linux:

```bash
make setup-ndi   # scripts/install-ndi-sdk.sh: downloads the SDK, shows its license, installs libndi.so.6 to /usr/local/lib (sudo)
```

The script registers `/usr/local/lib` with the dynamic linker (`/etc/ld.so.conf.d/ndi-sdk.conf`) when it is not searched already, as on Fedora/RHEL. `PREFIX=/opt/ndi make setup-ndi` installs elsewhere. On further machines, `NDI_ACCEPT_LICENSE=yes make setup-ndi` skips the license prompt once you have accepted it.

The controller looks for the library in this order:

1. `$NDI_RUNTIME_DIR_V6/libndi.so.6` (the NDI SDK's environment variable)
2. `libndi.so.6` / `libndi.so` on the system library path
3. `/usr/local/lib/libndi.so.6`

Without it, everything except NDI input works, and the GUI shows why NDI is unavailable.

NDI finds senders over mDNS (UDP 5353). If a firewall blocks it, list sender addresses in the NDI configuration file (`~/.ndi/ndi-config.v1.json`, or the directory named by `NDI_CONFIG_DIR`):

```json
{"ndi": {"networks": {"ips": "192.168.1.20,192.168.1.21"}}}
```

## Client simulator (tools/simulator/)

Lets you check the stream without real hardware: it decodes the H.264 stream received over UDP and shows it in a web browser. Its Python dependencies are declared inside the script (inline script metadata), and `uv run` installs them into a cached environment on first run.

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

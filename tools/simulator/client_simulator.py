#!/usr/bin/env python3
"""
client_simulator.py
Web-based Android Client Simulator for booth-display (LD290EJS-FPN1 / 1920x540).

Functions:
  1. Binds UDP socket (port 8554, 8555, or 8556).
  2. Parses 16-byte custom header and reassembles H.264 NAL fragments.
  3. Decodes H.264 stream using hardware/fast libavcodec (PyAV).
  4. Encodes screen as 1920x540 MJPEG and serves a standalone Web UI at http://<ip>:<web_port>/.

Run via uv:
  uv run --directory <uv-project-dir> python tools/simulator/client_simulator.py --port 8554 --web-port 9001 --id "display-1"
"""
from __future__ import annotations

import argparse
import http.server
import io
import json
import socket
import struct
import sys
import threading
import time
from typing import Optional

try:
    import av
    import cv2
    import numpy as np
except ImportError as e:
    sys.stderr.write(f"Missing dependency: {e}\n")
    sys.exit(1)

MAGIC_BYTE = 0xBD
VERSION_BYTE = 0x01
HEADER_SIZE = 16


class FrameAssembler:
    def __init__(self, callback):
        self.callback = callback
        self.current_seq_base = -1
        self.expected_frags = 0
        self.received_frags = 0
        self.is_keyframe = False
        self.current_ts = 0
        self.fragments: dict[int, bytes] = {}

    def on_packet(self, data: bytes):
        if len(data) < HEADER_SIZE:
            return
        magic, ver, p_type, flags = struct.unpack("!BBBB", data[:4])
        if magic != MAGIC_BYTE or ver != VERSION_BYTE:
            return
        seq, ts, frag_idx, frag_total = struct.unpack("!IIHH", data[4:16])
        payload = data[16:]
        is_key = bool(flags & 0x02)
        is_marker = bool(flags & 0x01)

        # Single-packet Access Unit (not fragmented)
        if frag_total <= 1:
            self.callback(payload, is_key, ts)
            return

        # Fragmented Access Unit: calculate the base sequence number for this AU
        seq_base = seq - frag_idx
        if seq_base != self.current_seq_base:
            self.current_seq_base = seq_base
            self.expected_frags = frag_total
            self.received_frags = 0
            self.is_keyframe = is_key
            self.current_ts = ts
            self.fragments.clear()

        if is_key:
            self.is_keyframe = True

        if frag_idx not in self.fragments:
            self.fragments[frag_idx] = payload
            self.received_frags += 1

        # Check completion either by receiving all expected fragments or by marker flag
        if self.expected_frags > 0 and (self.received_frags == self.expected_frags or (is_marker and len(self.fragments) == frag_total)):
            ordered = [self.fragments[i] for i in range(self.expected_frags) if i in self.fragments]
            if len(ordered) == self.expected_frags:
                full_frame = b"".join(ordered)
                self.callback(full_frame, self.is_keyframe, self.current_ts)
            self.expected_frags = 0
            self.received_frags = 0
            self.fragments.clear()


class SimulatorState:
    def __init__(self, display_id: str, udp_port: int, width: int = 1920, height: int = 540):
        self.display_id = display_id
        self.udp_port = udp_port
        self.width = width
        self.height = height
        self.lock = threading.Lock()
        self.latest_jpeg: Optional[bytes] = None
        self.fps = 0.0
        self.frames_received = 0
        self.packets_received = 0
        self.bytes_received = 0
        self.last_ts = 0
        self.last_nal_type = "None"
        self.show_osd = True
        self.last_frame_time = 0.0


class StreamingHandler(http.server.BaseHTTPRequestHandler):
    state: SimulatorState

    def do_GET(self):
        if self.path == "/" or self.path.startswith("/?"):
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.end_headers()
            html = f"""<!DOCTYPE html>
<html lang="en" class="dark">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>BOOTH-DISPLAY | Simulator ({self.state.display_id})</title>
  <link rel="icon" type="image/svg+xml" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='5 3 61 69'%3E%3Cpolygon fill='%233a3a3a' points='45.21 26.6 45.21 48.36 52.35 44.23 52.38 38.99 56.92 41.6 64.05 37.48 45.21 26.6'/%3E%3Cpolygon fill='%233a3a3a' points='17.34 10.51 17.34 10.51 7.11 4.6 7.11 70.36 14.28 66.21 14.28 17 17.34 18.75 17.34 64.44 24.52 60.3 24.52 22.93 29.48 25.81 33.07 19.59 17.34 10.51'/%3E%3Cpolygon fill='%23c7000a' points='45.84 26.97 38.68 22.83 38.67 43.92 35.64 45.68 35.64 21.08 28.47 16.94 28.46 58.03 45.85 47.99 45.84 26.97'/%3E%3C/svg%3E" />
  <script src="https://cdn.tailwindcss.com"></script>
  <script>
    tailwind.config = {{
      darkMode: 'class',
      theme: {{
        extend: {{
          colors: {{
            brand: '#C7000A',
            'brand-hover': '#b00009',
          }}
        }}
      }}
    }}
  </script>
</head>
<body class="bg-slate-50 dark:bg-slate-950 text-slate-900 dark:text-slate-100 min-h-screen flex flex-col font-sans antialiased selection:bg-[#C7000A]/30 selection:text-white">
  <!-- Top Navigation Header -->
  <header class="border-b border-slate-200 dark:border-slate-800 bg-white/90 dark:bg-slate-900/80 backdrop-blur px-4 sm:px-6 py-2.5 xl:py-3 flex flex-wrap items-center justify-between gap-y-2.5 sticky top-0 z-30 shadow-sm">
    <div class="flex items-center gap-4">
      <div class="flex items-center gap-3">
        <!-- SVG Logo -->
        <svg class="h-8 w-auto" viewBox="0 0 300.41 74.96" fill="currentColor">
          <g>
            <path class="fill-slate-900 dark:fill-white" d="M73.74,25.77H64.6V18.55H90.14v7.22H81V57H73.74Z"/>
            <path class="fill-slate-900 dark:fill-white" d="M99.59,49.7h14.64V57h-22V27.7h22V45.58H99.59Zm0-14.67V40h7.31V35Z"/>
            <path class="fill-slate-900 dark:fill-white" d="M131,45.77h7.31V57h-22V27.7h22V39H131V35h-7.33V49.7H131Z"/>
            <path class="fill-slate-900 dark:fill-white" d="M140.44,18.55h7.31V27.7h14.67V57h-7.31V35h-7.33V57h-7.33Z"/>
            <path class="fill-slate-900 dark:fill-white" d="M171.86,57h-7.33V27.7h22V57H179.2V35h-7.33Z"/>
            <path class="fill-slate-900 dark:fill-white" d="M210.59,27.7V57h-22V27.7ZM196,35V49.7h7.33V35Z"/>
            <path class="fill-slate-900 dark:fill-white" d="M221.86,25.77h-9.15V18.55h25.54v7.22h-9.15V57h-7.25Z"/>
            <path class="fill-slate-900 dark:fill-white" d="M258.69,18.55h7.22V57H240.37V18.55h7.25V49.78h11.07Z"/>
            <path class="fill-slate-900 dark:fill-white" d="M277.18,25.77H268V18.55h25.54v7.22h-9.15V57h-7.25Z"/>
            <polygon class="fill-slate-900 dark:fill-white" points="45.21 26.6 45.21 48.36 52.35 44.23 52.38 38.99 56.92 41.6 64.05 37.48 45.21 26.6"/>
            <polygon class="fill-slate-900 dark:fill-white" points="17.34 10.51 17.34 10.51 7.11 4.6 7.11 70.36 14.28 66.21 14.28 17 17.34 18.75 17.34 64.44 24.52 60.3 24.52 22.93 29.48 25.81 33.07 19.59 17.34 10.51"/>
            <polygon fill="#c7000a" points="45.84 26.97 38.68 22.83 38.67 43.92 35.64 45.68 35.64 21.08 28.47 16.94 28.46 58.03 45.85 47.99 45.84 26.97"/>
          </g>
        </svg>
        <div class="h-6 w-px bg-slate-200 dark:bg-slate-800"></div>
        <div>
          <div class="flex items-center gap-2">
            <h1 class="text-sm font-bold tracking-tight text-slate-900 dark:text-slate-100">Android Client Emulator</h1>
            <span class="px-2 py-0.5 rounded-md text-[10px] font-bold uppercase tracking-wider bg-[#C7000A] text-white">{self.state.display_id}</span>
          </div>
          <p class="text-[10px] text-slate-500 dark:text-slate-400 font-mono">LG Display LD290EJS-FPN1 (1920&times;540 / 32:9)</p>
        </div>
      </div>
    </div>

    <!-- Right Header Badges -->
    <div class="flex items-center gap-2 sm:gap-3 text-xs">
      <div class="flex items-center gap-2.5 bg-slate-100 dark:bg-slate-900 px-3 py-1.5 rounded-xl border border-slate-200 dark:border-slate-800 font-mono select-none">
        <div class="flex items-center gap-1.5">
          <span class="text-slate-400 font-sans font-semibold text-[10px] uppercase">PORT</span>
          <span class="font-bold text-slate-700 dark:text-slate-200">:{self.state.udp_port}</span>
        </div>
        <div class="w-px h-3 bg-slate-300 dark:bg-slate-700"></div>
        <div class="flex items-center gap-1.5">
          <span class="text-slate-400 font-sans font-semibold text-[10px] uppercase">FPS</span>
          <span id="fpsBadge" class="font-bold text-emerald-500 tabular-nums">{self.state.fps:.1f}</span>
        </div>
      </div>

      <!-- Online Badge -->
      <div class="flex items-center gap-1.5 px-3 py-1.5 rounded-xl border border-emerald-200 dark:border-emerald-900 bg-emerald-50 dark:bg-emerald-950/40 text-emerald-600 dark:text-emerald-400 font-semibold text-xs">
        <span class="h-2 w-2 rounded-full bg-emerald-500 animate-pulse"></span>
        <span>Online</span>
      </div>

      <!-- Dark / Light Mode Toggle Button -->
      <button onclick="document.documentElement.classList.toggle('dark')" class="p-2 rounded-xl border border-slate-200 dark:border-slate-800 text-slate-500 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-100 hover:bg-slate-100 dark:hover:bg-slate-800 transition cursor-pointer" title="Toggle Theme">
        <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 3v1m0 16v1m9-9h-1M4 9h-1m15.364 6.364l-.707-.707M6.343 6.343l-.707-.707m12.728 0l-.707.707M6.343 17.657l-.707.707M16 12a4 4 0 11-8 0 4 4 0 018 0z"></path></svg>
      </button>
    </div>
  </header>

  <!-- Main View Container -->
  <main class="flex-1 max-w-7xl w-full mx-auto p-4 sm:p-6 lg:p-8 flex flex-col justify-center space-y-6">
    <!-- Panel Simulation Card -->
    <div class="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-5 sm:p-6 shadow-sm space-y-4">
      <div class="flex items-center justify-between">
        <div>
          <div class="flex items-center gap-2 mb-1">
            <span class="inline-block w-2.5 h-2.5 rounded-full bg-emerald-500 animate-pulse"></span>
            <h2 class="text-xs font-bold text-slate-500 dark:text-slate-400 uppercase tracking-wider">
              Native Hardware SurfaceView (1920&times;540)
            </h2>
          </div>
          <p class="text-xs sm:text-sm text-slate-600 dark:text-slate-300">
            Real-time low-latency UDP H.264 stream received and decoded via hardware pipeline simulation.
          </p>
        </div>

        <div class="flex items-center gap-2">
          <a href="http://localhost:8080/" target="_blank" class="px-3.5 py-1.5 rounded-xl bg-slate-100 dark:bg-slate-800 hover:bg-slate-200 dark:hover:bg-slate-700 border border-slate-200 dark:border-slate-700 text-xs font-semibold text-slate-700 dark:text-slate-200 flex items-center gap-1.5 transition">
            <span>Controller GUI</span>
            <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"></path></svg>
          </a>
        </div>
      </div>

      <!-- 32:9 Ultra-Wide Bezel Display Viewport -->
      <div class="relative w-full bg-black rounded-xl overflow-hidden border-4 sm:border-8 border-slate-800 dark:border-slate-950 shadow-2xl" style="aspect-ratio: 1920 / 540;">
        <img src="/stream.mjpg" alt="Display Stream" class="w-full h-full object-fill block" />
      </div>

      <!-- Quick Metrics Strip -->
      <div class="grid grid-cols-1 sm:grid-cols-4 gap-3 pt-2 text-xs">
        <div class="bg-slate-50 dark:bg-slate-950/60 p-3 rounded-xl border border-slate-200 dark:border-slate-800/80">
          <span class="text-slate-500 dark:text-slate-400 block text-[10px] font-semibold uppercase">Resolution</span>
          <span class="font-mono font-bold text-slate-900 dark:text-slate-100">1920 &times; 540 px</span>
        </div>
        <div class="bg-slate-50 dark:bg-slate-950/60 p-3 rounded-xl border border-slate-200 dark:border-slate-800/80">
          <span class="text-slate-500 dark:text-slate-400 block text-[10px] font-semibold uppercase">Protocol</span>
          <span class="font-mono font-bold text-sky-600 dark:text-sky-400">H.264 Access Unit</span>
        </div>
        <div class="bg-slate-50 dark:bg-slate-950/60 p-3 rounded-xl border border-slate-200 dark:border-slate-800/80">
          <span class="text-slate-500 dark:text-slate-400 block text-[10px] font-semibold uppercase">Frames / Packets</span>
          <span id="framesBadge" class="font-mono font-bold text-slate-900 dark:text-slate-100 tabular-nums">0 / 0</span>
        </div>
        <div class="bg-slate-50 dark:bg-slate-950/60 p-3 rounded-xl border border-slate-200 dark:border-slate-800/80">
          <span class="text-slate-500 dark:text-slate-400 block text-[10px] font-semibold uppercase">Stream Status</span>
          <span id="statusBadge" class="font-mono font-bold text-amber-500 tabular-nums">Standby</span>
        </div>
      </div>
    </div>
  </main>

  <script>
    setInterval(async () => {{
      try {{
        const res = await fetch('/api/status');
        if (res.ok) {{
          const d = await res.json();
          const fpsEl = document.getElementById('fpsBadge');
          if (fpsEl) fpsEl.textContent = d.fps.toFixed(1);
          const framesEl = document.getElementById('framesBadge');
          if (framesEl) framesEl.textContent = `${{d.frames_received}} / ${{d.packets_received}}`;
          const statusEl = document.getElementById('statusBadge');
          if (statusEl) {{
            if (d.active) {{
              statusEl.textContent = 'Active (' + d.last_nal_type + ')';
              statusEl.className = 'font-mono font-bold text-emerald-500 tabular-nums';
            }} else {{
              statusEl.textContent = 'Standby';
              statusEl.className = 'font-mono font-bold text-amber-500 tabular-nums';
            }}
          }}
        }}
      }} catch (e) {{}}
    }}, 1000);
  </script>

  <!-- Footer -->
  <footer class="border-t border-slate-200 dark:border-slate-800/80 py-4 px-6 text-center text-xs text-slate-500 dark:text-slate-400">
    TechnoTUT booth-display &bull; Synchronized Multi-Display System
  </footer>
</body>
</html>"""
            self.wfile.write(html.encode("utf-8"))
            return

        if self.path == "/api/status":
            now = time.time()
            with self.state.lock:
                data = {
                    "display_id": self.state.display_id,
                    "udp_port": self.state.udp_port,
                    "fps": round(self.state.fps, 1),
                    "frames_received": self.state.frames_received,
                    "packets_received": self.state.packets_received,
                    "bytes_received": self.state.bytes_received,
                    "last_ts": self.state.last_ts,
                    "last_nal_type": self.state.last_nal_type,
                    "active": (now - self.state.last_frame_time < 2.0) if self.state.last_frame_time > 0 else False,
                }
            resp = json.dumps(data).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(resp)))
            self.send_header("Access-Control-Allow-Origin", "*")
            self.end_headers()
            self.wfile.write(resp)
            return

        if self.path == "/stream.mjpg":
            self.send_response(200)
            self.send_header("Content-Type", "multipart/x-mixed-replace; boundary=frame")
            self.send_header("Cache-Control", "no-cache, no-store, must-revalidate")
            self.send_header("Connection", "close")
            self.end_headers()

            while True:
                with self.state.lock:
                    jpeg = self.state.latest_jpeg

                if jpeg:
                    try:
                        self.wfile.write(b"--frame\r\n")
                        self.wfile.write(b"Content-Type: image/jpeg\r\n")
                        self.wfile.write(f"Content-Length: {len(jpeg)}\r\n\r\n".encode("utf-8"))
                        self.wfile.write(jpeg)
                        self.wfile.write(b"\r\n")
                    except (BrokenPipeError, ConnectionResetError):
                        break
                time.sleep(0.033)  # ~30 fps cap
            return

        self.send_error(404)


def create_standby_frame(state: SimulatorState) -> bytes:
    # 1920x540 blank standby frame with OSD
    img = np.zeros((state.height, state.width, 3), dtype=np.uint8)
    # Background slate
    img[:] = (15, 23, 42)

    # Grid line
    cv2.line(img, (0, 0), (state.width, state.height), (30, 41, 59), 2)
    cv2.line(img, (0, state.height), (state.width, 0), (30, 41, 59), 2)

    # Draw OSD Card
    cv2.rectangle(img, (40, 40), (600, 160), (10, 15, 28), -1)
    cv2.rectangle(img, (40, 40), (600, 160), (199, 0, 10), 2)

    cv2.putText(img, f"BOOTH-DISPLAY CLIENT ({state.display_id})", (60, 75), cv2.FONT_HERSHEY_SIMPLEX, 0.8, (255, 255, 255), 2)
    cv2.putText(img, f"Listening on UDP :{state.udp_port} (Waiting for stream...)", (60, 110), cv2.FONT_HERSHEY_SIMPLEX, 0.6, (148, 163, 184), 1)
    cv2.putText(img, "Resolution: 1920x540 | Android 7.1 HW MediaCodec", (60, 140), cv2.FONT_HERSHEY_SIMPLEX, 0.5, (100, 116, 139), 1)

    _, jpeg = cv2.imencode(".jpg", img, [cv2.IMWRITE_JPEG_QUALITY, 85])
    return jpeg.tobytes()


def detect_au_nal_types(data: bytes) -> str:
    types = []
    i = 0
    while i < len(data) - 4:
        if data[i:i+3] == b"\x00\x00\x01":
            nal_type = data[i+3] & 0x1F
            types.append(nal_type)
            i += 4
        elif data[i:i+4] == b"\x00\x00\x00\x01":
            nal_type = data[i+4] & 0x1F
            types.append(nal_type)
            i += 5
        else:
            i += 1
    if not types:
        return "Unknown"
    labels = []
    type_names = {1: "P", 5: "IDR", 7: "SPS", 8: "PPS", 9: "AUD"}
    for t in types:
        labels.append(type_names.get(t, f"NAL-{t}"))
    return "+".join(labels)


def udp_worker(state: SimulatorState):
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind(("0.0.0.0", state.udp_port))
    sock.settimeout(0.5)

    codec = av.CodecContext.create("h264", "r")
    frame_count = 0
    last_calc = time.time()

    def on_frame(full_frame: bytes, is_keyframe: bool, ts: int):
        nonlocal frame_count, last_calc
        state.bytes_received += len(full_frame)
        state.frames_received += 1
        state.last_ts = ts
        state.last_nal_type = detect_au_nal_types(full_frame)
        frame_count += 1

        now = time.time()
        if now - last_calc >= 1.0:
            state.fps = frame_count / (now - last_calc)
            frame_count = 0
            last_calc = now

        try:
            packets = codec.parse(full_frame)
            for pkt in packets:
                try:
                    frames = codec.decode(pkt)
                    for f in frames:
                        state.last_frame_time = time.time()
                        bgr = f.to_ndarray(format="bgr24")
                        if bgr.shape[1] != state.width or bgr.shape[0] != state.height:
                            bgr = cv2.resize(bgr, (state.width, state.height))

                        # Render small OSD badge
                        if state.show_osd and time.time() - state.last_frame_time < 5.0:
                            cv2.rectangle(bgr, (20, 20), (520, 80), (0, 0, 0), -1)
                            cv2.putText(bgr, f"{state.display_id} | {state.fps:.1f} FPS | {state.last_nal_type}", (30, 45),
                                         cv2.FONT_HERSHEY_SIMPLEX, 0.6, (0, 255, 0), 2)
                            cv2.putText(bgr, f"1920x540 | UDP :{state.udp_port} | TS: {state.last_ts}ms", (30, 70),
                                         cv2.FONT_HERSHEY_SIMPLEX, 0.45, (200, 200, 200), 1)

                        _, jpeg = cv2.imencode(".jpg", bgr, [cv2.IMWRITE_JPEG_QUALITY, 80])
                        with state.lock:
                            state.latest_jpeg = jpeg.tobytes()
                except Exception:
                    pass
        except Exception:
            pass

    assembler = FrameAssembler(on_frame)

    while True:
        try:
            data, _ = sock.recvfrom(2048)
            state.packets_received += 1
            assembler.on_packet(data)
        except socket.timeout:
            # Standby if no packets received for 2 seconds
            if state.last_frame_time > 0 and time.time() - state.last_frame_time > 2.0:
                with state.lock:
                    state.latest_jpeg = create_standby_frame(state)
        except Exception:
            break


def main():
    parser = argparse.ArgumentParser(description="booth-display Client Simulator")
    parser.add_argument("--id", type=str, default="display-1", help="Display ID")
    parser.add_argument("--port", type=int, default=8554, help="UDP listening port")
    parser.add_argument("--web-port", type=int, default=9001, help="Web UI HTTP port")
    args = parser.parse_args()

    state = SimulatorState(display_id=args.id, udp_port=args.port)
    state.latest_jpeg = create_standby_frame(state)

    # Start UDP receiver in background
    t = threading.Thread(target=udp_worker, args=(state,), daemon=True)
    t.start()

    # Web server
    handler_class = type("Handler", (StreamingHandler,), {"state": state})
    server = http.server.ThreadingHTTPServer(("0.0.0.0", args.web_port), handler_class)
    sys.stderr.write(f"\n[Simulator] Display '{args.id}' started!\n")
    sys.stderr.write(f"  - UDP Stream Receiver:  0.0.0.0:{args.port}\n")
    sys.stderr.write(f"  - Browser View URL:     http://localhost:{args.web_port}/\n\n")

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()

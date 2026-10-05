#!/usr/bin/env python3
"""
ndi_bridge.py
NDI Source discovery and receiver bridge for booth-display controller.
Uses cyndilib via uv.

Modes:
  1. Discovery: python ndi_bridge.py --discover
     Outputs JSON array of available NDI sources to stdout.
  2. Stream: python ndi_bridge.py --source <source_name> --width <w> --height <h> --fps <fps>
     Receives NDI frames, resizes to target canvas (w x h), and streams raw BGRX frames to stdout.
"""
from __future__ import annotations

import argparse
import json
import os
import signal
import sys
import time

try:
    from cyndilib.finder import Finder
    from cyndilib.receiver import Receiver
    from cyndilib.video_frame import VideoFrameSync
    from cyndilib.audio_frame import AudioFrameSync
    from cyndilib.wrapper.ndi_recv import RecvColorFormat, RecvBandwidth
    import numpy as np
    import cv2
except ImportError as e:
    sys.stderr.write(f"Failed to import cyndilib or dependencies: {e}\n")
    sys.exit(1)


def discover_sources(timeout_sec: float = 1.0) -> list[dict]:
    finder = Finder()
    sources = []
    end_time = time.time() + timeout_sec
    while time.time() < end_time:
        finder.wait_for_sources(0)
        curr = list(finder)
        if curr:
            sources = curr
            time.sleep(0.3)
            finder.wait_for_sources(0)
            sources = list(finder)
            break
        time.sleep(0.1)

    result = []
    for s in sources:
        name = getattr(s, "name", "")
        stream_name = getattr(s, "stream_name", None)
        host_name = getattr(s, "host_name", None)
        if name:
            result.append({
                "name": name,
                "stream_name": stream_name,
                "host_name": host_name,
            })
    return result


def calc_crop_rect(src_w: int, src_h: int, target_width: int, target_height: int, crop_mode: str = "center") -> tuple[int, int, int, int]:
    """Calculate crop coordinates (x, y, w, h) for input dimensions."""
    if crop_mode != "center":
        return 0, 0, src_w, src_h

    src_aspect = src_w / src_h
    target_aspect = target_width / target_height

    if abs(src_aspect - target_aspect) < 1e-4:
        return 0, 0, src_w, src_h

    if src_aspect < target_aspect:
        # Source is taller than target -> crop top and bottom
        crop_w = src_w
        crop_h = int(round(src_w / target_aspect))
        crop_h = max(1, min(src_h, crop_h))
        crop_x = 0
        crop_y = (src_h - crop_h) // 2
    else:
        # Source is wider than target -> crop left and right
        crop_h = src_h
        crop_w = int(round(src_h * target_aspect))
        crop_w = max(1, min(src_w, crop_w))
        crop_x = (src_w - crop_w) // 2
        crop_y = 0

    return crop_x, crop_y, crop_w, crop_h


def stream_source(source_name: str, width: int, height: int, fps: int, crop_mode: str = "center"):
    # Ensure multi-threaded OpenCV for fast parallel resizing
    try:
        cv2.setNumThreads(4)
    except Exception:
        pass

    # Set stdout to binary non-buffered mode
    stdout = sys.stdout.buffer

    finder = Finder()
    matched = None
    sys.stderr.write(f"[ndi_bridge] Searching for NDI source '{source_name}'...\n")

    for _ in range(50):
        finder.wait_for_sources(0)
        for s in finder:
            if s.name == source_name or getattr(s, "stream_name", "") == source_name or source_name in s.name:
                matched = s
                break
        if matched is not None:
            break
        time.sleep(0.1)

    if matched is None:
        sys.stderr.write(f"[ndi_bridge] Error: Source '{source_name}' not found on network.\n")
        sys.exit(2)

    sys.stderr.write(f"[ndi_bridge] Connected to NDI source: {matched.name}\n")

    receiver = Receiver(
        color_format=RecvColorFormat.BGRX_BGRA,
        bandwidth=RecvBandwidth.highest,
    )
    vf = VideoFrameSync()
    receiver.frame_sync.set_video_frame(vf)

    # Attach AudioFrameSync to immediately drain and drop audio samples.
    # When audio samples are left unconsumed, NDIlib_framesync's internal queue
    # overflows ("Unrepairable overflow!"), breaking A/V sync and adding massive latency.
    af = AudioFrameSync()
    receiver.frame_sync.set_audio_frame(af)

    receiver.set_source(matched)

    # Pre-allocate output buffer and 1D memoryview to eliminate per-frame GC allocations
    out_buf = np.empty((height, width, 4), dtype=np.uint8)
    out_view = out_buf.data.cast('B')

    frame_interval = 1.0 / max(1, fps)
    next_frame_time = time.perf_counter()

    cached_res: tuple[int, int] | None = None
    crop_rect: tuple[int, int, int, int] = (0, 0, width, height)

    running = True

    def sig_handler(sig, frame):
        nonlocal running
        running = False

    signal.signal(signal.SIGINT, sig_handler)
    signal.signal(signal.SIGTERM, sig_handler)

    last_frame_id: tuple[float, float] | None = None
    last_output_time = 0.0
    max_idle_sec = 0.05  # Keep-alive frame interval (20fps minimum fallback)

    while running:
        try:
            # Drain and drop all pending audio samples immediately to keep NDI internal latency at zero
            if receiver.frame_sync.audio_samples_available() > 0:
                receiver.frame_sync.capture_available_audio()

            receiver.frame_sync.capture_video()
            w, h = vf.get_resolution()
            data_size = vf.get_data_size()

            if w > 0 and h > 0 and data_size > 0:
                frame_id = (vf.get_timecode_posix(), vf.get_timestamp_posix())
                now = time.perf_counter()

                # Process immediately when a new NDI frame arrives (or on fallback keep-alive)
                if frame_id != last_frame_id or (now - last_output_time >= max_idle_sec):
                    last_frame_id = frame_id
                    last_output_time = now

                    arr = None
                    try:
                        # Zero-copy view into frame memory buffer
                        arr = np.asarray(vf).reshape((h, w, 4))

                        if w == width and h == height:
                            np.copyto(out_buf, arr)
                        else:
                            if cached_res != (w, h):
                                cached_res = (w, h)
                                crop_rect = calc_crop_rect(w, h, width, height, crop_mode=crop_mode)

                            cx, cy, cw, ch = crop_rect
                            cv2.resize(arr[cy : cy + ch, cx : cx + cw], (width, height), dst=out_buf, interpolation=cv2.INTER_LINEAR)
                    finally:
                        # Release view buffer immediately so next capture_video doesn't fail with 'cannot write with view active'
                        arr = None

                    stdout.write(out_view)
                    stdout.flush()
                else:
                    # New frame not yet arrived from NDI; sleep sub-millisecond to avoid high CPU while polling
                    time.sleep(0.0005)
            else:
                # Waiting for frame from NDI source
                time.sleep(0.001)
        except (BrokenPipeError, IOError):
            break
        except Exception as e:
            sys.stderr.write(f"[ndi_bridge] Capture error: {e}\n")
            time.sleep(0.005)


def main():
    parser = argparse.ArgumentParser(description="booth-display NDI bridge")
    parser.add_argument("--discover", action="store_true", help="Discover available NDI sources and output JSON")
    parser.add_argument("--source", type=str, default="", help="NDI source name to stream")
    parser.add_argument("--width", type=int, default=5792, help="Output canvas width")
    parser.add_argument("--height", type=int, default=540, help="Output canvas height")
    parser.add_argument("--fps", type=int, default=30, help="Output frame rate")
    parser.add_argument("--crop-mode", type=str, default="center", choices=["center", "stretch"], help="Crop mode: 'center' (default) or 'stretch'")
    args = parser.parse_args()

    if args.discover:
        sources = discover_sources()
        print(json.dumps(sources))
        sys.stdout.flush()
        os._exit(0)

    if not args.source:
        sys.stderr.write("Error: --source is required for streaming.\n")
        os._exit(1)

    stream_source(args.source, args.width, args.height, args.fps, args.crop_mode)
    os._exit(0)


if __name__ == "__main__":
    main()

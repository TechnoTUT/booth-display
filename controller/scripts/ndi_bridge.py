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


def crop_and_resize(arr: np.ndarray, target_width: int, target_height: int, crop_mode: str = "center") -> np.ndarray:
    """
    Resizes or crops input frame to target canvas size.
    Modes:
      - 'center': Crops input frame around center to match target aspect ratio, then resizes.
      - 'stretch': Stretches input frame to target size without maintaining aspect ratio.
    """
    src_h, src_w = arr.shape[:2]
    if src_w == target_width and src_h == target_height:
        return arr

    if crop_mode != "center":
        return cv2.resize(arr, (target_width, target_height), interpolation=cv2.INTER_LINEAR)

    src_aspect = src_w / src_h
    target_aspect = target_width / target_height

    if abs(src_aspect - target_aspect) < 1e-4:
        return cv2.resize(arr, (target_width, target_height), interpolation=cv2.INTER_LINEAR)

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

    cropped = arr[crop_y : crop_y + crop_h, crop_x : crop_x + crop_w]
    return cv2.resize(cropped, (target_width, target_height), interpolation=cv2.INTER_LINEAR)


def stream_source(source_name: str, width: int, height: int, fps: int, crop_mode: str = "center"):
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
    receiver.set_source(matched)

    frame_interval = 1.0 / max(1, fps)
    last_frame_time = time.time()

    running = True

    def sig_handler(sig, frame):
        nonlocal running
        running = False

    signal.signal(signal.SIGINT, sig_handler)
    signal.signal(signal.SIGTERM, sig_handler)

    while running and receiver.is_connected():
        now = time.time()
        elapsed = now - last_frame_time
        if elapsed < frame_interval:
            time.sleep(max(0.001, frame_interval - elapsed))
            continue

        try:
            receiver.frame_sync.capture_video()
            w, h = vf.get_resolution()
            data_size = vf.get_data_size()

            if w > 0 and h > 0 and data_size > 0:
                raw_data = bytes(vf)
                arr = np.frombuffer(raw_data, dtype=np.uint8, count=w * h * 4).reshape((h, w, 4))

                if w != width or h != height:
                    processed = crop_and_resize(arr, width, height, crop_mode=crop_mode)
                    out_bytes = processed.tobytes()
                else:
                    out_bytes = raw_data

                stdout.write(out_bytes)
                stdout.flush()
                last_frame_time = time.time()
            else:
                time.sleep(0.005)
        except (BrokenPipeError, IOError):
            break
        except Exception as e:
            sys.stderr.write(f"[ndi_bridge] Capture error: {e}\n")
            time.sleep(0.05)


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

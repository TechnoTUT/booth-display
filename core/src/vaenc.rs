//! The native VA-API encoder in the pipeline: one thread per display takes
//! every canvas frame from the mixer, converts its crop to NV12 straight into
//! the VA surface, encodes it and hands the access unit to the display's
//! sender. No ffmpeg process, no pipe copies of the canvas, and the displays
//! encode in parallel.

use crate::config::{Canvas, DisplayConfig};
use crate::frame::Frame;
use crate::nv12::bgrx_to_nv12;
use crate::protocol::PAYLOAD_H264;
use crate::sender::DisplaySender;
use crate::vaapi::h264::{Encoder, Params};
use anyhow::{bail, Result};
use crossbeam_channel::{bounded, select, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;
use std::time::Instant;

/// Same rate control as the ffmpeg h264_vaapi encoder had: 12 Mbps CBR per
/// display with a 1 Mbit HRD buffer (3 displays stay well inside 100 Mbps).
const BITRATE: u32 = 12_000_000;
const BUFFER_BITS: u32 = 1_000_000;

pub struct NativeRun {
    stop: Option<Sender<()>>,
    threads: Vec<JoinHandle<()>>,
}

/// Opens one encoder per display (so a driver problem fails here, before
/// anything runs) and starts the encode threads.
pub fn start(
    canvas: Canvas,
    displays: &[DisplayConfig],
    senders: &[Arc<DisplaySender>],
    device: &str,
    subscribe: &dyn Fn() -> Receiver<Frame>,
    exit_err: Arc<Mutex<Option<String>>>,
) -> Result<NativeRun> {
    let mut encoders = Vec::with_capacity(displays.len());
    for d in displays {
        let (w, h) = (d.width, d.height);
        if w <= 0 || h <= 0 || w % 2 != 0 || h % 2 != 0 {
            bail!("display {}: size {w}x{h} must be positive and even", d.id);
        }
        if d.crop_x < 0 || d.crop_y < 0 || (d.crop_x + w) as usize > canvas.width || (d.crop_y + h) as usize > canvas.height {
            bail!("display {}: crop {w}x{h} at {},{} is outside the {}x{} canvas", d.id, d.crop_x, d.crop_y, canvas.width, canvas.height);
        }
        let p = Params {
            width: w as usize,
            height: h as usize,
            fps: canvas.fps,
            bitrate: BITRATE,
            buffer_bits: BUFFER_BITS,
            gop: canvas.fps,
        };
        encoders.push(Encoder::new(device, p).map_err(|e| anyhow::anyhow!("display {}: {e:#}", d.id))?);
    }
    if let Some(e) = encoders.first() {
        eprintln!("[encoder] VA-API: {}", e.describe());
    }

    let (stop_tx, stop_rx) = bounded::<()>(0);
    let mut threads = Vec::new();
    for ((mut enc, d), sender) in encoders.into_iter().zip(displays).zip(senders) {
        let (frames, stop, sender, exit_err) = (subscribe(), stop_rx.clone(), sender.clone(), exit_err.clone());
        let (id, x0, y0, h) = (d.id.clone(), d.crop_x as usize, d.crop_y as usize, d.height as usize);
        let stride = canvas.width * 4;
        threads.push(std::thread::Builder::new().name(format!("vaenc-{id}")).spawn(move || {
            let start = Instant::now();
            loop {
                let f = select! {
                    recv(frames) -> f => match f { Ok(f) => f, Err(_) => return },
                    recv(stop) -> _ => return,
                };
                match enc.encode(|planes| bgrx_to_nv12(&f, stride, x0, y0, h, planes)) {
                    Ok((au, key)) => sender.send_frame(au, PAYLOAD_H264, key, start.elapsed().as_millis() as u32),
                    Err(e) => {
                        let msg = format!("encoder stopped: display {id}: {e:#}");
                        eprintln!("[encoder] {msg}");
                        *exit_err.lock().unwrap() = Some(msg);
                        return;
                    }
                }
            }
        })?);
    }
    Ok(NativeRun { stop: Some(stop_tx), threads })
}

impl Drop for NativeRun {
    fn drop(&mut self) {
        self.stop.take();
        for t in self.threads.drain(..) {
            let _ = t.join();
        }
    }
}


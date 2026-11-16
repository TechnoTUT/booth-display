//! The in-process VA-API encoder in the pipeline: one VA display and encoder
//! per display, driven by the shared encode threads (`native`).

use crate::config::{Canvas, DisplayConfig};
use crate::frame::Frame;
use crate::native::{check_display, NativeRun};
use crate::sender::DisplaySender;
use crate::vaapi::h264::{Encoder, Params};
use anyhow::Result;
use crossbeam_channel::Receiver;
use std::sync::{Arc, Mutex};

/// Same rate control as the ffmpeg h264_vaapi encoder had: 12 Mbps CBR per
/// display with a 1 Mbit HRD buffer (3 displays stay well inside 100 Mbps).
const BITRATE: u32 = 12_000_000;
const BUFFER_BITS: u32 = 1_000_000;

fn params(canvas: Canvas, d: &DisplayConfig) -> Params {
    Params {
        width: d.width as usize,
        height: d.height as usize,
        fps: canvas.fps,
        bitrate: BITRATE,
        buffer_bits: BUFFER_BITS,
        gop: canvas.fps,
    }
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
        check_display(canvas, d)?;
        encoders.push(Encoder::new(device, params(canvas, d)).map_err(|e| anyhow::anyhow!("display {}: {e:#}", d.id))?);
    }
    if let Some(e) = encoders.first() {
        eprintln!("[encoder] VA-API: {}", e.describe());
    }
    crate::native::spawn("vaenc", canvas, displays, encoders, senders, subscribe, exit_err)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Canvas frame handed to the encoder -> access unit ready, for the three
    /// 1920x540 displays of the default canvas encoded in parallel, plus the
    /// encode threads' CPU time. Opt-in, like encoder::tests::latency:
    ///
    ///     LATENCY_TEST=1 cargo test --release vaenc::tests::latency -- --nocapture
    ///
    /// LIBVA_DRIVER_NAME=i965 uses the Ivy Bridge driver.
    #[test]
    fn latency() {
        if std::env::var_os("LATENCY_TEST").is_none() {
            eprintln!("set LATENCY_TEST=1 to run");
            return;
        }
        let canvas = Canvas { width: 5760, height: 540, fps: 30 };
        let d = DisplayConfig { width: 1920, height: 540, ..Default::default() };
        let encoders: Vec<Encoder> =
            (0..3).map(|_| Encoder::new("/dev/dri/renderD128", params(canvas, &d)).expect("VA-API encoder")).collect();
        eprintln!("encoder: {}", encoders[0].describe());
        crate::native::tests::measure_latency("in-process VA-API", encoders);
    }
}

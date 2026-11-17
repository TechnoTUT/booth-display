//! The in-process libx264 encoder in the pipeline (the software encoder): one
//! x264 instance per display, driven by the shared encode threads (`native`).

use crate::config::{Canvas, DisplayConfig};
use crate::frame::Frame;
use crate::native::{check_display, NativeRun};
use crate::sender::DisplaySender;
use crate::x264::{Encoder, Params};
use anyhow::Result;
use crossbeam_channel::Receiver;
use std::sync::{Arc, Mutex};

/// Same rate control as the ffmpeg libx264 command had: 4 Mbps with a 400 kbit
/// VBV buffer per display.
const BITRATE_KBPS: u32 = 4000;
const BUFFER_KBITS: u32 = 400;

fn params(canvas: Canvas, d: &DisplayConfig) -> Params {
    Params {
        width: d.width as usize,
        height: d.height as usize,
        fps: canvas.fps,
        bitrate_kbps: BITRATE_KBPS,
        buffer_kbits: BUFFER_KBITS,
        gop: canvas.fps,
    }
}

/// Opens one encoder per display (so a missing libx264 fails here, before
/// anything runs) and starts the encode threads.
pub fn start(
    canvas: Canvas,
    displays: &[DisplayConfig],
    senders: &[Arc<DisplaySender>],
    subscribe: &dyn Fn() -> Receiver<Frame>,
    exit_err: Arc<Mutex<Option<String>>>,
) -> Result<NativeRun> {
    let mut encoders = Vec::with_capacity(displays.len());
    for d in displays {
        check_display(canvas, d)?;
        encoders.push(Encoder::new(params(canvas, d)).map_err(|e| anyhow::anyhow!("display {}: {e:#}", d.id))?);
    }
    if let Some(e) = encoders.first() {
        eprintln!("[encoder] {}", e.describe());
    }
    crate::native::spawn("x264", canvas, displays, encoders, senders, subscribe, exit_err)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Like vaenc::tests::latency, for libx264. Opt-in, machine dependent:
    ///
    ///     LATENCY_TEST=1 cargo test --release x264enc::tests::latency -- --nocapture
    #[test]
    fn latency() {
        if std::env::var_os("LATENCY_TEST").is_none() {
            eprintln!("set LATENCY_TEST=1 to run");
            return;
        }
        let canvas = Canvas { width: 5760, height: 540, fps: 30 };
        let d = DisplayConfig { width: 1920, height: 540, ..Default::default() };
        let encoders: Vec<Encoder> = (0..3).map(|_| Encoder::new(params(canvas, &d)).expect("libx264 encoder")).collect();
        eprintln!("encoder: {}", encoders[0].describe());
        crate::native::tests::measure_latency("in-process libx264", encoders);
    }

    /// Highest sustained frame rate, see native::tests::measure_throughput.
    /// Opt-in, machine dependent:
    ///
    ///     THROUGHPUT_TEST=1 cargo test --release x264enc::tests::throughput -- --nocapture
    ///
    /// THROUGHPUT_DISPLAYS=1..3 sets the number of parallel encoders (default 3).
    #[test]
    fn throughput() {
        if std::env::var_os("THROUGHPUT_TEST").is_none() {
            eprintln!("set THROUGHPUT_TEST=1 to run");
            return;
        }
        let canvas = Canvas { width: 5760, height: 540, fps: 30 };
        let d = DisplayConfig { width: 1920, height: 540, ..Default::default() };
        let n = crate::native::tests::throughput_displays();
        let encoders: Vec<Encoder> = (0..n).map(|_| Encoder::new(params(canvas, &d)).expect("libx264 encoder")).collect();
        eprintln!("encoder: {}", encoders[0].describe());
        crate::native::tests::measure_throughput("in-process libx264", encoders);
    }
}

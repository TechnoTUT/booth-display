//! Low-rate JPEG preview of the whole canvas for the Web GUI: every frame the
//! mixer renders is thinned to PREVIEW_FPS, downscaled to PREVIEW_WIDTH (aspect
//! kept, e.g. 5760x540 -> 1280x120) and JPEG-encoded in-process.

use crate::config::Canvas;
use crate::frame::Frame;
use crate::scale::Scaler;
use crossbeam_channel::Receiver;
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

const PREVIEW_WIDTH: usize = 1280;
const PREVIEW_FPS: u32 = 15;
const JPEG_QUALITY: u8 = 75;

pub struct Preview {
    thread: Option<JoinHandle<()>>,
}

impl Preview {
    /// Encodes frames from `frames` until the stream ends, handing each JPEG to `out`.
    pub fn start(canvas: Canvas, frames: Receiver<Frame>, out: impl Fn(Vec<u8>) + Send + 'static) -> anyhow::Result<Self> {
        let w = PREVIEW_WIDTH.min(canvas.width);
        let h = ((canvas.height * w + canvas.width / 2) / canvas.width).max(1);
        let thread = std::thread::Builder::new().name("preview".into()).spawn(move || {
            // One worker: the preview must not take cores from the stream.
            let mut scaler = Scaler::bgrx(canvas.width, canvas.height, w, h, false, 1);
            let mut small = vec![0u8; w * h * 4];
            let gap = Duration::from_secs(1) / PREVIEW_FPS * 9 / 10;
            let mut last: Option<Instant> = None;
            for f in frames.iter() {
                if last.is_some_and(|t| t.elapsed() < gap) {
                    continue;
                }
                last = Some(Instant::now());
                scaler.scale(&mut small, &f, canvas.width * 4);
                drop(f);
                let mut jpeg = Vec::with_capacity(64 * 1024);
                let enc = jpeg_encoder::Encoder::new(&mut jpeg, JPEG_QUALITY);
                match enc.encode(&small, w as u16, h as u16, jpeg_encoder::ColorType::Bgra) {
                    Ok(()) => out(jpeg),
                    Err(e) => {
                        eprintln!("[preview] JPEG encoding failed: {e}");
                        return;
                    }
                }
            }
        })?;
        Ok(Self { thread: Some(thread) })
    }
}

impl Drop for Preview {
    /// The thread ends when the mixer closes the frame stream.
    fn drop(&mut self) {
        if let Some(t) = self.thread.take() {
            let _ = t.join();
        }
    }
}

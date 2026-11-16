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

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    /// CPU time (user + system) of the calling thread.
    pub fn thread_cpu() -> Duration {
        // SAFETY: getrusage fills the struct.
        let mut ru: libc::rusage = unsafe { std::mem::zeroed() };
        unsafe { libc::getrusage(libc::RUSAGE_THREAD, &mut ru) };
        let tv = |t: libc::timeval| Duration::new(t.tv_sec as u64, t.tv_usec as u32 * 1000);
        tv(ru.ru_utime) + tv(ru.ru_stime)
    }

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
        const FRAMES: usize = 150;
        let canvas = Canvas { width: 5760, height: 540, fps: 30 };
        let p = Params { width: 1920, height: 540, fps: 30, bitrate: BITRATE, buffer_bits: BUFFER_BITS, gop: 30 };
        let mut txs = Vec::new();
        let mut threads = Vec::new();
        for d in 0..3usize {
            let (tx, rx) = bounded::<(Arc<Vec<u8>>, Instant)>(2);
            txs.push(tx);
            let mut enc = Encoder::new("/dev/dri/renderD128", p).expect("VA-API encoder");
            if d == 0 {
                eprintln!("encoder: {}", enc.describe());
            }
            threads.push(std::thread::spawn(move || {
                let (mut lat, cpu0) = (Vec::new(), thread_cpu());
                for (f, sent) in rx.iter() {
                    enc.encode(|planes| bgrx_to_nv12(&f, canvas.width * 4, d * 1920, 0, 540, planes)).unwrap();
                    lat.push(sent.elapsed().as_secs_f64() * 1000.0);
                }
                (lat, thread_cpu() - cpu0)
            }));
        }
        let start = Instant::now();
        for i in 0..FRAMES {
            let due = start + Duration::from_secs(1) * i as u32 / 30;
            std::thread::sleep(due.saturating_duration_since(Instant::now()));
            let mut frame = vec![0u8; canvas.frame_size()];
            for (j, px) in frame.chunks_exact_mut(4).enumerate() {
                px[0] = (i * 3 + (j >> 2)) as u8;
                px[1] = (i + (j >> 8)) as u8;
            }
            let (f, sent) = (Arc::new(frame), Instant::now());
            for tx in &txs {
                tx.send((f.clone(), sent)).unwrap();
            }
        }
        drop(txs);
        let wall = Duration::from_secs(1) * FRAMES as u32 / 30;
        let mut cpu = Duration::ZERO;
        let mut all = Vec::new();
        for t in threads {
            let (lat, c) = t.join().unwrap();
            assert_eq!(lat.len(), FRAMES);
            cpu += c;
            all.extend_from_slice(&lat[15..]);
        }
        all.sort_by(|a, b| a.partial_cmp(b).unwrap());
        eprintln!(
            "in-process VA-API, 3 displays: frame -> access unit median {:.1} ms, p90 {:.1} ms; encode threads CPU {:.0}% of one core",
            all[all.len() / 2],
            all[all.len() * 9 / 10],
            cpu.as_secs_f64() / wall.as_secs_f64() * 100.0
        );
    }
}

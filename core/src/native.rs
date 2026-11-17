//! In-process encoding in the pipeline, shared by the VA-API (`vaenc`) and
//! libx264 (`x264enc`) encoders: one thread per display takes every canvas
//! frame from the mixer, converts its crop to NV12 straight into the encoder's
//! input, encodes it and hands the access unit to the display's sender. No
//! ffmpeg process, no pipe copies of the canvas, and the displays encode in
//! parallel.

use crate::config::{Canvas, DisplayConfig};
use crate::frame::Frame;
use crate::nv12::{bgrx_to_nv12, Nv12Planes};
use crate::protocol::{now_us, PAYLOAD_H264};
use crate::sender::DisplaySender;
use anyhow::{bail, Result};
use crossbeam_channel::{bounded, select, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;

/// An H.264 encoder for one display, driven by its own thread.
pub trait FrameEncoder: Send + 'static {
    /// Fills the input picture through `fill` and encodes it, as an IDR when
    /// `force_idr`. Returns the Annex-B access unit (possibly empty) and
    /// whether it is an IDR.
    fn encode_frame(&mut self, fill: &mut dyn FnMut(&mut Nv12Planes), force_idr: bool) -> Result<(&[u8], bool)>;
}

impl FrameEncoder for crate::vaapi::h264::Encoder {
    fn encode_frame(&mut self, fill: &mut dyn FnMut(&mut Nv12Planes), force_idr: bool) -> Result<(&[u8], bool)> {
        self.encode(fill, force_idr)
    }
}

impl FrameEncoder for crate::x264::Encoder {
    fn encode_frame(&mut self, fill: &mut dyn FnMut(&mut Nv12Planes), force_idr: bool) -> Result<(&[u8], bool)> {
        self.encode(fill, force_idr)
    }
}

/// Checks that a display's crop is even-sized and inside the canvas.
pub fn check_display(canvas: Canvas, d: &DisplayConfig) -> Result<()> {
    let (w, h) = (d.width, d.height);
    if w <= 0 || h <= 0 || w % 2 != 0 || h % 2 != 0 {
        bail!("display {}: size {w}x{h} must be positive and even", d.id);
    }
    if d.crop_x < 0 || d.crop_y < 0 || (d.crop_x + w) as usize > canvas.width || (d.crop_y + h) as usize > canvas.height {
        bail!("display {}: crop {w}x{h} at {},{} is outside the {}x{} canvas", d.id, d.crop_x, d.crop_y, canvas.width, canvas.height);
    }
    Ok(())
}

pub struct NativeRun {
    stop: Option<Sender<()>>,
    threads: Vec<JoinHandle<()>>,
}

/// Starts one encode thread per display with the already opened `encoders`
/// (one per display, in order). `label` names the threads and log lines.
pub fn spawn<E: FrameEncoder>(
    label: &'static str,
    canvas: Canvas,
    displays: &[DisplayConfig],
    encoders: Vec<E>,
    senders: &[Arc<DisplaySender>],
    subscribe: &dyn Fn() -> Receiver<Frame>,
    exit_err: Arc<Mutex<Option<String>>>,
) -> Result<NativeRun> {
    let (stop_tx, stop_rx) = bounded::<()>(0);
    let mut threads = Vec::new();
    for ((mut enc, d), sender) in encoders.into_iter().zip(displays).zip(senders) {
        let (frames, stop, sender, exit_err) = (subscribe(), stop_rx.clone(), sender.clone(), exit_err.clone());
        let (id, x0, y0, h) = (d.id.clone(), d.crop_x as usize, d.crop_y as usize, d.height as usize);
        let stride = canvas.width * 4;
        threads.push(std::thread::Builder::new().name(format!("{label}-{id}")).spawn(move || {
            loop {
                let f = select! {
                    recv(frames) -> f => match f { Ok(f) => f, Err(_) => return },
                    recv(stop) -> _ => return,
                };
                // The frame's timestamp: when the canvas frame reached the encoder.
                let ts = now_us();
                let force_idr = sender.take_keyframe_request();
                match enc.encode_frame(&mut |planes| bgrx_to_nv12(&f, stride, x0, y0, h, planes), force_idr) {
                    Ok((au, _)) if au.is_empty() => {}
                    Ok((au, key)) => sender.send_frame(au, PAYLOAD_H264, key, ts),
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
pub mod tests {
    use super::*;
    use crossbeam_channel::bounded;
    use std::time::{Duration, Instant};

    /// CPU time (user + system) of the calling thread.
    pub fn thread_cpu() -> Duration {
        // SAFETY: getrusage fills the struct.
        let mut ru: libc::rusage = unsafe { std::mem::zeroed() };
        unsafe { libc::getrusage(libc::RUSAGE_THREAD, &mut ru) };
        let tv = |t: libc::timeval| Duration::new(t.tv_sec as u64, t.tv_usec as u32 * 1000);
        tv(ru.ru_utime) + tv(ru.ru_stime)
    }

    /// Canvas frame handed to the encoders -> access unit ready, for the three
    /// 1920x540 displays of the default canvas encoded in parallel, plus the
    /// encode threads' CPU time. Prints one summary line headed by `label`.
    pub fn measure_latency<E: FrameEncoder>(label: &str, encoders: Vec<E>) {
        const FRAMES: usize = 150;
        let canvas = Canvas { width: 5760, height: 540, fps: 30 };
        let mut txs = Vec::new();
        let mut threads = Vec::new();
        for (d, mut enc) in encoders.into_iter().enumerate() {
            let (tx, rx) = bounded::<(Arc<Vec<u8>>, Instant)>(2);
            txs.push(tx);
            threads.push(std::thread::spawn(move || {
                let (mut lat, cpu0) = (Vec::new(), thread_cpu());
                for (f, sent) in rx.iter() {
                    enc.encode_frame(&mut |planes| bgrx_to_nv12(&f, canvas.width * 4, d * 1920, 0, 540, planes), false).unwrap();
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
            "{label}, 3 displays: frame -> access unit median {:.1} ms, p90 {:.1} ms; encode threads CPU {:.0}% of one core",
            all[all.len() / 2],
            all[all.len() * 9 / 10],
            cpu.as_secs_f64() / wall.as_secs_f64() * 100.0
        );
    }

    /// Number of displays for the throughput tests: `THROUGHPUT_DISPLAYS`
    /// (1 to 3, the displays of the default canvas), 3 when unset.
    pub fn throughput_displays() -> usize {
        match std::env::var("THROUGHPUT_DISPLAYS") {
            Err(_) => 3,
            Ok(v) => match v.parse() {
                Ok(n @ 1..=3) => n,
                _ => panic!("THROUGHPUT_DISPLAYS must be 1, 2 or 3, got {v:?}"),
            },
        }
    }

    /// Highest frame rate the encoders sustain: each encoder gets its own
    /// 1920x540 crop of the default canvas and encodes frames back to back,
    /// with no 30 fps pacing, all encoders in parallel. Frames come from a
    /// small pre-built pool so frame generation is not measured. Prints one
    /// summary line headed by `label`: frames per second per display and the
    /// encode threads' CPU time (NV12 conversion included).
    pub fn measure_throughput<E: FrameEncoder>(label: &str, encoders: Vec<E>) {
        const FRAMES: usize = 300;
        let canvas = Canvas { width: 5760, height: 540, fps: 30 };
        assert!(encoders.len() * 1920 <= canvas.width);
        let pool: Arc<Vec<Vec<u8>>> = Arc::new(
            (0..8)
                .map(|i| {
                    let mut frame = vec![0u8; canvas.frame_size()];
                    for (j, px) in frame.chunks_exact_mut(4).enumerate() {
                        px[0] = (i * 3 + (j >> 2)) as u8;
                        px[1] = (i + (j >> 8)) as u8;
                    }
                    frame
                })
                .collect(),
        );
        let n = encoders.len();
        let start = Instant::now();
        let threads: Vec<_> = encoders
            .into_iter()
            .enumerate()
            .map(|(d, mut enc)| {
                let pool = pool.clone();
                std::thread::spawn(move || {
                    let cpu0 = thread_cpu();
                    for i in 0..FRAMES {
                        let f = &pool[i % pool.len()];
                        enc.encode_frame(&mut |planes| bgrx_to_nv12(f, canvas.width * 4, d * 1920, 0, 540, planes), false)
                            .unwrap();
                    }
                    thread_cpu() - cpu0
                })
            })
            .collect();
        let cpu: Duration = threads.into_iter().map(|t| t.join().unwrap()).sum();
        let wall = start.elapsed().as_secs_f64();
        eprintln!(
            "{label}, {n} display(s) unpaced: {:.1} fps per display; encode threads CPU {:.0}% of one core",
            FRAMES as f64 / wall,
            cpu.as_secs_f64() / wall * 100.0
        );
    }
}

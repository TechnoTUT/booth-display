//! One ffmpeg process reads the full-canvas raw BGRX stream once, splits it,
//! crops one region per display and encodes each to its own H.264 stream.
//! Every encoded stream comes back through an extra pipe (fd 3, 4, ...) as FLV
//! and goes to the matching display sender.
//!
//! Intel VA-API (h264_vaapi) keeps the CPU load minimal; libx264 is the
//! automatic fallback when VA-API is missing or fails to start.

use crate::config::{Canvas, DisplayConfig, EncoderMode, PipelineConfig};
use crate::flv::FlvReader;
use crate::frame::Frame;
use crate::protocol::PAYLOAD_H264;
use crate::sender::DisplaySender;
use crossbeam_channel::{bounded, Receiver};
use std::fs::File;
use std::io::Write;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
use std::os::unix::process::CommandExt;
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

/// How long a freshly started ffmpeg must keep running to count as healthy.
/// An encoder that cannot open its device (e.g. VA-API) exits within a few
/// hundred milliseconds.
const STARTUP_PROBE: Duration = Duration::from_secs(2);

/// Builds the ffmpeg arguments for the single-process, N-output encode.
pub fn build_args(canvas: Canvas, displays: &[DisplayConfig], vaapi: Option<&str>) -> Vec<String> {
    let fps = canvas.fps.to_string();
    let threads = crate::scale::default_workers().to_string();
    // Convert the canvas to 4:2:0 once, before splitting, instead of once per
    // display, and let the filter graph use every core. Measured on a
    // 3-display canvas: about 8 ms lower median latency and a much tighter p90.
    // (In 4:2:0 a crop offset is rounded down to an even pixel.)
    let pix_fmt = if vaapi.is_some() { "nv12" } else { "yuv420p" };
    let mut fc = format!("[0:v]format={pix_fmt},split={}", displays.len());
    for i in 0..displays.len() {
        fc += &format!("[s{i}]");
    }
    for (i, d) in displays.iter().enumerate() {
        let up = if vaapi.is_some() { ",hwupload" } else { "" };
        fc += &format!(";[s{i}]crop={}:{}:{}:{}{up}[o{i}]", d.width, d.height, d.crop_x, d.crop_y);
    }

    let mut a: Vec<String> = Vec::new();
    let mut push = |xs: &[&str]| a.extend(xs.iter().map(|s| s.to_string()));
    // No "-fflags nobuffer": with a rawvideo pipe input on ffmpeg 7 it holds
    // every frame back by one frame interval (measured: 15 ms -> 49 ms).
    push(&["-hide_banner", "-loglevel", "error", "-flags", "low_delay", "-probesize", "32", "-analyzeduration", "0"]);
    if let Some(dev) = vaapi {
        push(&["-init_hw_device", &format!("vaapi=va:{dev}"), "-filter_hw_device", "va"]);
    }
    push(&["-f", "rawvideo", "-pix_fmt", "bgr0", "-s", &format!("{}x{}", canvas.width, canvas.height)]);
    push(&["-r", &fps, "-i", "pipe:0", "-filter_threads", &threads, "-filter_complex", &fc]);
    for i in 0..displays.len() {
        push(&["-map", &format!("[o{i}]")]);
        if vaapi.is_some() {
            push(&[
                "-c:v", "h264_vaapi", "-profile:v", "constrained_baseline", "-async_depth", "1",
                "-rc_mode", "CBR", "-aud", "1", "-bf", "0", "-g", &fps,
                // idr_interval counts I-frames, not frames: 0 makes every GOP start
                // with an IDR (receivers resync on IDR).
                "-idr_interval", "0",
                // The iHD driver's CBR control starves quality when the HRD buffer is
                // tiny; a 1M buffer lets it spend the bits. 12 Mbps x 3 displays stays
                // well inside a 100 Mbps wired link.
                "-b:v", "12000k", "-maxrate", "12000k", "-bufsize", "1000k",
            ]);
        } else {
            push(&[
                "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
                "-b:v", "4000k", "-maxrate", "4000k", "-bufsize", "400k", "-g", &fps,
                // Receiver-friendly H.264: one slice per frame, AUD at every access
                // unit, in-band SPS/PPS, Baseline. threads=1: frame threading delays
                // every frame by (threads - 1) frames, and sliced threading ignores
                // slices=1. One thread per display keeps up easily at ultrafast.
                "-profile:v", "baseline", "-x264-params", "threads=1:slices=1:aud=1:repeat-headers=1",
            ]);
        }
        // FLV, not raw h264: each tag carries its size, so a frame can be
        // forwarded as soon as it is written (see FlvReader).
        push(&["-an", "-f", "flv", "-flvflags", "no_duration_filesize", &format!("pipe:{}", 3 + i)]);
    }
    a
}

/// Pipes whose write ends become fds 3, 4, ... in the child.
pub struct OutputPipes {
    pub readers: Vec<File>,
    writers: Vec<OwnedFd>,
}

impl OutputPipes {
    pub fn new(n: usize) -> std::io::Result<Self> {
        let (mut readers, mut writers) = (Vec::new(), Vec::new());
        for _ in 0..n {
            let mut fds = [0 as RawFd; 2];
            // SAFETY: fds is a valid out-array of two ints.
            if unsafe { libc::pipe2(fds.as_mut_ptr(), libc::O_CLOEXEC) } != 0 {
                return Err(std::io::Error::last_os_error());
            }
            // SAFETY: pipe2 returned two fresh fds that we now own.
            let (r, w) = unsafe { (File::from_raw_fd(fds[0]), OwnedFd::from_raw_fd(fds[1])) };
            // Move the write end above the target range 3..3+n so the dup2s
            // in the child never overwrite one another.
            // SAFETY: F_DUPFD_CLOEXEC on a valid fd.
            let high = unsafe { libc::fcntl(w.as_raw_fd(), libc::F_DUPFD_CLOEXEC, (3 + n) as libc::c_int) };
            if high < 0 {
                return Err(std::io::Error::last_os_error());
            }
            readers.push(r);
            // SAFETY: fresh fd from fcntl.
            writers.push(unsafe { OwnedFd::from_raw_fd(high) });
        }
        Ok(Self { readers, writers })
    }

    /// Makes the write ends fds 3.. in the child process.
    pub fn attach(&self, cmd: &mut Command) {
        let fds: Vec<RawFd> = self.writers.iter().map(|w| w.as_raw_fd()).collect();
        // SAFETY: only async-signal-safe calls (dup2) between fork and exec.
        unsafe {
            cmd.pre_exec(move || {
                for (i, &fd) in fds.iter().enumerate() {
                    // dup2 clears close-on-exec on the new descriptor.
                    if libc::dup2(fd, 3 + i as libc::c_int) < 0 {
                        return Err(std::io::Error::last_os_error());
                    }
                }
                Ok(())
            });
        }
    }

    /// Closes the parent's copies of the write ends (the child holds its own),
    /// so readers see EOF once ffmpeg exits.
    pub fn close_writers(&mut self) {
        self.writers.clear();
    }
}

struct Running {
    pid: i32,
    stopping: Arc<AtomicBool>,
    done: Receiver<()>,
    threads: Vec<JoinHandle<()>>,
}

#[derive(Default)]
pub struct Encoder {
    running: Option<Running>,
    /// The encoder running now: "vaapi", "vaapi-ffmpeg" or "libx264".
    kind: &'static str,
    /// The native VA-API encoder, when it runs instead of ffmpeg.
    native: Option<crate::native::NativeRun>,
    /// Why the encoder stopped although it was not asked to.
    exit_err: Arc<Mutex<Option<String>>>,
}

impl Encoder {
    /// Starts encoding. `subscribe` must return a fresh frame stream on every
    /// call, so a retry after a failed VA-API start begins at a frame boundary.
    pub fn start(
        &mut self,
        canvas: Canvas,
        displays: &[DisplayConfig],
        senders: &[Arc<DisplaySender>],
        cfg: &PipelineConfig,
        subscribe: &dyn Fn() -> Receiver<Frame>,
    ) -> anyhow::Result<()> {
        self.stop();
        if displays.is_empty() {
            return Ok(());
        }
        *self.exit_err.lock().unwrap() = None;
        match cfg.mode() {
            EncoderMode::Vaapi(dev) => {
                eprintln!("[encoder] starting hardware encoder: VA-API ({dev}), in-process");
                match crate::vaenc::start(canvas, displays, senders, &dev, subscribe, self.exit_err.clone()) {
                    Ok(run) => {
                        self.native = Some(run);
                        self.kind = "vaapi";
                        return Ok(());
                    }
                    Err(e) => eprintln!("[encoder] VA-API encoding failed: {e:#}. Falling back to software libx264."),
                }
            }
            EncoderMode::FfmpegVaapi(dev) => {
                eprintln!("[encoder] starting hardware encoder: ffmpeg h264_vaapi ({dev})");
                match self.spawn(canvas, displays, senders, Some(&dev), subscribe()) {
                    Ok(()) => {
                        self.kind = "vaapi-ffmpeg";
                        return Ok(());
                    }
                    Err(e) => eprintln!("[encoder] VA-API encoding failed: {e}. Falling back to software libx264."),
                }
            }
            EncoderMode::Software => {}
        }
        eprintln!("[encoder] starting software encoder: libx264 (ultrafast)");
        self.spawn(canvas, displays, senders, None, subscribe())?;
        self.kind = "libx264";
        Ok(())
    }

    fn spawn(
        &mut self,
        canvas: Canvas,
        displays: &[DisplayConfig],
        senders: &[Arc<DisplaySender>],
        vaapi: Option<&str>,
        frames: Receiver<Frame>,
    ) -> anyhow::Result<()> {
        *self.exit_err.lock().unwrap() = None;
        let mut pipes = OutputPipes::new(displays.len())?;
        let mut cmd = Command::new("ffmpeg");
        cmd.args(build_args(canvas, displays, vaapi))
            .stdin(Stdio::piped())
            .stdout(Stdio::null())
            .stderr(Stdio::inherit());
        pipes.attach(&mut cmd);
        let mut child = cmd.spawn().map_err(|e| anyhow::anyhow!("failed to start ffmpeg encoder: {e}"))?;
        pipes.close_writers();
        let pid = child.id() as i32;
        let mut stdin = child.stdin.take().expect("piped stdin");
        let stopping = Arc::new(AtomicBool::new(false));
        let mut threads = Vec::new();

        // Feed canvas frames to ffmpeg. Ends when the mixer drops the
        // subscription or ffmpeg goes away.
        threads.push(std::thread::Builder::new().name("encoder-feed".into()).spawn(move || {
            for f in frames.iter() {
                if stdin.write_all(&f).is_err() {
                    return;
                }
            }
        })?);

        let (done_tx, done) = bounded::<()>(0);
        {
            let (stopping, exit_err) = (stopping.clone(), self.exit_err.clone());
            threads.push(std::thread::Builder::new().name("encoder-wait".into()).spawn(move || {
                let status = child.wait();
                // Only an exit we did not ask for counts as a failure.
                if !stopping.load(Ordering::SeqCst) {
                    let why = match status {
                        Ok(s) => format!("ffmpeg exited unexpectedly ({s})"),
                        Err(e) => format!("ffmpeg wait failed: {e}"),
                    };
                    eprintln!("[encoder] encoder stopped: {why}");
                    *exit_err.lock().unwrap() = Some(format!("encoder stopped: {why}"));
                }
                drop(done_tx);
            })?);
        }

        for ((reader, d), sender) in pipes.readers.drain(..).zip(displays).zip(senders) {
            let (id, sender, stopping) = (d.id.clone(), sender.clone(), stopping.clone());
            threads.push(std::thread::Builder::new().name(format!("encoder-{id}")).spawn(move || {
                let mut fr = FlvReader::new(std::io::BufReader::with_capacity(256 * 1024, reader));
                let start = Instant::now();
                loop {
                    match fr.next_au() {
                        Ok(Some((au, key))) => {
                            let ts = start.elapsed().as_millis() as u32;
                            sender.send_frame(au, PAYLOAD_H264, key, ts);
                        }
                        Ok(None) => return,
                        Err(e) => {
                            if !stopping.load(Ordering::SeqCst) {
                                eprintln!("[{id}] encoder output read error: {e}");
                            }
                            return;
                        }
                    }
                }
            })?);
        }

        self.running = Some(Running { pid, stopping, done: done.clone(), threads });

        // Startup probe: an encoder that cannot open its device exits almost at once.
        if done.recv_timeout(STARTUP_PROBE).is_err_and(|e| e.is_disconnected()) {
            let err = self.err().unwrap_or_else(|| "ffmpeg exited during startup".into());
            self.stop();
            anyhow::bail!(err);
        }
        Ok(())
    }

    /// The running encoder ("vaapi", "vaapi-ffmpeg", "libx264"), or "" when stopped.
    pub fn kind(&self) -> &'static str {
        self.kind
    }

    /// Why the encoder stopped unexpectedly, or None while it is healthy.
    pub fn err(&self) -> Option<String> {
        self.exit_err.lock().unwrap().clone()
    }

    pub fn stop(&mut self) {
        self.native = None; // joins the encode threads
        self.kind = "";
        let Some(r) = self.running.take() else { return };
        r.stopping.store(true, Ordering::SeqCst);
        // Not reaped yet while `done` is open, so the pid is still ours.
        if r.done.try_recv().is_err_and(|e| e.is_empty()) {
            // SAFETY: plain kill(2).
            unsafe { libc::kill(r.pid, libc::SIGKILL) };
        }
        let _ = r.done.recv();
        for t in r.threads {
            let _ = t.join();
        }
    }
}

impl Drop for Encoder {
    fn drop(&mut self) {
        self.stop();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn displays() -> Vec<DisplayConfig> {
        (0..3)
            .map(|i| DisplayConfig { id: format!("d{i}"), width: 1920, height: 540, crop_x: 1920 * i, ..Default::default() })
            .collect()
    }

    #[test]
    fn args_map_one_output_per_display() {
        let canvas = Canvas { width: 5760, height: 540, fps: 30 };
        for vaapi in [None, Some("/dev/dri/renderD128")] {
            let a = build_args(canvas, &displays(), vaapi);
            let fc = &a[a.iter().position(|x| x == "-filter_complex").unwrap() + 1];
            assert!(fc.contains("split=3[s0][s1][s2]"));
            assert!(fc.contains("[s2]crop=1920:540:3840:0"));
            assert_eq!(fc.contains("hwupload"), vaapi.is_some());
            for i in 0..3 {
                assert!(a.contains(&format!("pipe:{}", 3 + i)));
            }
            assert_eq!(a.contains(&"h264_vaapi".to_string()), vaapi.is_some());
        }
    }

    /// Measures how long a raw canvas frame takes to come back out of the real
    /// ffmpeg command as a complete Access Unit. Opt-in, machine dependent:
    ///
    ///     LATENCY_TEST=1 cargo test --release encoder::tests::latency -- --nocapture
    ///
    /// LIBVA_DRIVER_NAME=i965 exercises the legacy driver used on Ivy Bridge
    /// (Core i5-3470), and LATENCY_IVY=1 masks CPU features Ivy Bridge lacks
    /// (AVX2, FMA3, BMI) in ffmpeg and x264.
    #[test]
    fn latency() {
        if std::env::var_os("LATENCY_TEST").is_none() {
            eprintln!("set LATENCY_TEST=1 to run");
            return;
        }
        measure(None);
        if std::path::Path::new("/dev/dri/renderD128").exists() {
            measure(Some("/dev/dri/renderD128"));
        }
    }

    /// CPU seconds (user + system) of all reaped child processes so far.
    fn children_cpu() -> f64 {
        // SAFETY: getrusage fills the struct.
        let mut ru: libc::rusage = unsafe { std::mem::zeroed() };
        unsafe { libc::getrusage(libc::RUSAGE_CHILDREN, &mut ru) };
        let secs = |t: libc::timeval| t.tv_sec as f64 + t.tv_usec as f64 / 1e6;
        secs(ru.ru_utime) + secs(ru.ru_stime)
    }

    fn measure(vaapi: Option<&str>) {
        let canvas = Canvas { width: 5760, height: 540, fps: 30 };
        let ds = displays();
        let mut args = build_args(canvas, &ds, vaapi);
        if std::env::var_os("LATENCY_IVY").is_some() {
            args.splice(0..0, ["-cpuflags".into(), "mmx+mmxext+sse+sse2+sse3+ssse3+sse4.1+sse4.2+avx+cmov".into()]);
            if let Some(i) = args.iter().position(|a| a == "-x264-params") {
                for j in (i + 1..args.len()).step_by(1) {
                    if args[j].starts_with("threads=1") {
                        args[j] = format!("asm=MMX2,SSE,SSE2,SSE2Fast,SSSE3,SSE4.1,SSE4.2,AVX,LZCNT:{}", args[j]);
                    }
                }
            }
        }
        let cpu0 = children_cpu();
        let mut pipes = OutputPipes::new(ds.len()).unwrap();
        let mut cmd = Command::new("ffmpeg");
        cmd.args(&args).stdin(Stdio::piped()).stderr(Stdio::inherit());
        pipes.attach(&mut cmd);
        let mut child = cmd.spawn().unwrap();
        pipes.close_writers();
        let mut readers = pipes.readers.drain(..);
        let first = readers.next().unwrap();
        let others: Vec<_> = readers
            .map(|r| std::thread::spawn(move || { let mut fr = FlvReader::new(r); while let Ok(Some(_)) = fr.next_au() {} }))
            .collect();
        drop(others);

        const FRAMES: usize = 150;
        let sent = Arc::new(Mutex::new(vec![Instant::now(); FRAMES]));
        let mut stdin = child.stdin.take().unwrap();
        let s = sent.clone();
        let feeder = std::thread::spawn(move || {
            let mut frame = vec![0u8; canvas.frame_size()];
            let start = Instant::now();
            for i in 0..FRAMES {
                let due = start + Duration::from_secs(1) * i as u32 / 30;
                std::thread::sleep(due.saturating_duration_since(Instant::now()));
                for (j, px) in frame.chunks_exact_mut(4).enumerate() {
                    px[0] = (i * 3 + (j >> 2)) as u8;
                    px[1] = (i + (j >> 8)) as u8;
                }
                s.lock().unwrap()[i] = Instant::now();
                if stdin.write_all(&frame).is_err() {
                    return;
                }
            }
        });
        let mut got = Vec::new();
        let mut fr = FlvReader::new(first);
        while let Ok(Some(_)) = fr.next_au() {
            got.push(Instant::now());
        }
        feeder.join().unwrap();
        let _ = child.wait();
        assert!(got.len() >= FRAMES - 5, "got {} access units, want about {FRAMES}", got.len());
        let sent = sent.lock().unwrap();
        let mut lat: Vec<f64> = (15..got.len().min(FRAMES)).map(|i| (got[i] - sent[i]).as_secs_f64() * 1000.0).collect();
        lat.sort_by(|a, b| a.partial_cmp(b).unwrap());
        let cpu = children_cpu() - cpu0;
        eprintln!(
            "{} (ffmpeg): frame written -> access unit ready: median {:.1} ms, p90 {:.1} ms; ffmpeg CPU {:.0}% of one core",
            vaapi.map_or("libx264", |_| "h264_vaapi"),
            lat[lat.len() / 2],
            lat[lat.len() * 9 / 10],
            cpu / (FRAMES as f64 / 30.0) * 100.0
        );
    }
}

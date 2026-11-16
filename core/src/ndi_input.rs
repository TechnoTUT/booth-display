//! NDI input through moip-engine's `ndi` crate (libndi.so.6 loaded at run time).
//!
//! * One finder lives for the whole process, so it already knows the senders
//!   when a source is looked up. Only right after it is created does
//!   discovery wait for the list to settle.
//! * Receivers ask for video only (UYVY, full bandwidth, progressive); audio
//!   and metadata are not requested, so the SDK discards them. Sources with
//!   alpha arrive as BGRA.
//! * Capture waits inside the SDK for the next frame and returns the moment
//!   it arrives; frames that queued up meanwhile are released at once and only
//!   the newest is used.
//! * Receivers are reused: switching sources reconnects a pooled receiver.
//!   A receiver whose session died is never torn down on the receive thread
//!   (that blocks inside the SDK for 10 s or more); it is destroyed in the
//!   background, followed by `malloc_trim`.

use crate::config::Canvas;
use crate::scale::{default_workers, Scaler};
use crossbeam_channel::{bounded, Receiver, RecvTimeoutError, Sender, TryRecvError};
use serde::Serialize;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

const RECV_NAME: &str = "booth-display";

/// How long discovery waits for senders to settle after the finder is created.
const DISCOVER_TIMEOUT: Duration = Duration::from_secs(4);
const DISCOVER_SETTLE: Duration = Duration::from_secs(1);
/// How long the source may be missing before it is reported as an error; it
/// is still looked for afterwards.
const FIND_TIMEOUT: Duration = Duration::from_secs(5);
/// Bounds one wait for a frame, and so how quickly stop takes effect.
const CAPTURE_TIMEOUT_MS: u32 = 20;
/// How often a missing source is looked up again.
const LOOKUP_INTERVAL: Duration = Duration::from_millis(100);
/// A connected source that delivers no frame for this long gets a fresh
/// receiver; the interval doubles up to MAX_STALL_INTERVAL while it stays silent.
const STALL_TIMEOUT: Duration = Duration::from_secs(2);
const MAX_STALL_INTERVAL: Duration = Duration::from_secs(30);
/// A receiver that delivered a frame this recently is healthy and can be
/// disconnected and reused when input stops.
const LIVE_WINDOW: Duration = Duration::from_millis(500);

fn init() -> anyhow::Result<()> {
    ndi::init().map_err(|e| {
        anyhow::anyhow!("NDI runtime (libndi) not available; run \"make setup-ndi\" or set NDI_RUNTIME_DIR_V6: {e}")
    })
}

// ---------------------------------------------------------------------------
// Finder
// ---------------------------------------------------------------------------

struct FinderState {
    finder: ndi::Finder,
    created: Instant,
}

static FINDER: Mutex<Option<FinderState>> = Mutex::new(None);

fn current_sources() -> anyhow::Result<(Vec<ndi::Source>, Duration)> {
    init()?;
    let mut f = FINDER.lock().unwrap();
    if f.is_none() {
        *f = Some(FinderState { finder: ndi::Finder::new(true, None)?, created: Instant::now() });
    }
    let st = f.as_ref().unwrap();
    let mut list = st.finder.sources();
    list.sort_by(|a, b| a.name.cmp(&b.name));
    Ok((list, st.created.elapsed()))
}

#[derive(Debug, Clone, Serialize)]
pub struct SourceInfo {
    pub name: String,
    pub stream_name: Option<String>,
    pub host_name: Option<String>,
}

/// The NDI senders on the network. Right after the finder is created it waits
/// until the list stops changing (senders are announced one by one);
/// afterwards the finder keeps the list current and it returns at once.
pub fn discover() -> anyhow::Result<Vec<SourceInfo>> {
    // Polled with short locks: the receive thread looks sources up meanwhile.
    let (mut last, age) = current_sources()?;
    let end = Instant::now() + DISCOVER_TIMEOUT.saturating_sub(age);
    let mut last_change = Instant::now();
    while Instant::now() < end {
        std::thread::sleep(Duration::from_millis(100));
        let (cur, _) = current_sources()?;
        if cur != last {
            last = cur;
            last_change = Instant::now();
        } else if !cur.is_empty() && last_change.elapsed() >= DISCOVER_SETTLE {
            break;
        }
    }
    Ok(last
        .into_iter()
        .map(|s| {
            let has_stream = s.name.contains(" (") && s.name.ends_with(')');
            SourceInfo {
                host_name: Some(s.host_name().to_string()),
                stream_name: has_stream.then(|| s.stream_name().to_string()),
                name: s.name,
            }
        })
        .collect())
}

/// The source matching `name` among those currently known, without waiting
/// (see [`ndi::find_source`]).
fn lookup(name: &str) -> anyhow::Result<Option<ndi::Source>> {
    let (list, _) = current_sources()?;
    Ok(ndi::find_source(&list, name).cloned())
}

// ---------------------------------------------------------------------------
// Receiver pool
// ---------------------------------------------------------------------------

static IDLE: Mutex<Option<ndi::Receiver>> = Mutex::new(None);
static DISCARDING: AtomicUsize = AtomicUsize::new(0);

fn new_receiver() -> anyhow::Result<ndi::Receiver> {
    let opts = ndi::RecvOptions {
        color_format: ndi::ColorFormat::UyvyBgra,
        bandwidth: ndi::Bandwidth::Highest,
        allow_video_fields: false,
        name: Some(RECV_NAME.into()),
    };
    Ok(ndi::Receiver::new(None, &opts)?)
}

fn acquire_receiver() -> anyhow::Result<ndi::Receiver> {
    match IDLE.lock().unwrap().take() {
        Some(r) => Ok(r),
        None => new_receiver(),
    }
}

fn drain(r: &ndi::Receiver) {
    while let ndi::Capture::Video(_) = r.capture(0, true, false) {}
}

/// Disconnects a receiver that was delivering frames (quick on a live
/// session) in the background and keeps it for reuse.
fn release(r: ndi::Receiver) {
    std::thread::spawn(move || {
        r.connect(None);
        drain(&r);
        let mut idle = IDLE.lock().unwrap();
        if idle.is_none() {
            *idle = Some(r);
        } else {
            drop(idle);
            destroy_and_trim(r);
        }
    });
}

/// Destroys a receiver in the background: tearing down a session that died
/// blocks inside the SDK for 10 s or more.
fn discard(r: ndi::Receiver) {
    let n = DISCARDING.fetch_add(1, Ordering::Relaxed) + 1;
    if n > 4 {
        eprintln!("[ndi] {n} stalled receivers are still being torn down");
    }
    std::thread::spawn(move || {
        destroy_and_trim(r);
        DISCARDING.fetch_sub(1, Ordering::Relaxed);
    });
}

fn destroy_and_trim(r: ndi::Receiver) {
    let start = Instant::now();
    drop(r);
    // Hand the frame buffers libndi freed back to the OS.
    // SAFETY: glibc call without preconditions.
    unsafe { libc::malloc_trim(0) };
    if start.elapsed() > Duration::from_secs(1) {
        eprintln!("[ndi] released a receiver in {:.1}s", start.elapsed().as_secs_f64());
    }
}

fn fourcc_str(c: u32) -> String {
    c.to_le_bytes().iter().map(|&b| b as char).collect()
}

// ---------------------------------------------------------------------------
// NdiInput
// ---------------------------------------------------------------------------

struct Shared {
    latest: Mutex<(Vec<u8>, bool)>,
    err: Mutex<Option<String>>,
}

/// Receives one NDI source, scales each frame onto the canvas (center crop,
/// BGRX) and keeps the latest one for the mixer, signalling each new frame.
pub struct NdiInput {
    pub source_name: String,
    shared: Arc<Shared>,
    notify: Receiver<()>,
    stop: Option<Sender<()>>,
    thread: Option<JoinHandle<()>>,
}

impl NdiInput {
    /// Loads libndi and starts receiving in the background. Finding the source
    /// and connecting happen on the receive thread; a failure there is
    /// reported through [`NdiInput::err`].
    pub fn start(source_name: &str, canvas: Canvas) -> anyhow::Result<Self> {
        init()?;
        let shared = Arc::new(Shared { latest: Mutex::new((vec![0; canvas.frame_size()], false)), err: Mutex::new(None) });
        let (notify_tx, notify) = bounded(1);
        let (stop_tx, stop_rx) = bounded::<()>(0);
        let worker = Worker {
            name: source_name.to_string(),
            canvas,
            shared: shared.clone(),
            notify: notify_tx,
            stop: stop_rx,
        };
        let thread = std::thread::Builder::new().name("ndi-recv".into()).spawn(move || worker.run())?;
        Ok(Self { source_name: source_name.to_string(), shared, notify, stop: Some(stop_tx), thread: Some(thread) })
    }

    /// Signals every new frame (coalesced).
    pub fn notifier(&self) -> Receiver<()> {
        self.notify.clone()
    }

    /// Copies the latest frame into dst; false (dst untouched) until one arrived.
    pub fn copy_latest(&self, dst: &mut [u8]) -> bool {
        let l = self.shared.latest.lock().unwrap();
        if l.1 {
            dst.copy_from_slice(&l.0);
        }
        l.1
    }

    /// Why input is not working, or None while it is healthy.
    pub fn err(&self) -> Option<String> {
        self.shared.err.lock().unwrap().clone()
    }
}

impl Drop for NdiInput {
    /// Ends receiving; the thread exits within a capture timeout and the
    /// receiver is released in the background.
    fn drop(&mut self) {
        self.stop.take();
        if let Some(t) = self.thread.take() {
            let _ = t.join();
        }
    }
}

struct Worker {
    name: String,
    canvas: Canvas,
    shared: Arc<Shared>,
    notify: Sender<()>,
    stop: Receiver<()>,
}

impl Worker {
    fn fail(&self, msg: String) {
        eprintln!("[ndi] {msg}");
        *self.shared.err.lock().unwrap() = Some(format!("NDI input: {msg}"));
    }

    fn stopped(&self) -> bool {
        matches!(self.stop.try_recv(), Err(TryRecvError::Disconnected))
    }

    /// Finds the source, connects and forwards frames until stopped. It
    /// recovers on its own:
    ///
    /// * Source not found: it keeps looking (the error is reported after
    ///   FIND_TIMEOUT and cleared once frames arrive).
    /// * No new frame for the stall interval although NDI reports a
    ///   connection: after a network outage a dead session can look connected
    ///   for minutes, and tearing it down blocks for ~15 s. So the receiver is
    ///   discarded in the background and a fresh one connects. The last frame
    ///   stays on the canvas meanwhile.
    /// * No connection: NDI retries by itself; if the sender reappears (or
    ///   under another address), a fresh receiver connects to it at once.
    fn run(self) {
        eprintln!("[ndi] looking for source {:?} (NDI {})", self.name, ndi::version().unwrap_or_default());
        let mut recv = match acquire_receiver() {
            Ok(r) => r,
            Err(e) => return self.fail(e.to_string()),
        };
        let started = Instant::now();
        let mut src: Option<ndi::Source> = None;
        let mut last_frame = Instant::now(); // last new frame, or when the current connection started
        let mut next_check = Instant::now();
        let mut stall_ivl = STALL_TIMEOUT;
        let mut reported = false; // "not found" error reported
        let mut gone = false; // the connected source dropped off the source list
        let mut connected = false; // a frame arrived on the current connection

        // Accept a frame once 60% of the canvas interval has passed since the
        // last one: a 30 fps source passes every frame despite jitter, a 60 fps
        // source passes every other one.
        let min_gap = Duration::from_secs(1) * 6 / (self.canvas.fps * 10);
        let mut scaler: Option<(Scaler, u32)> = None;
        let mut back = vec![0u8; self.canvas.frame_size()];
        let mut last_out: Option<Instant> = None;
        let (mut frames, mut skipped, mut stats_time) = (0u32, 0usize, Instant::now());
        let mut last_err = false;

        let replace = |recv: &mut ndi::Receiver, s: &ndi::Source| -> bool {
            match new_receiver() {
                Ok(fresh) => {
                    discard(std::mem::replace(recv, fresh));
                    recv.connect(Some(s));
                    true
                }
                Err(e) => {
                    eprintln!("[ndi] {e}");
                    false
                }
            }
        };

        loop {
            if self.stopped() {
                break;
            }
            let Some(cur) = src.clone() else {
                let wait = next_check.saturating_duration_since(Instant::now());
                if !wait.is_zero() && !matches!(self.stop.recv_timeout(wait), Err(RecvTimeoutError::Timeout)) {
                    break;
                }
                next_check = Instant::now() + LOOKUP_INTERVAL;
                match lookup(&self.name) {
                    Err(e) => {
                        self.fail(e.to_string());
                        break;
                    }
                    Ok(None) => {
                        if !reported && started.elapsed() >= FIND_TIMEOUT {
                            self.fail(format!("NDI source {:?} not found on the network; still looking", self.name));
                            reported = true;
                        }
                    }
                    Ok(Some(s)) => {
                        recv.connect(Some(&s));
                        eprintln!("[ndi] connecting to {} ({})", s.name, s.url.as_deref().unwrap_or("-"));
                        src = Some(s);
                        last_frame = Instant::now();
                    }
                }
                continue;
            };

            let captured = recv.capture_newest_video(CAPTURE_TIMEOUT_MS);
            let now = Instant::now();
            let is_err = matches!(captured, ndi::NewestVideo::Error);
            if is_err && !last_err {
                eprintln!("[ndi] receiver reported a connection error");
            }
            last_err = is_err;
            let ndi::NewestVideo::Frame(f, dropped) = captured else {
                let silent = now - last_frame;
                if silent < LIVE_WINDOW || now < next_check {
                    continue;
                }
                next_check = now + 2 * LOOKUP_INTERVAL;
                if recv.is_connected() {
                    if silent < stall_ivl {
                        continue;
                    }
                    // Connected but silent: replace the receiver (see above).
                    next_check = now + Duration::from_secs(1);
                    let s = lookup(&self.name).ok().flatten().unwrap_or(cur);
                    if replace(&mut recv, &s) {
                        eprintln!(
                            "[ndi] no new frames for {:.1}s on a connected receiver; reconnecting to {}",
                            silent.as_secs_f64(),
                            s.name
                        );
                        last_frame = Instant::now();
                        stall_ivl = (stall_ivl * 2).min(MAX_STALL_INTERVAL);
                        connected = false;
                        src = Some(s);
                    }
                    continue;
                }
                // Not connected. NDI retries by itself, but backs off to ~10 s
                // between attempts; connect afresh as soon as the sender is back.
                match lookup(&self.name).ok().flatten() {
                    None => gone = true,
                    Some(s) if gone || s.url != cur.url => {
                        gone = false;
                        if replace(&mut recv, &s) {
                            eprintln!("[ndi] source is back; connecting to {}", s.name);
                            last_frame = Instant::now();
                            connected = false;
                            src = Some(s);
                        }
                    }
                    Some(_) => {}
                }
                continue;
            };

            last_frame = now;
            stall_ivl = STALL_TIMEOUT;
            gone = false;
            skipped += dropped;
            if !connected {
                connected = true;
                eprintln!("[ndi] receiving {}: {}x{} {}", cur.name, f.width(), f.height(), fourcc_str(f.fourcc()));
                if reported {
                    *self.shared.err.lock().unwrap() = None;
                    reported = false;
                }
            }
            if last_out.is_some_and(|t| now - t < min_gap) {
                skipped += 1;
                continue;
            }
            last_out = Some(now);

            let (w, h, fourcc, stride) = (f.width(), f.height(), f.fourcc(), f.stride());
            let uyvy = fourcc == ndi::FOURCC_UYVY;
            if !(uyvy || fourcc == ndi::FOURCC_BGRA || fourcc == ndi::FOURCC_BGRX) || w == 0 || h == 0 {
                continue;
            }
            if stride < if uyvy { w.div_ceil(2) * 4 } else { w * 4 } {
                continue;
            }
            let rebuild = !matches!(&scaler, Some((s, fc)) if s.src_w == w && s.src_h == h && *fc == fourcc);
            if rebuild {
                let (cw, ch) = (self.canvas.width, self.canvas.height);
                let s = if uyvy {
                    Scaler::uyvy(w, h, cw, ch, true, default_workers())
                } else {
                    Scaler::bgrx(w, h, cw, ch, true, default_workers())
                };
                eprintln!(
                    "[ndi] source {w}x{h} -> canvas {cw}x{ch} (crop {}x{} at {},{})",
                    s.crop_w, s.crop_h, s.crop_x, s.crop_y
                );
                scaler = Some((s, fourcc));
            }
            scaler.as_mut().unwrap().0.scale(&mut back, f.data(), stride);
            drop(f);

            {
                let mut l = self.shared.latest.lock().unwrap();
                std::mem::swap(&mut l.0, &mut back);
                l.1 = true;
            }
            let _ = self.notify.try_send(());

            frames += 1;
            let since = now - stats_time;
            if since >= Duration::from_secs(60) {
                eprintln!(
                    "[ndi] {:.1} fps forwarded, {skipped} frames skipped in the last {:.0}s",
                    frames as f64 / since.as_secs_f64(),
                    since.as_secs_f64()
                );
                (frames, skipped, stats_time) = (0, 0, now);
            }
        }

        if src.is_some() && last_frame.elapsed() > LIVE_WINDOW {
            discard(recv);
        } else {
            release(recv);
        }
    }
}

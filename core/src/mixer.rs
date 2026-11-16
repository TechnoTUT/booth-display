//! Virtual canvas mixer: renders canvas frames from the active source (test
//! pattern, logo, NDI, video file, ffmpeg testsrc), handles transitions (cut,
//! crossfade, dip to black) without restarting the encoder, and fans every
//! frame out to its subscribers by reference (no per-subscriber copy).

use crate::config::{Canvas, DisplayConfig};
use crate::ffsource::FfSource;
use crate::frame::{Frame, FramePool};
use crate::logo::Logo;
use crate::ndi_input::NdiInput;
use crate::scale::{blend_rows, scale_bytes};
use crate::testpattern::TestPattern;
use crossbeam_channel::{bounded, never, select, tick, Receiver, Sender, TrySendError};
use serde::{Deserialize, Serialize};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Transition {
    #[default]
    Cut,
    Fade,
    Black,
}

#[derive(Debug, Clone, Serialize)]
pub struct SceneStatus {
    pub active_source: String,
    pub active_target: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub next_source: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub next_target: String,
    pub transition: Transition,
    pub duration_ms: u64,
    pub in_transition: bool,
    pub progress: f64,
}

struct State {
    canvas: Canvas,
    test_pattern: TestPattern,
    logo: Logo,
    ndi: Option<NdiInput>,
    ff: Option<FfSource>,

    last: Option<Frame>,
    prev: Vec<u8>,     // the frame on screen when the transition started
    next_buf: Vec<u8>, // incoming source frame during a transition

    active_source: String,
    active_target: String,
    next_source: String,
    next_target: String,
    transition: Transition,
    duration: Duration,
    trans_start: Instant,
    in_transition: bool,
}

impl State {
    fn stop_external(&mut self) {
        self.ndi = None;
        self.ff = None;
    }

    /// Writes the next frame of `source` into dst.
    fn fetch(&mut self, source: &str, dst: &mut [u8]) {
        let ok = match source {
            "ndi" => self.ndi.as_ref().is_some_and(|n| n.copy_latest(dst)),
            "video" | "rainbow" => self.ff.as_ref().is_some_and(|f| f.copy_latest(dst)),
            "logo" => {
                self.logo.render(dst);
                true
            }
            _ => {
                self.test_pattern.render(dst);
                true
            }
        };
        if !ok {
            dst.fill(0);
        }
    }

    fn finish_transition(&mut self) {
        self.active_source = std::mem::take(&mut self.next_source);
        self.active_target = std::mem::take(&mut self.next_target);
        self.in_transition = false;
    }

    fn render(&mut self, out: &mut [u8]) {
        if !self.in_transition {
            let src = self.active_source.clone();
            return self.fetch(&src, out);
        }
        let progress = self.trans_start.elapsed().as_secs_f64() / self.duration.as_secs_f64();
        if progress >= 1.0 || self.transition == Transition::Cut {
            self.finish_transition();
            let src = self.active_source.clone();
            return self.fetch(&src, out);
        }
        let mut next = std::mem::take(&mut self.next_buf);
        let src = self.next_source.clone();
        self.fetch(&src, &mut next);
        // Weights are fixed-point with 8 fractional bits (0..256).
        match self.transition {
            Transition::Fade => blend_rows(out, &self.prev, &next, (progress * 256.0) as u16),
            Transition::Black if progress < 0.5 => scale_bytes(out, &self.prev, ((0.5 - progress) * 512.0) as u16),
            Transition::Black => scale_bytes(out, &next, ((progress - 0.5) * 512.0) as u16),
            Transition::Cut => unreachable!(),
        }
        self.next_buf = next;
    }
}

struct Inner {
    pool: FramePool,
    fps: u32,
    state: Mutex<State>,
    subs: Mutex<Vec<Sender<Frame>>>,
}

pub struct Mixer {
    inner: Arc<Inner>,
    stop: Option<Sender<()>>,
    thread: Option<JoinHandle<()>>,
}

impl Mixer {
    pub fn start(canvas: Canvas, displays: &[DisplayConfig]) -> anyhow::Result<Self> {
        let size = canvas.frame_size();
        let inner = Arc::new(Inner {
            pool: FramePool::new(size),
            fps: canvas.fps,
            state: Mutex::new(State {
                canvas,
                test_pattern: TestPattern::new(canvas, displays),
                // Rasterized here, not on first use: it takes ~25 ms, which
                // would stall the render loop.
                logo: Logo::new(canvas),
                ndi: None,
                ff: None,
                last: None,
                prev: vec![0; size],
                next_buf: vec![0; size],
                active_source: "testpattern".into(),
                active_target: String::new(),
                next_source: String::new(),
                next_target: String::new(),
                transition: Transition::Cut,
                duration: Duration::from_millis(500),
                trans_start: Instant::now(),
                in_transition: false,
            }),
            subs: Mutex::default(),
        });
        let (stop_tx, stop_rx) = bounded::<()>(0);
        let i = inner.clone();
        let thread = std::thread::Builder::new().name("mixer".into()).spawn(move || run(&i, stop_rx))?;
        Ok(Self { inner, stop: Some(stop_tx), thread: Some(thread) })
    }

    /// A stream of canvas frames. Frames are dropped for a subscriber that
    /// falls two frames behind; dropping the receiver unsubscribes.
    pub fn subscribe(&self) -> Receiver<Frame> {
        let (tx, rx) = bounded(2);
        self.inner.subs.lock().unwrap().push(tx);
        rx
    }

    /// Redraws the test pattern's display boundaries.
    pub fn set_displays(&self, displays: &[DisplayConfig]) {
        let mut st = self.inner.state.lock().unwrap();
        st.test_pattern = TestPattern::new(st.canvas, displays);
    }

    /// Starts a transition to a new source.
    pub fn switch_source(&self, source_type: &str, target: &str, trans: Transition, duration_ms: u64) -> anyhow::Result<()> {
        let mut st = self.inner.state.lock().unwrap();
        let duration_ms = if duration_ms == 0 { 500 } else { duration_ms };

        // The frame on screen now is where a crossfade starts from.
        let last = st.last.clone();
        match last {
            Some(f) => st.prev.copy_from_slice(&f),
            None => st.prev.fill(0),
        }

        let canvas = st.canvas;
        let (source, target) = match source_type {
            "ndi" => {
                anyhow::ensure!(!target.is_empty(), "no NDI source name specified");
                let same = st.ndi.as_ref().is_some_and(|n| n.source_name == target);
                if !same {
                    st.stop_external();
                    st.ndi = Some(NdiInput::start(target, canvas).map_err(|e| anyhow::anyhow!("failed to start NDI: {e}"))?);
                }
                ("ndi", target)
            }
            "video" => {
                anyhow::ensure!(!target.is_empty(), "no video file path specified");
                st.stop_external();
                st.ff = Some(FfSource::video(target, canvas)?);
                ("video", target)
            }
            "rainbow" => {
                st.stop_external();
                st.ff = Some(FfSource::testsrc(canvas)?);
                ("rainbow", "testsrc")
            }
            "testpattern" if target == "rainbow" => {
                st.stop_external();
                st.ff = Some(FfSource::testsrc(canvas)?);
                ("rainbow", "testsrc")
            }
            "logo" => {
                st.stop_external();
                ("logo", "technotut")
            }
            _ => {
                st.stop_external();
                ("testpattern", "bars") // SMPTE color bars
            }
        };

        if trans == Transition::Cut || duration_ms < 50 {
            st.active_source = source.into();
            st.active_target = target.into();
            st.next_source.clear();
            st.next_target.clear();
            st.in_transition = false;
        } else {
            st.next_source = source.into();
            st.next_target = target.into();
            st.transition = trans;
            st.duration = Duration::from_millis(duration_ms);
            st.trans_start = Instant::now();
            st.in_transition = true;
        }
        Ok(())
    }

    pub fn status(&self) -> SceneStatus {
        let st = self.inner.state.lock().unwrap();
        let progress = if st.in_transition {
            (st.trans_start.elapsed().as_secs_f64() / st.duration.as_secs_f64()).min(1.0)
        } else {
            0.0
        };
        SceneStatus {
            active_source: st.active_source.clone(),
            active_target: st.active_target.clone(),
            next_source: st.next_source.clone(),
            next_target: st.next_target.clone(),
            transition: st.transition,
            duration_ms: st.duration.as_millis() as u64,
            in_transition: st.in_transition,
            progress,
        }
    }

    /// Why the active NDI input is not working, if it is not.
    pub fn source_err(&self) -> Option<String> {
        self.inner.state.lock().unwrap().ndi.as_ref().and_then(|n| n.err())
    }
}

impl Drop for Mixer {
    fn drop(&mut self) {
        self.stop.take();
        if let Some(t) = self.thread.take() {
            let _ = t.join();
        }
        let mut st = self.inner.state.lock().unwrap();
        st.stop_external();
        st.last = None;
        // Closing the subscriber channels ends the consumers' loops.
        self.inner.subs.lock().unwrap().clear();
    }
}

/// Renders a frame whenever the NDI source delivers one, so a frame goes to
/// the encoder the moment it arrives. The ticker paces generator sources and
/// stands in when NDI frames stop.
///
/// The ticker must not compete with NDI arrivals: a ticker of exactly 30 Hz
/// beats against a 29.97 fps source, and whenever it fired just before a frame
/// arrived it would render the old frame and push the new one back a full
/// interval. So while NDI frames keep coming, ticks are skipped.
fn run(inner: &Inner, stop: Receiver<()>) {
    let interval = Duration::from_secs(1) / inner.fps;
    // An NDI frame is rendered once 60% of an interval has passed since the
    // last render: absorbs arrival jitter of a 30 fps source while thinning a
    // faster source to about the canvas rate.
    let min_gap = interval * 6 / 10;
    // Ticks are skipped while the last NDI frame is more recent than this.
    let ndi_live = interval * 3 / 2;
    let ticker = tick(interval);
    let none = never();
    let (mut last_render, mut last_ndi) = (Instant::now() - interval, Instant::now() - interval);

    let render = |last_render: &mut Instant| {
        let mut buf = inner.pool.get();
        let frame: Frame = {
            let mut st = inner.state.lock().unwrap();
            st.render(buf.as_mut_slice());
            let f = Arc::new(buf);
            st.last = Some(f.clone());
            f
        };
        inner.subs.lock().unwrap().retain(|s| !matches!(s.try_send(frame.clone()), Err(TrySendError::Disconnected(_))));
        *last_render = Instant::now();
    };

    loop {
        let ndi_rx = {
            let st = inner.state.lock().unwrap();
            match &st.ndi {
                Some(n) if st.active_source == "ndi" || st.next_source == "ndi" => Some(n.notifier()),
                _ => None,
            }
        };
        let ndi_active = ndi_rx.is_some();
        select! {
            recv(stop) -> _ => return,
            recv(ndi_rx.as_ref().unwrap_or(&none)) -> _ => {
                last_ndi = Instant::now();
                if last_render.elapsed() >= min_gap {
                    render(&mut last_render);
                }
            }
            recv(ticker) -> _ => {
                if !(ndi_active && last_ndi.elapsed() < ndi_live) {
                    render(&mut last_render);
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn canvas() -> Canvas {
        Canvas { width: 1920, height: 540, fps: 30 }
    }

    #[test]
    fn transitions_complete() {
        let m = Mixer::start(canvas(), &[]).unwrap();
        let rx = m.subscribe();
        assert_eq!(m.status().active_source, "testpattern");
        m.switch_source("testpattern", "", Transition::Cut, 0).unwrap();
        m.switch_source("logo", "", Transition::Fade, 100).unwrap();
        assert!(m.status().in_transition);
        assert_eq!(m.status().next_source, "logo");
        std::thread::sleep(Duration::from_millis(200));
        let st = m.status();
        assert!(!st.in_transition, "transition must finish after its duration");
        assert_eq!(st.active_source, "logo");
        m.switch_source("testpattern", "", Transition::Black, 100).unwrap();
        std::thread::sleep(Duration::from_millis(200));
        assert_eq!(m.status().active_source, "testpattern");
        let f = rx.recv_timeout(Duration::from_secs(1)).unwrap();
        assert_eq!(f.len(), canvas().frame_size());
    }

    #[test]
    fn renders_at_canvas_rate() {
        let m = Mixer::start(canvas(), &[]).unwrap();
        let rx = m.subscribe();
        let start = Instant::now();
        let mut n = 0;
        while start.elapsed() < Duration::from_millis(500) {
            if rx.recv_timeout(Duration::from_millis(100)).is_ok() {
                n += 1;
            }
        }
        assert!((12..=18).contains(&n), "got {n} frames in 0.5 s at 30 fps");
    }

    #[test]
    fn crossfade_is_between_frames() {
        let mut prev = vec![200u8; 8];
        let next = vec![100u8; 8];
        let mut out = vec![0u8; 8];
        blend_rows(&mut out, &prev, &next, 128);
        assert!(out.iter().all(|&v| v == 150));
        prev.fill(255);
        scale_bytes(&mut out, &prev, 256);
        assert!(out.iter().all(|&v| v == 255));
    }

    #[test]
    fn ndi_needs_a_name() {
        let m = Mixer::start(canvas(), &[]).unwrap();
        assert!(m.switch_source("ndi", "", Transition::Cut, 0).is_err());
    }
}

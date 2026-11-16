//! Canvas sources produced by an ffmpeg child process as raw BGRX frames: a
//! looping video file, or ffmpeg's animated `testsrc` ("rainbow").

use crate::config::Canvas;
use anyhow::Context;
use std::io::Read;
use std::os::fd::AsRawFd;
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;

struct Latest {
    data: Vec<u8>,
    has_frame: bool,
}

pub struct FfSource {
    child: Child,
    reader: Option<JoinHandle<()>>,
    latest: Arc<Mutex<Latest>>,
}

impl FfSource {
    pub fn video(path: &str, canvas: Canvas) -> anyhow::Result<Self> {
        Self::spawn(&["-re", "-stream_loop", "-1", "-i", path], canvas)
    }

    pub fn testsrc(canvas: Canvas) -> anyhow::Result<Self> {
        let src = format!("testsrc=size={}x{}:rate={}", canvas.width, canvas.height, canvas.fps);
        Self::spawn(&["-re", "-f", "lavfi", "-i", &src], canvas)
    }

    fn spawn(input: &[&str], canvas: Canvas) -> anyhow::Result<Self> {
        let mut child = Command::new("ffmpeg")
            .args(["-hide_banner", "-loglevel", "error"])
            .args(input)
            .args(["-f", "rawvideo", "-pix_fmt", "bgr0"])
            .args(["-s", &format!("{}x{}", canvas.width, canvas.height)])
            .args(["-r", &canvas.fps.to_string(), "pipe:1"])
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .spawn()
            .context("failed to start ffmpeg source")?;
        let mut stdout = child.stdout.take().expect("piped stdout");
        expand_pipe_buffer(&stdout);

        let size = canvas.frame_size();
        let latest = Arc::new(Mutex::new(Latest { data: vec![0; size], has_frame: false }));
        let shared = latest.clone();
        let reader = std::thread::Builder::new().name("ffmpeg-source".into()).spawn(move || {
            let mut back = vec![0u8; size];
            while stdout.read_exact(&mut back).is_ok() {
                let mut l = shared.lock().unwrap();
                std::mem::swap(&mut l.data, &mut back);
                l.has_frame = true;
            }
        })?;
        Ok(Self { child, reader: Some(reader), latest })
    }

    /// Copies the latest frame into dst; false (dst untouched) until one arrived.
    pub fn copy_latest(&self, dst: &mut [u8]) -> bool {
        let l = self.latest.lock().unwrap();
        if l.has_frame {
            dst.copy_from_slice(&l.data);
        }
        l.has_frame
    }
}

impl Drop for FfSource {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
        if let Some(t) = self.reader.take() {
            let _ = t.join();
        }
    }
}

/// Grows a pipe to 512 KB (the unprivileged maximum by default) to cut down
/// context switches while frames of several MB stream through it.
pub fn expand_pipe_buffer(fd: &impl AsRawFd) {
    // SAFETY: plain fcntl on an fd we own; failure is harmless.
    unsafe {
        libc::fcntl(fd.as_raw_fd(), libc::F_SETPIPE_SZ, 512 * 1024);
    }
}

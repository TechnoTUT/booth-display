//! Software H.264 encoding with libx264, loaded at run time.
//!
//! libx264 is GPL; booth-core does not link it or ship it. It is opened with
//! dlopen from the system (Debian/Ubuntu `libx264-164`, RPM Fusion `x264-libs`)
//! when the software encoder is needed, the same way libva and libndi are, so
//! building needs neither the library nor its headers.
//!
//! The stream matches what the ffmpeg libx264 command produced before:
//! ultrafast/zerolatency, Baseline, one slice per frame, AUD and in-band
//! SPS/PPS on every access unit's IDR, an IDR at the start of every GOP, CBR
//! with a small VBV buffer, and output for every input frame (no lookahead).

pub mod sys;

use crate::nv12::Nv12Planes;
use anyhow::{anyhow, bail, Result};
use std::ffi::CString;
use std::os::raw::c_int;
use std::sync::OnceLock;
use sys::*;

/// Builds tried, newest first. The fields booth-core touches have kept their
/// layout across all of them; the soname number is the build number.
const BUILDS: std::ops::RangeInclusive<u32> = 148..=200;

struct Runtime {
    api: Api,
    _lib: libloading::Library,
}

static RUNTIME: OnceLock<std::result::Result<Runtime, String>> = OnceLock::new();

fn load() -> std::result::Result<Runtime, String> {
    for build in BUILDS.rev() {
        let name = format!("libx264.so.{build}");
        // SAFETY: loading libx264 runs its (trusted) initialisers.
        let Ok(lib) = (unsafe { libloading::Library::new(&name) }) else { continue };
        // SAFETY: this is libx264 of this build, so the symbols have the declared signatures.
        let api = unsafe { Api::load(&lib, build) }.map_err(|e| format!("{name}: missing symbol: {e}"))?;
        return Ok(Runtime { api, _lib: lib });
    }
    // An unversioned development symlink, with any build number.
    // SAFETY: as above.
    if let Ok(lib) = unsafe { libloading::Library::new("libx264.so") } {
        for build in BUILDS.rev() {
            // SAFETY: as above; the build is confirmed by the versioned open symbol.
            if let Ok(api) = unsafe { Api::load(&lib, build) } {
                return Ok(Runtime { api, _lib: lib });
            }
        }
        return Err("libx264.so: no x264_encoder_open_<build> symbol".into());
    }
    Err(format!(
        "libx264.so.{{{}..{}}} not found (install libx264-164 / x264-libs)",
        BUILDS.start(),
        BUILDS.end()
    ))
}

fn api() -> Result<&'static Api> {
    match RUNTIME.get_or_init(load) {
        Ok(rt) => Ok(&rt.api),
        Err(e) => Err(anyhow!("libx264 not available: {e}")),
    }
}

/// Stream parameters for one display.
#[derive(Debug, Clone, Copy)]
pub struct Params {
    pub width: usize,
    pub height: usize,
    pub fps: u32,
    /// Target and maximum bit rate, kbit/s.
    pub bitrate_kbps: u32,
    /// VBV buffer, kbit.
    pub buffer_kbits: u32,
    /// Frames per GOP; every GOP starts with an IDR.
    pub gop: u32,
}

pub struct Encoder {
    api: &'static Api,
    h: *mut x264_t,
    pic_in: Box<x264_picture_t>,
    pic_out: Box<x264_picture_t>,
    y: Vec<u8>,
    uv: Vec<u8>,
    p: Params,
    pts: i64,
}

// SAFETY: the x264 handle is used by one thread at a time (Encoder is not Sync).
unsafe impl Send for Encoder {}

impl Encoder {
    pub fn new(p: Params) -> Result<Self> {
        let api = api()?;
        if p.width == 0 || p.height == 0 || p.width % 2 != 0 || p.height % 2 != 0 {
            bail!("size {}x{} must be positive and even", p.width, p.height);
        }
        let mut param = x264_param_t::zeroed();
        let c = |s: &str| CString::new(s).unwrap();
        // SAFETY: param has room for the whole x264_param_t (layout tests).
        if unsafe { (api.param_default_preset)(&mut *param, c("ultrafast").as_ptr(), c("zerolatency").as_ptr()) } < 0 {
            bail!("x264_param_default_preset failed");
        }
        param.head.i_width = p.width as c_int;
        param.head.i_height = p.height as c_int;
        param.head.i_csp = X264_CSP_NV12;
        let (fps, gop) = (p.fps.max(1).to_string(), p.gop.max(1).to_string());
        let (rate, vbv) = (p.bitrate_kbps.to_string(), p.buffer_kbits.to_string());
        let settings = [
            ("fps", fps.as_str()),
            // One thread per display: frame threading would hold every frame
            // back by (threads - 1) frames.
            ("threads", "1"),
            ("sliced-threads", "0"),
            ("slices", "1"),
            ("keyint", gop.as_str()),
            ("min-keyint", gop.as_str()),
            ("scenecut", "0"),
            ("bframes", "0"),
            ("rc-lookahead", "0"),
            ("sync-lookahead", "0"),
            ("bitrate", rate.as_str()),
            ("vbv-maxrate", rate.as_str()),
            ("vbv-bufsize", vbv.as_str()),
            ("aud", "1"),
            ("repeat-headers", "1"),
            ("annexb", "1"),
        ];
        for (k, v) in &settings {
            // SAFETY: valid param and NUL-terminated strings.
            let r = unsafe { (api.param_parse)(&mut *param, c(k).as_ptr(), c(v).as_ptr()) };
            if r != 0 {
                free_param(api, &mut param);
                bail!("x264_param_parse({k}={v}) failed ({r})");
            }
        }
        // Errors only; x264 otherwise prints a summary on close. Older builds
        // without this option keep their default level.
        // SAFETY: as above.
        unsafe { (api.param_parse)(&mut *param, c("log").as_ptr(), c(&X264_LOG_ERROR.to_string()).as_ptr()) };
        // SAFETY: as above.
        if unsafe { (api.param_apply_profile)(&mut *param, c("baseline").as_ptr()) } < 0 {
            free_param(api, &mut param);
            bail!("x264_param_apply_profile(baseline) failed");
        }
        // SAFETY: valid, fully initialised param; x264 copies it.
        let h = unsafe { (api.encoder_open)(&mut *param) };
        free_param(api, &mut param);
        if h.is_null() {
            bail!("x264_encoder_open_{} failed for {}x{}", api.build, p.width, p.height);
        }

        let (w, ht) = (p.width, p.height);
        let mut enc = Self {
            api,
            h,
            pic_in: x264_picture_t::zeroed(),
            pic_out: x264_picture_t::zeroed(),
            y: vec![0; w * ht],
            uv: vec![128; w * ht / 2],
            p,
            pts: 0,
        };
        // SAFETY: the picture structs have room for the whole x264_picture_t.
        unsafe {
            (api.picture_init)(&mut *enc.pic_in);
            (api.picture_init)(&mut *enc.pic_out);
        }
        let img = &mut enc.pic_in.img;
        img.i_csp = X264_CSP_NV12;
        img.i_plane = 2;
        img.i_stride = [w as c_int, w as c_int, 0, 0];
        img.plane = [enc.y.as_mut_ptr(), enc.uv.as_mut_ptr(), std::ptr::null_mut(), std::ptr::null_mut()];
        Ok(enc)
    }

    pub fn describe(&self) -> String {
        format!("libx264 build {} (in-process, ultrafast, {} kbit/s)", self.api.build, self.p.bitrate_kbps)
    }

    /// Fills the input picture through `fill` and encodes it, as an IDR when
    /// `force_idr`. Returns the Annex-B access unit and whether it is an IDR.
    /// The access unit may be empty if x264 produced nothing for this frame.
    pub fn encode(&mut self, mut fill: impl FnMut(&mut Nv12Planes), force_idr: bool) -> Result<(&[u8], bool)> {
        let w = self.p.width;
        let mut planes =
            Nv12Planes { width: w, height: self.p.height, y: &mut self.y, y_pitch: w, uv: &mut self.uv, uv_pitch: w };
        fill(&mut planes);

        self.pic_in.i_type = if force_idr { X264_TYPE_IDR } else { X264_TYPE_AUTO };
        self.pic_in.i_pts = self.pts;
        self.pts += 1;
        let mut nals: *mut x264_nal_t = std::ptr::null_mut();
        let mut n: c_int = 0;
        // SAFETY: valid encoder; pic_in points at our planes, which stay alive
        // and unchanged during the call.
        let size =
            unsafe { (self.api.encoder_encode)(self.h, &mut nals, &mut n, &mut *self.pic_in, &mut *self.pic_out) };
        if size < 0 {
            bail!("x264_encoder_encode failed ({size})");
        }
        if size == 0 || n == 0 || nals.is_null() {
            return Ok((&[], false));
        }
        // SAFETY: x264 guarantees the payloads of one call are contiguous,
        // starting at the first NAL, `size` bytes in all, valid until the next
        // encode call (which needs &mut self).
        let au = unsafe { std::slice::from_raw_parts((*nals).p_payload, size as usize) };
        Ok((au, self.pic_out.b_keyframe != 0))
    }
}

fn free_param(api: &Api, param: &mut x264_param_t) {
    if let Some(cleanup) = api.param_cleanup {
        // SAFETY: frees only what x264_param_parse allocated in this param.
        unsafe { cleanup(param) };
    }
}

impl Drop for Encoder {
    fn drop(&mut self) {
        // SAFETY: valid encoder, closed once.
        unsafe { (self.api.encoder_close)(self.h) };
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::nv12::bgrx_to_nv12;
    use std::io::Write;
    use std::process::{Command, Stdio};

    /// A moving pattern must encode to a stream ffmpeg decodes at high PSNR,
    /// with an AUD on every access unit and SPS/PPS on every IDR, one IDR per
    /// GOP. Skipped when libx264 (or, for the decode, ffmpeg) is missing.
    #[test]
    fn encodes_decodable_stream() {
        let p = Params { width: 320, height: 180, fps: 30, bitrate_kbps: 4000, buffer_kbits: 400, gop: 15 };
        let mut enc = match Encoder::new(p) {
            Ok(e) => e,
            Err(e) => {
                eprintln!("{e:#}; skipping");
                return;
            }
        };
        eprintln!("encoder: {}", enc.describe());
        const FRAMES: usize = 45;
        const AUD: [u8; 5] = [0, 0, 0, 1, 0x09];
        let (w, h) = (p.width, p.height);
        let mut inputs = Vec::new();
        // A keyframe request in the middle of a GOP.
        const FORCED_IDR: usize = 20;
        let mut stream = Vec::new();
        let mut keys = 0;
        for i in 0..FRAMES {
            let mut frame = vec![0u8; w * h * 4];
            for (j, px) in frame.chunks_exact_mut(4).enumerate() {
                let (x, y) = (j % w, j / w);
                px[0] = ((x + i * 4) & 0xff) as u8;
                px[1] = ((y * 2 + i) & 0xff) as u8;
                px[2] = ((x ^ y) & 0xff) as u8;
            }
            let mut y_plane = vec![0u8; w * h];
            let mut uv_plane = vec![0u8; w * h / 2];
            let mut ref_planes = Nv12Planes { width: w, height: h, y: &mut y_plane, y_pitch: w, uv: &mut uv_plane, uv_pitch: w };
            bgrx_to_nv12(&frame, w * 4, 0, 0, h, &mut ref_planes);
            inputs.push(y_plane);

            let (au, idr) = enc.encode(|planes| bgrx_to_nv12(&frame, w * 4, 0, 0, h, planes), i == FORCED_IDR).unwrap();
            assert!(!au.is_empty(), "frame {i}: no output (zero latency expected)");
            assert_eq!(&au[..5], &AUD, "frame {i} must start with an AUD");
            // GOP 15, restarted by the IDR forced at frame 20.
            assert_eq!(idr, [0, 15, 20, 35].contains(&i), "frame {i}");
            if idr {
                keys += 1;
                assert!(au.windows(5).any(|w| w == [0, 0, 0, 1, 0x67]), "IDR {i}: no SPS");
                assert!(au.windows(5).any(|w| w == [0, 0, 0, 1, 0x68]), "IDR {i}: no PPS");
            }
            stream.extend_from_slice(au);
        }
        assert_eq!(keys, 4);

        if Command::new("ffmpeg").arg("-version").stdout(Stdio::null()).status().is_err() {
            eprintln!("ffmpeg not installed; skipping the decode check");
            return;
        }
        let mut dec = Command::new("ffmpeg")
            .args(["-hide_banner", "-loglevel", "error", "-f", "h264", "-i", "pipe:0", "-f", "rawvideo", "-pix_fmt", "yuv420p", "pipe:1"])
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .unwrap();
        let mut stdin = dec.stdin.take().unwrap();
        let feeder = std::thread::spawn(move || stdin.write_all(&stream).unwrap());
        let out = dec.wait_with_output().unwrap();
        feeder.join().unwrap();
        assert!(out.status.success(), "decode failed: {}", String::from_utf8_lossy(&out.stderr));
        assert!(out.stderr.is_empty(), "decoder complained: {}", String::from_utf8_lossy(&out.stderr));
        let frame_len = w * h * 3 / 2;
        assert_eq!(out.stdout.len(), frame_len * FRAMES, "decoded frames");
        let mut worst = f64::INFINITY;
        for (i, want) in inputs.iter().enumerate() {
            let got = &out.stdout[i * frame_len..i * frame_len + w * h];
            let mse = want.iter().zip(got).map(|(&a, &b)| (a as f64 - b as f64).powi(2)).sum::<f64>() / (w * h) as f64;
            let psnr = if mse == 0.0 { 99.0 } else { 10.0 * (255.0 * 255.0 / mse).log10() };
            worst = worst.min(psnr);
        }
        eprintln!("lowest luma PSNR over {FRAMES} frames: {worst:.1} dB");
        assert!(worst > 30.0, "lowest luma PSNR {worst:.1} dB");
    }
}

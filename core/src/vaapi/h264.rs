//! Low-latency H.264 encoding through VA-API, with the parameters the
//! displays need: Constrained Baseline, one slice per frame, IDR at the start of
//! every GOP, P-frames with a single reference, no B-frames, CBR, and an AUD in
//! front of every access unit. The driver writes SPS, PPS and slice headers
//! itself (no packed headers), and SPS/PPS precede every IDR.
//!
//! Each frame is encoded synchronously: the picture is submitted, the input
//! surface is synced and the coded buffer read back before `encode` returns,
//! which is the lowest latency the hardware allows.

use super::sys::{self, *};
use super::Display;
use crate::nv12::Nv12Planes;
use anyhow::{bail, Result};
use std::ffi::c_void;
use std::ptr;

#[derive(Debug, Clone, Copy)]
pub struct Params {
    pub width: usize,
    pub height: usize,
    pub fps: u32,
    pub bitrate: u32,
    /// HRD buffer size in bits.
    pub buffer_bits: u32,
    /// Frames per GOP; every GOP starts with an IDR.
    pub gop: u32,
}

const AUD: [u8; 6] = [0, 0, 0, 1, 0x09, 0xF0]; // primary_pic_type 7: any slice type
const LOG2_MAX_FRAME_NUM: u32 = 8;
const LOG2_MAX_POC_LSB: u32 = 8;
const INIT_QP: u8 = 26;
/// Level 4.0: 1920x544 at 30 fps is 122,400 macroblocks per second.
const LEVEL_IDC: u8 = 40;

/// How frames get into the input surface.
enum Upload {
    /// The surface is mapped directly (vaDeriveImage).
    Derive,
    /// A separate image is filled and copied to the surface (vaPutImage).
    Put(VAImage),
}

#[derive(Clone, Copy)]
struct RefPic {
    surface: VASurfaceID,
    frame_num: u32,
    poc: i32,
}

pub struct Encoder {
    d: Display,
    p: Params,
    entrypoint: &'static str,
    config: VAConfigID,
    context: VAContextID,
    input: VASurfaceID,
    recon: [VASurfaceID; 2],
    coded: VABufferID,
    upload: Upload,
    width_mbs: u32,
    height_mbs: u32,

    frame_in_gop: u32,
    frame_num: u32,
    poc: i32,
    idr_pic_id: u16,
    slot: usize,
    reference: Option<RefPic>,
    out: Vec<u8>,
    /// SPS and PPS, put after the AUD of every IDR.
    param_sets: Vec<u8>,
}

// SAFETY: all VA objects belong to `d`, which moves with the encoder.
unsafe impl Send for Encoder {}

impl Encoder {
    pub fn new(device: &str, p: Params) -> Result<Self> {
        if p.width % 2 != 0 || p.height % 2 != 0 || p.width == 0 || p.height == 0 {
            bail!("unsupported size {}x{} (must be even)", p.width, p.height);
        }
        let d = Display::open(device)?;
        let api = d.api;
        let profile = VA_PROFILE_H264_CONSTRAINED_BASELINE;

        // Prefer the full encoder (VME); the low-power one is a fallback.
        // SAFETY: valid display.
        let max = unsafe { (api.vaMaxNumEntrypoints)(d.dpy) }.max(1) as usize;
        let mut eps = vec![0; max];
        let mut n = 0;
        // SAFETY: eps holds vaMaxNumEntrypoints entries.
        d.check(unsafe { (api.vaQueryConfigEntrypoints)(d.dpy, profile, eps.as_mut_ptr(), &mut n) }, "vaQueryConfigEntrypoints")?;
        eps.truncate(n.max(0) as usize);
        let (entrypoint, ep_name) = if eps.contains(&VA_ENTRYPOINT_ENC_SLICE) {
            (VA_ENTRYPOINT_ENC_SLICE, "EncSlice")
        } else if eps.contains(&VA_ENTRYPOINT_ENC_SLICE_LP) {
            (VA_ENTRYPOINT_ENC_SLICE_LP, "EncSliceLP")
        } else {
            bail!("{}: no H.264 Constrained Baseline encoder", d.vendor);
        };

        let mut attrs = [
            VAConfigAttrib { type_: VA_CONFIG_ATTRIB_RT_FORMAT, value: 0 },
            VAConfigAttrib { type_: VA_CONFIG_ATTRIB_RATE_CONTROL, value: 0 },
            VAConfigAttrib { type_: VA_CONFIG_ATTRIB_ENC_PACKED_HEADERS, value: 0 },
        ];
        // SAFETY: attrs is a valid in/out array.
        d.check(
            unsafe { (api.vaGetConfigAttributes)(d.dpy, profile, entrypoint, attrs.as_mut_ptr(), attrs.len() as i32) },
            "vaGetConfigAttributes",
        )?;
        if attrs[0].value & VA_RT_FORMAT_YUV420 == 0 {
            bail!("{}: YUV 4:2:0 encoding not supported", d.vendor);
        }
        if attrs[1].value == VA_ATTRIB_NOT_SUPPORTED || attrs[1].value & VA_RC_CBR == 0 {
            bail!("{}: CBR rate control not supported", d.vendor);
        }
        let mut wanted = vec![
            VAConfigAttrib { type_: VA_CONFIG_ATTRIB_RT_FORMAT, value: VA_RT_FORMAT_YUV420 },
            VAConfigAttrib { type_: VA_CONFIG_ATTRIB_RATE_CONTROL, value: VA_RC_CBR },
        ];
        if attrs[2].value != VA_ATTRIB_NOT_SUPPORTED {
            // No packed headers: the driver writes SPS, PPS and slice headers.
            wanted.push(VAConfigAttrib { type_: VA_CONFIG_ATTRIB_ENC_PACKED_HEADERS, value: VA_ENC_PACKED_HEADER_NONE });
        }
        let mut config = VA_INVALID_ID;
        // SAFETY: valid display and attribute list.
        d.check(
            unsafe { (api.vaCreateConfig)(d.dpy, profile, entrypoint, wanted.as_mut_ptr(), wanted.len() as i32, &mut config) },
            "vaCreateConfig",
        )?;

        let width_mbs = p.width.div_ceil(16) as u32;
        let height_mbs = p.height.div_ceil(16) as u32;
        let (aw, ah) = (width_mbs * 16, height_mbs * 16);
        let mut enc = Self {
            d,
            p,
            entrypoint: ep_name,
            config,
            context: VA_INVALID_ID,
            input: VA_INVALID_SURFACE,
            recon: [VA_INVALID_SURFACE; 2],
            coded: VA_INVALID_ID,
            upload: Upload::Derive,
            width_mbs,
            height_mbs,
            frame_in_gop: 0,
            frame_num: 0,
            poc: 0,
            idr_pic_id: 0,
            slot: 0,
            reference: None,
            out: Vec::new(),
            param_sets: Vec::new(),
        };
        // From here on, Drop releases whatever was created.
        let (api, dpy) = (enc.d.api, enc.d.dpy);
        let mut surfaces = [VA_INVALID_SURFACE; 3];
        // SAFETY: surfaces holds 3 entries.
        enc.d.check(
            unsafe { (api.vaCreateSurfaces)(dpy, VA_RT_FORMAT_YUV420, aw, ah, surfaces.as_mut_ptr(), 3, ptr::null_mut(), 0) },
            "vaCreateSurfaces",
        )?;
        enc.input = surfaces[0];
        enc.recon = [surfaces[1], surfaces[2]];
        let mut ctx = VA_INVALID_ID;
        // SAFETY: the render targets are the surfaces just created.
        enc.d.check(
            unsafe { (api.vaCreateContext)(dpy, config, aw as i32, ah as i32, VA_PROGRESSIVE, surfaces.as_mut_ptr(), 3, &mut ctx) },
            "vaCreateContext",
        )?;
        enc.context = ctx;
        // Room for an intra frame at very high quality; CBR keeps real frames far smaller.
        let coded_size = (aw * ah * 3 / 2).max(1 << 20);
        let mut coded = VA_INVALID_ID;
        // SAFETY: valid context; no initial data.
        enc.d.check(
            unsafe { (api.vaCreateBuffer)(dpy, ctx, VA_ENC_CODED_BUFFER_TYPE, coded_size, 1, ptr::null_mut(), &mut coded) },
            "vaCreateBuffer(coded)",
        )?;
        enc.coded = coded;
        enc.upload = enc.choose_upload(aw, ah)?;
        enc.param_sets = super::headers::sps(&super::headers::SpsParams {
            level_idc: LEVEL_IDC,
            width_mbs,
            height_mbs,
            crop_right: aw - p.width as u32,
            crop_bottom: ah - p.height as u32,
            log2_max_frame_num: LOG2_MAX_FRAME_NUM,
            log2_max_poc_lsb: LOG2_MAX_POC_LSB,
            fps: p.fps,
        });
        enc.param_sets.extend(super::headers::pps(INIT_QP));
        Ok(enc)
    }

    /// "<vendor>, <entrypoint>, <upload>" for the log.
    pub fn describe(&self) -> String {
        let up = match self.upload {
            Upload::Derive => "derived image",
            Upload::Put(_) => "put image",
        };
        format!("{} ({}, {up})", self.d.vendor, self.entrypoint)
    }

    /// Derives the input surface if the driver allows it as NV12, otherwise
    /// prepares an NV12 image to copy from.
    fn choose_upload(&self, aw: u32, ah: u32) -> Result<Upload> {
        let (api, dpy) = (self.d.api, self.d.dpy);
        let mut img: VAImage = zeroed();
        // SAFETY: valid surface and out-pointer.
        if unsafe { (api.vaDeriveImage)(dpy, self.input, &mut img) } == VA_STATUS_SUCCESS {
            let nv12 = img.format.fourcc == VA_FOURCC_NV12;
            // SAFETY: image just created.
            unsafe { (api.vaDestroyImage)(dpy, img.image_id) };
            if nv12 {
                return Ok(Upload::Derive);
            }
        }
        let mut fmt = VAImageFormat { fourcc: VA_FOURCC_NV12, byte_order: 1, bits_per_pixel: 12, ..Default::default() };
        let mut img: VAImage = zeroed();
        // SAFETY: valid format and out-pointer.
        self.d.check(unsafe { (api.vaCreateImage)(dpy, &mut fmt, aw as i32, ah as i32, &mut img) }, "vaCreateImage")?;
        Ok(Upload::Put(img))
    }

    /// Fills the input surface through `fill`, which receives the mapped NV12 planes.
    fn upload(&mut self, fill: &mut dyn FnMut(&mut Nv12Planes)) -> Result<()> {
        let (api, dpy) = (self.d.api, self.d.dpy);
        let img = match &self.upload {
            Upload::Derive => {
                let mut img: VAImage = zeroed();
                // SAFETY: valid surface, not in use (the last encode was synced).
                self.d.check(unsafe { (api.vaDeriveImage)(dpy, self.input, &mut img) }, "vaDeriveImage")?;
                img
            }
            Upload::Put(img) => *img,
        };
        let mut p: *mut c_void = ptr::null_mut();
        // SAFETY: the image's buffer, mapped until vaUnmapBuffer below.
        let mapped = self.d.check(unsafe { (api.vaMapBuffer)(dpy, img.buf, &mut p) }, "vaMapBuffer(image)");
        if mapped.is_ok() {
            // SAFETY: the mapping covers data_size bytes; the planes are disjoint ranges of it.
            unsafe {
                let base = p as *mut u8;
                let (yo, uvo) = (img.offsets[0] as usize, img.offsets[1] as usize);
                let (yp, uvp) = (img.pitches[0] as usize, img.pitches[1] as usize);
                let ah = self.height_mbs as usize * 16;
                let mut planes = Nv12Planes {
                    width: self.p.width,
                    height: ah,
                    y: std::slice::from_raw_parts_mut(base.add(yo), yp * ah),
                    y_pitch: yp,
                    uv: std::slice::from_raw_parts_mut(base.add(uvo), uvp * ah / 2),
                    uv_pitch: uvp,
                };
                fill(&mut planes);
                (api.vaUnmapBuffer)(dpy, img.buf);
            }
        }
        let result = match self.upload {
            Upload::Derive => {
                // SAFETY: derived image no longer mapped.
                unsafe { (api.vaDestroyImage)(dpy, img.image_id) };
                mapped
            }
            Upload::Put(_) => mapped.and_then(|_| {
                let (w, h) = (self.width_mbs * 16, self.height_mbs * 16);
                // SAFETY: valid image and surface of the same size.
                self.d.check(unsafe { (api.vaPutImage)(dpy, self.input, img.image_id, 0, 0, w, h, 0, 0, w, h) }, "vaPutImage")
            }),
        };
        result
    }

    fn render<T>(&self, buf_type: VABufferType, data: &T) -> Result<()> {
        let (api, dpy) = (self.d.api, self.d.dpy);
        let mut id = VA_INVALID_ID;
        // SAFETY: data is a plain C struct of size_of::<T>() bytes; libva copies it.
        self.d.check(
            unsafe {
                (api.vaCreateBuffer)(dpy, self.context, buf_type, size_of::<T>() as u32, 1, data as *const T as *mut c_void, &mut id)
            },
            "vaCreateBuffer",
        )?;
        // SAFETY: valid context and buffer.
        let r = self.d.check(unsafe { (api.vaRenderPicture)(dpy, self.context, &mut id, 1) }, "vaRenderPicture");
        // Buffers can be destroyed once rendered; the driver has consumed them.
        // SAFETY: valid buffer.
        unsafe { (api.vaDestroyBuffer)(dpy, id) };
        r
    }

    fn render_sequence(&self) -> Result<()> {
        let p = &self.p;
        let mut seq: VAEncSequenceParameterBufferH264 = zeroed();
        seq.level_idc = LEVEL_IDC;
        seq.intra_period = p.gop;
        seq.intra_idr_period = p.gop;
        seq.ip_period = 1;
        seq.bits_per_second = p.bitrate;
        seq.max_num_ref_frames = 1;
        seq.picture_width_in_mbs = self.width_mbs as u16;
        seq.picture_height_in_mbs = self.height_mbs as u16;
        seq.seq_fields = 1 << seq_fields::CHROMA_FORMAT_IDC
            | 1 << seq_fields::FRAME_MBS_ONLY_FLAG
            | 1 << seq_fields::DIRECT_8X8_INFERENCE_FLAG
            | (LOG2_MAX_FRAME_NUM - 4) << seq_fields::LOG2_MAX_FRAME_NUM_MINUS4
            | (LOG2_MAX_POC_LSB - 4) << seq_fields::LOG2_MAX_PIC_ORDER_CNT_LSB_MINUS4;
        let (aw, ah) = (self.width_mbs as usize * 16, self.height_mbs as usize * 16);
        if aw != p.width || ah != p.height {
            // Crop offsets are in units of 2 luma samples for 4:2:0.
            seq.frame_cropping_flag = 1;
            seq.frame_crop_right_offset = ((aw - p.width) / 2) as u32;
            seq.frame_crop_bottom_offset = ((ah - p.height) / 2) as u32;
        }
        seq.vui_parameters_present_flag = 1;
        seq.vui_fields = 1 << vui_fields::TIMING_INFO_PRESENT_FLAG
            | 1 << vui_fields::FIXED_FRAME_RATE_FLAG
            | 1 << vui_fields::BITSTREAM_RESTRICTION_FLAG
            | 15 << vui_fields::LOG2_MAX_MV_LENGTH_HORIZONTAL
            | 15 << vui_fields::LOG2_MAX_MV_LENGTH_VERTICAL;
        seq.num_units_in_tick = 1;
        seq.time_scale = p.fps * 2;
        self.render(VA_ENC_SEQUENCE_PARAMETER_BUFFER_TYPE, &seq)?;

        let rc = MiscParam {
            type_: VA_ENC_MISC_PARAMETER_TYPE_RATE_CONTROL,
            data: VAEncMiscParameterRateControl {
                bits_per_second: p.bitrate,
                target_percentage: 100,
                window_size: 1000,
                initial_qp: INIT_QP as u32,
                rc_flags: RC_FLAG_DISABLE_FRAME_SKIP,
                ..Default::default()
            },
        };
        self.render(VA_ENC_MISC_PARAMETER_BUFFER_TYPE, &rc)?;
        let hrd = MiscParam {
            type_: VA_ENC_MISC_PARAMETER_TYPE_HRD,
            data: VAEncMiscParameterHRD {
                initial_buffer_fullness: p.buffer_bits / 4 * 3,
                buffer_size: p.buffer_bits,
                ..Default::default()
            },
        };
        self.render(VA_ENC_MISC_PARAMETER_BUFFER_TYPE, &hrd)?;
        let fr = MiscParam {
            type_: VA_ENC_MISC_PARAMETER_TYPE_FRAME_RATE,
            data: VAEncMiscParameterFrameRate { framerate: p.fps, ..Default::default() },
        };
        self.render(VA_ENC_MISC_PARAMETER_BUFFER_TYPE, &fr)
    }

    /// Encodes one frame, filled into the NV12 input by `fill`. Returns the
    /// access unit (Annex-B, AUD first) and whether it is an IDR. The data is
    /// valid until the next call.
    pub fn encode(&mut self, mut fill: impl FnMut(&mut Nv12Planes)) -> Result<(&[u8], bool)> {
        self.upload(&mut fill)?;
        let idr = self.frame_in_gop == 0;
        if idr {
            self.frame_num = 0;
            self.poc = 0;
            self.reference = None;
        }
        let recon = self.recon[self.slot];
        let (api, dpy) = (self.d.api, self.d.dpy);
        // SAFETY: valid context and render target.
        self.d.check(unsafe { (api.vaBeginPicture)(dpy, self.context, self.input) }, "vaBeginPicture")?;
        let submitted = self.submit(idr, recon);
        // SAFETY: ends the picture begun above, even after a failed submit.
        let ended = self.d.check(unsafe { (api.vaEndPicture)(dpy, self.context) }, "vaEndPicture");
        submitted.and(ended)?;
        // SAFETY: valid surface.
        self.d.check(unsafe { (api.vaSyncSurface)(dpy, self.input) }, "vaSyncSurface")?;
        self.read_coded(idr)?;

        self.reference = Some(RefPic { surface: recon, frame_num: self.frame_num, poc: self.poc });
        self.slot ^= 1;
        self.frame_num = (self.frame_num + 1) % (1 << LOG2_MAX_FRAME_NUM);
        self.poc += 2;
        self.frame_in_gop = (self.frame_in_gop + 1) % self.p.gop.max(1);
        if idr {
            self.idr_pic_id = self.idr_pic_id.wrapping_add(1);
        }
        Ok((&self.out, idr))
    }

    fn submit(&self, idr: bool, recon: VASurfaceID) -> Result<()> {
        if idr {
            self.render_sequence()?;
        }
        let ref_pic = self.reference.filter(|_| !idr).map(|r| VAPictureH264 {
            picture_id: r.surface,
            frame_idx: r.frame_num,
            flags: VA_PICTURE_H264_SHORT_TERM_REFERENCE,
            TopFieldOrderCnt: r.poc,
            BottomFieldOrderCnt: r.poc,
            va_reserved: [0; 4],
        });

        let mut pic: VAEncPictureParameterBufferH264 = zeroed();
        pic.CurrPic = VAPictureH264 {
            picture_id: recon,
            frame_idx: self.frame_num,
            flags: 0,
            TopFieldOrderCnt: self.poc,
            BottomFieldOrderCnt: self.poc,
            va_reserved: [0; 4],
        };
        pic.ReferenceFrames = [VAPictureH264::INVALID; 16];
        if let Some(r) = ref_pic {
            pic.ReferenceFrames[0] = r;
        }
        pic.coded_buf = self.coded;
        pic.frame_num = self.frame_num as u16;
        pic.pic_init_qp = INIT_QP;
        pic.pic_fields = (idr as u32) << pic_fields::IDR_PIC_FLAG
            | 1 << pic_fields::REFERENCE_PIC_FLAG
            | 1 << pic_fields::DEBLOCKING_FILTER_CONTROL_PRESENT_FLAG;
        self.render(VA_ENC_PICTURE_PARAMETER_BUFFER_TYPE, &pic)?;

        let mut slice: VAEncSliceParameterBufferH264 = zeroed();
        slice.num_macroblocks = self.width_mbs * self.height_mbs;
        slice.macroblock_info = VA_INVALID_ID;
        slice.slice_type = if ref_pic.is_some() { SLICE_TYPE_P } else { SLICE_TYPE_I };
        slice.idr_pic_id = self.idr_pic_id;
        slice.pic_order_cnt_lsb = (self.poc as u32 % (1 << LOG2_MAX_POC_LSB)) as u16;
        slice.direct_spatial_mv_pred_flag = 1;
        slice.RefPicList0 = [VAPictureH264::INVALID; 32];
        slice.RefPicList1 = [VAPictureH264::INVALID; 32];
        if let Some(r) = ref_pic {
            slice.RefPicList0[0] = r;
        }
        self.render(VA_ENC_SLICE_PARAMETER_BUFFER_TYPE, &slice)
    }

    fn read_coded(&mut self, idr: bool) -> Result<()> {
        let (api, dpy) = (self.d.api, self.d.dpy);
        let mut p: *mut c_void = ptr::null_mut();
        // SAFETY: the coded buffer of the synced picture.
        self.d.check(unsafe { (api.vaMapBuffer)(dpy, self.coded, &mut p) }, "vaMapBuffer(coded)")?;
        self.out.clear();
        self.out.extend_from_slice(&AUD);
        if idr {
            self.out.extend_from_slice(&self.param_sets);
        }
        let mut seg = p as *const VACodedBufferSegment;
        while !seg.is_null() {
            // SAFETY: the driver's segment list, valid while mapped.
            unsafe {
                let s = &*seg;
                if !s.buf.is_null() && s.size > 0 {
                    self.out.extend_from_slice(std::slice::from_raw_parts(s.buf as *const u8, s.size as usize));
                }
                seg = s.next as *const VACodedBufferSegment;
            }
        }
        // SAFETY: mapped above.
        unsafe { (api.vaUnmapBuffer)(dpy, self.coded) };
        Ok(())
    }
}

impl Drop for Encoder {
    fn drop(&mut self) {
        let (api, dpy) = (self.d.api, self.d.dpy);
        // SAFETY: each object is destroyed once, before the display is terminated.
        unsafe {
            if let Upload::Put(img) = &self.upload {
                (api.vaDestroyImage)(dpy, img.image_id);
            }
            if self.coded != VA_INVALID_ID {
                (api.vaDestroyBuffer)(dpy, self.coded);
            }
            if self.context != VA_INVALID_ID {
                (api.vaDestroyContext)(dpy, self.context);
            }
            let mut surfaces = [self.input, self.recon[0], self.recon[1]];
            if surfaces[0] != VA_INVALID_SURFACE {
                (api.vaDestroySurfaces)(dpy, surfaces.as_mut_ptr(), 3);
            }
            (api.vaDestroyConfig)(dpy, self.config);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::nv12::bgrx_to_nv12;
    use std::io::Write;
    use std::process::{Command, Stdio};

    const DEVICE: &str = "/dev/dri/renderD128";

    /// Encodes a moving test pattern and checks the stream with ffmpeg:
    /// every frame decodes, IDRs start each GOP and carry SPS/PPS, and every
    /// access unit starts with an AUD. Runs with the default driver; set
    /// LIBVA_DRIVER_NAME=i965 to test the Ivy Bridge driver.
    #[test]
    fn encodes_decodable_stream() {
        if !std::path::Path::new(DEVICE).exists() {
            eprintln!("no {DEVICE}; skipping");
            return;
        }
        let p = Params { width: 640, height: 360, fps: 30, bitrate: 4_000_000, buffer_bits: 1_000_000, gop: 15 };
        let mut enc = match Encoder::new(DEVICE, p) {
            Ok(e) => e,
            Err(e) => {
                eprintln!("VA-API H.264 encoding unavailable: {e:#}; skipping");
                return;
            }
        };
        eprintln!("encoder: {}", enc.describe());
        const FRAMES: usize = 45;
        const W: usize = 640;
        const H: usize = 360;
        let mut stream = Vec::new();
        let mut keys = 0;
        let mut bgrx = vec![0u8; W * H * 4];
        let mut want_luma = Vec::new(); // the Y planes that went in (limited range)
        for i in 0..FRAMES {
            // Smooth gradients that move, so P-frames have real motion to code.
            for (j, px) in bgrx.chunks_exact_mut(4).enumerate() {
                let (x, y) = (j % W, j / W);
                px[0] = (x / 3 + i * 4) as u8;
                px[1] = (y / 2 + i * 2) as u8;
                px[2] = ((x + y) / 4) as u8;
            }
            let (mut yp, mut uvp) = (vec![0u8; W * 368], vec![0u8; W * 184]);
            bgrx_to_nv12(&bgrx, W * 4, 0, 0, H, &mut Nv12Planes { width: W, height: 368, y: &mut yp, y_pitch: W, uv: &mut uvp, uv_pitch: W });
            want_luma.extend_from_slice(&yp[..W * H]);

            let (au, idr) = enc.encode(|planes| bgrx_to_nv12(&bgrx, W * 4, 0, 0, H, planes)).unwrap();
            if std::env::var_os("DUMP_NALS").is_some() && i < 3 {
                let types: Vec<u8> = au.windows(4).enumerate().filter(|(_, w)| w[..3] == [0, 0, 1]).map(|(k, _)| au[k + 3] & 0x1f).collect();
                eprintln!("frame {i}: {} bytes, NAL types {types:?}", au.len());
            }
            assert_eq!(&au[..6], &AUD, "frame {i} must start with an AUD");
            assert_eq!(idr, i % 15 == 0, "frame {i}");
            if idr {
                keys += 1;
                assert_eq!(&au[6..11], &[0, 0, 0, 1, 0x67], "IDR {i}: SPS right after the AUD");
            }
            stream.extend_from_slice(au);
        }
        assert_eq!(keys, 3);

        // Decode and compare the luma with what went in.
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
        assert!(out.status.success() && out.stderr.is_empty(), "decoder: {}", String::from_utf8_lossy(&out.stderr));
        assert_eq!(out.stdout.len(), W * H * 3 / 2 * FRAMES, "decoded frames (cropped to {W}x{H})");
        let mut worst = f64::MAX;
        for (i, (frame, want)) in out.stdout.chunks_exact(W * H * 3 / 2).zip(want_luma.chunks_exact(W * H)).enumerate() {
            let got = &frame[..W * H]; // the Y plane, as decoded
            let mse = got.iter().zip(want).map(|(&a, &b)| (a as f64 - b as f64).powi(2)).sum::<f64>() / (W * H) as f64;
            let psnr = 10.0 * (255.0f64 * 255.0 / mse.max(1e-9)).log10();
            assert!(psnr > 35.0, "frame {i}: luma PSNR {psnr:.1} dB, the reference chain is broken");
            worst = worst.min(psnr);
        }
        eprintln!("lowest luma PSNR over {FRAMES} frames: {worst:.1} dB");

    }
}

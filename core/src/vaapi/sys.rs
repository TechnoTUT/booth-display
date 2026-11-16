//! Raw C ABI of libva (subset), transcribed from the libva 2.22 headers
//! (`va.h`, `va_enc_h264.h`, `va_drm.h`). Only what the H.264 encoder uses is
//! declared. libva is loaded at run time, so no headers or link-time library
//! are needed to build; the layout tests below pin every struct to the sizes
//! and offsets a C compiler reports for the real headers.

#![allow(non_camel_case_types, non_snake_case, dead_code)]

use std::os::raw::{c_char, c_int, c_uint, c_void};

pub type VADisplay = *mut c_void;
pub type VAStatus = c_int;
pub type VAGenericID = c_uint;
pub type VAConfigID = VAGenericID;
pub type VAContextID = VAGenericID;
pub type VASurfaceID = VAGenericID;
pub type VABufferID = VAGenericID;
pub type VAImageID = VAGenericID;
pub type VAProfile = c_int;
pub type VAEntrypoint = c_int;
pub type VABufferType = c_int;

pub const VA_STATUS_SUCCESS: VAStatus = 0;
pub const VA_INVALID_ID: u32 = 0xffff_ffff;
pub const VA_INVALID_SURFACE: u32 = VA_INVALID_ID;

pub const VA_PROFILE_H264_CONSTRAINED_BASELINE: VAProfile = 13;
pub const VA_ENTRYPOINT_ENC_SLICE: VAEntrypoint = 6;
pub const VA_ENTRYPOINT_ENC_SLICE_LP: VAEntrypoint = 8;

pub const VA_CONFIG_ATTRIB_RT_FORMAT: c_int = 0;
pub const VA_CONFIG_ATTRIB_RATE_CONTROL: c_int = 5;
pub const VA_CONFIG_ATTRIB_ENC_PACKED_HEADERS: c_int = 10;
pub const VA_ATTRIB_NOT_SUPPORTED: u32 = 0x8000_0000;

pub const VA_RT_FORMAT_YUV420: u32 = 0x1;
pub const VA_RC_CBR: u32 = 0x2;
pub const VA_ENC_PACKED_HEADER_NONE: u32 = 0;
pub const VA_PROGRESSIVE: c_int = 0x1;
pub const VA_FOURCC_NV12: u32 = 0x3231_564e;

pub const VA_ENC_CODED_BUFFER_TYPE: VABufferType = 21;
pub const VA_ENC_SEQUENCE_PARAMETER_BUFFER_TYPE: VABufferType = 22;
pub const VA_ENC_PICTURE_PARAMETER_BUFFER_TYPE: VABufferType = 23;
pub const VA_ENC_SLICE_PARAMETER_BUFFER_TYPE: VABufferType = 24;
pub const VA_ENC_MISC_PARAMETER_BUFFER_TYPE: VABufferType = 27;

pub const VA_ENC_MISC_PARAMETER_TYPE_FRAME_RATE: u32 = 0;
pub const VA_ENC_MISC_PARAMETER_TYPE_RATE_CONTROL: u32 = 1;
pub const VA_ENC_MISC_PARAMETER_TYPE_HRD: u32 = 5;

pub const VA_PICTURE_H264_INVALID: u32 = 0x1;
pub const VA_PICTURE_H264_SHORT_TERM_REFERENCE: u32 = 0x8;

/// slice_type values (H.264 table 7-6).
pub const SLICE_TYPE_P: u8 = 0;
pub const SLICE_TYPE_I: u8 = 2;

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct VAConfigAttrib {
    pub type_: c_int,
    pub value: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct VAImageFormat {
    pub fourcc: u32,
    pub byte_order: u32,
    pub bits_per_pixel: u32,
    pub depth: u32,
    pub red_mask: u32,
    pub green_mask: u32,
    pub blue_mask: u32,
    pub alpha_mask: u32,
    pub va_reserved: [u32; 4],
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct VAImage {
    pub image_id: VAImageID,
    pub format: VAImageFormat,
    pub buf: VABufferID,
    pub width: u16,
    pub height: u16,
    pub data_size: u32,
    pub num_planes: u32,
    pub pitches: [u32; 3],
    pub offsets: [u32; 3],
    pub num_palette_entries: i32,
    pub entry_bytes: i32,
    pub component_order: [i8; 4],
    pub va_reserved: [u32; 4],
}

#[repr(C)]
pub struct VACodedBufferSegment {
    pub size: u32,
    pub bit_offset: u32,
    pub status: u32,
    pub reserved: u32,
    pub buf: *mut c_void,
    pub next: *mut c_void,
    pub va_reserved: [u32; 4],
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct VAPictureH264 {
    pub picture_id: VASurfaceID,
    pub frame_idx: u32,
    pub flags: u32,
    pub TopFieldOrderCnt: i32,
    pub BottomFieldOrderCnt: i32,
    pub va_reserved: [u32; 4],
}

impl VAPictureH264 {
    pub const INVALID: Self = Self {
        picture_id: VA_INVALID_SURFACE,
        frame_idx: 0,
        flags: VA_PICTURE_H264_INVALID,
        TopFieldOrderCnt: 0,
        BottomFieldOrderCnt: 0,
        va_reserved: [0; 4],
    };
}

/// `seq_fields` bit positions.
pub mod seq_fields {
    pub const CHROMA_FORMAT_IDC: u32 = 0; // 2 bits
    pub const FRAME_MBS_ONLY_FLAG: u32 = 2;
    pub const DIRECT_8X8_INFERENCE_FLAG: u32 = 5;
    pub const LOG2_MAX_FRAME_NUM_MINUS4: u32 = 6; // 4 bits
    pub const PIC_ORDER_CNT_TYPE: u32 = 10; // 2 bits
    pub const LOG2_MAX_PIC_ORDER_CNT_LSB_MINUS4: u32 = 12; // 4 bits
}

/// `vui_fields` bit positions.
pub mod vui_fields {
    pub const TIMING_INFO_PRESENT_FLAG: u32 = 1;
    pub const BITSTREAM_RESTRICTION_FLAG: u32 = 2;
    pub const LOG2_MAX_MV_LENGTH_HORIZONTAL: u32 = 3; // 5 bits
    pub const LOG2_MAX_MV_LENGTH_VERTICAL: u32 = 8; // 5 bits
    pub const FIXED_FRAME_RATE_FLAG: u32 = 13;
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct VAEncSequenceParameterBufferH264 {
    pub seq_parameter_set_id: u8,
    pub level_idc: u8,
    pub intra_period: u32,
    pub intra_idr_period: u32,
    pub ip_period: u32,
    pub bits_per_second: u32,
    pub max_num_ref_frames: u32,
    pub picture_width_in_mbs: u16,
    pub picture_height_in_mbs: u16,
    pub seq_fields: u32,
    pub bit_depth_luma_minus8: u8,
    pub bit_depth_chroma_minus8: u8,
    pub num_ref_frames_in_pic_order_cnt_cycle: u8,
    pub offset_for_non_ref_pic: i32,
    pub offset_for_top_to_bottom_field: i32,
    pub offset_for_ref_frame: [i32; 256],
    pub frame_cropping_flag: u8,
    pub frame_crop_left_offset: u32,
    pub frame_crop_right_offset: u32,
    pub frame_crop_top_offset: u32,
    pub frame_crop_bottom_offset: u32,
    pub vui_parameters_present_flag: u8,
    pub vui_fields: u32,
    pub aspect_ratio_idc: u8,
    pub sar_width: u32,
    pub sar_height: u32,
    pub num_units_in_tick: u32,
    pub time_scale: u32,
    pub va_reserved: [u32; 4],
}

/// `pic_fields` bit positions.
pub mod pic_fields {
    pub const IDR_PIC_FLAG: u32 = 0;
    pub const REFERENCE_PIC_FLAG: u32 = 1; // 2 bits
    pub const ENTROPY_CODING_MODE_FLAG: u32 = 3;
    pub const DEBLOCKING_FILTER_CONTROL_PRESENT_FLAG: u32 = 9;
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct VAEncPictureParameterBufferH264 {
    pub CurrPic: VAPictureH264,
    pub ReferenceFrames: [VAPictureH264; 16],
    pub coded_buf: VABufferID,
    pub pic_parameter_set_id: u8,
    pub seq_parameter_set_id: u8,
    pub last_picture: u8,
    pub frame_num: u16,
    pub pic_init_qp: u8,
    pub num_ref_idx_l0_active_minus1: u8,
    pub num_ref_idx_l1_active_minus1: u8,
    pub chroma_qp_index_offset: i8,
    pub second_chroma_qp_index_offset: i8,
    pub pic_fields: u32,
    pub va_reserved: [u32; 4],
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct VAEncSliceParameterBufferH264 {
    pub macroblock_address: u32,
    pub num_macroblocks: u32,
    pub macroblock_info: VABufferID,
    pub slice_type: u8,
    pub pic_parameter_set_id: u8,
    pub idr_pic_id: u16,
    pub pic_order_cnt_lsb: u16,
    pub delta_pic_order_cnt_bottom: i32,
    pub delta_pic_order_cnt: [i32; 2],
    pub direct_spatial_mv_pred_flag: u8,
    pub num_ref_idx_active_override_flag: u8,
    pub num_ref_idx_l0_active_minus1: u8,
    pub num_ref_idx_l1_active_minus1: u8,
    pub RefPicList0: [VAPictureH264; 32],
    pub RefPicList1: [VAPictureH264; 32],
    pub luma_log2_weight_denom: u8,
    pub chroma_log2_weight_denom: u8,
    pub luma_weight_l0_flag: u8,
    pub luma_weight_l0: [i16; 32],
    pub luma_offset_l0: [i16; 32],
    pub chroma_weight_l0_flag: u8,
    pub chroma_weight_l0: [[i16; 2]; 32],
    pub chroma_offset_l0: [[i16; 2]; 32],
    pub luma_weight_l1_flag: u8,
    pub luma_weight_l1: [i16; 32],
    pub luma_offset_l1: [i16; 32],
    pub chroma_weight_l1_flag: u8,
    pub chroma_weight_l1: [[i16; 2]; 32],
    pub chroma_offset_l1: [[i16; 2]; 32],
    pub cabac_init_idc: u8,
    pub slice_qp_delta: i8,
    pub disable_deblocking_filter_idc: u8,
    pub slice_alpha_c0_offset_div2: i8,
    pub slice_beta_offset_div2: i8,
    pub va_reserved: [u32; 4],
}

/// A misc parameter buffer: the `VAEncMiscParameterBuffer` header (its type)
/// followed by the parameter struct.
#[repr(C)]
#[derive(Clone, Copy)]
pub struct MiscParam<T> {
    pub type_: u32,
    pub data: T,
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct VAEncMiscParameterRateControl {
    pub bits_per_second: u32,
    pub target_percentage: u32,
    pub window_size: u32,
    pub initial_qp: u32,
    pub min_qp: u32,
    pub basic_unit_size: u32,
    pub rc_flags: u32,
    pub ICQ_quality_factor: u32,
    pub max_qp: u32,
    pub quality_factor: u32,
    pub target_frame_size: u32,
    pub va_reserved: [u32; 4],
}

/// `rc_flags` bits.
pub const RC_FLAG_DISABLE_FRAME_SKIP: u32 = 1 << 1;

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct VAEncMiscParameterHRD {
    pub initial_buffer_fullness: u32,
    pub buffer_size: u32,
    pub va_reserved: [u32; 4],
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct VAEncMiscParameterFrameRate {
    /// Numerator in the low 16 bits, denominator in the high 16 bits (0 = 1).
    pub framerate: u32,
    pub framerate_flags: u32,
    pub va_reserved: [u32; 4],
}

/// Zero-initialized C struct (all fields above are integers, arrays of them or
/// raw pointers, for which all-zero is valid).
pub fn zeroed<T>() -> T {
    // SAFETY: only used for the plain C structs in this module.
    unsafe { std::mem::zeroed() }
}

macro_rules! api {
    ($($name:ident : fn($($arg:ty),*) $(-> $ret:ty)?;)*) => {
        /// Function pointers resolved from libva.so.2 and libva-drm.so.2.
        pub struct Api { $(pub $name: unsafe extern "C" fn($($arg),*) $(-> $ret)?,)* }
        impl Api {
            /// # Safety
            /// The symbols must have the declared C signatures.
            pub unsafe fn load(va: &libloading::Library, drm: &libloading::Library) -> Result<Self, libloading::Error> {
                Ok(Self { $($name: {
                    let sym = stringify!($name);
                    let lib = if sym == "vaGetDisplayDRM" { drm } else { va };
                    *lib.get(sym.as_bytes())?
                },)* })
            }
        }
    };
}

api! {
    vaGetDisplayDRM: fn(c_int) -> VADisplay;
    vaInitialize: fn(VADisplay, *mut c_int, *mut c_int) -> VAStatus;
    vaTerminate: fn(VADisplay) -> VAStatus;
    vaErrorStr: fn(VAStatus) -> *const c_char;
    vaQueryVendorString: fn(VADisplay) -> *const c_char;
    vaMaxNumEntrypoints: fn(VADisplay) -> c_int;
    vaQueryConfigEntrypoints: fn(VADisplay, VAProfile, *mut VAEntrypoint, *mut c_int) -> VAStatus;
    vaGetConfigAttributes: fn(VADisplay, VAProfile, VAEntrypoint, *mut VAConfigAttrib, c_int) -> VAStatus;
    vaCreateConfig: fn(VADisplay, VAProfile, VAEntrypoint, *mut VAConfigAttrib, c_int, *mut VAConfigID) -> VAStatus;
    vaDestroyConfig: fn(VADisplay, VAConfigID) -> VAStatus;
    vaCreateSurfaces: fn(VADisplay, c_uint, c_uint, c_uint, *mut VASurfaceID, c_uint, *mut c_void, c_uint) -> VAStatus;
    vaDestroySurfaces: fn(VADisplay, *mut VASurfaceID, c_int) -> VAStatus;
    vaCreateContext: fn(VADisplay, VAConfigID, c_int, c_int, c_int, *mut VASurfaceID, c_int, *mut VAContextID) -> VAStatus;
    vaDestroyContext: fn(VADisplay, VAContextID) -> VAStatus;
    vaCreateBuffer: fn(VADisplay, VAContextID, VABufferType, c_uint, c_uint, *mut c_void, *mut VABufferID) -> VAStatus;
    vaDestroyBuffer: fn(VADisplay, VABufferID) -> VAStatus;
    vaMapBuffer: fn(VADisplay, VABufferID, *mut *mut c_void) -> VAStatus;
    vaUnmapBuffer: fn(VADisplay, VABufferID) -> VAStatus;
    vaBeginPicture: fn(VADisplay, VAContextID, VASurfaceID) -> VAStatus;
    vaRenderPicture: fn(VADisplay, VAContextID, *mut VABufferID, c_int) -> VAStatus;
    vaEndPicture: fn(VADisplay, VAContextID) -> VAStatus;
    vaSyncSurface: fn(VADisplay, VASurfaceID) -> VAStatus;
    vaDeriveImage: fn(VADisplay, VASurfaceID, *mut VAImage) -> VAStatus;
    vaCreateImage: fn(VADisplay, *mut VAImageFormat, c_int, c_int, *mut VAImage) -> VAStatus;
    vaDestroyImage: fn(VADisplay, VAImageID) -> VAStatus;
    vaPutImage: fn(VADisplay, VASurfaceID, VAImageID, c_int, c_int, c_uint, c_uint, c_int, c_int, c_uint, c_uint) -> VAStatus;
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::mem::{offset_of, size_of};

    /// Compares the layouts with the installed libva headers (libva-devel /
    /// libva-dev), compiling a probe with cc. Skipped when they are missing.
    #[test]
    fn layout_matches_system_headers() {
        if !std::path::Path::new("/usr/include/va/va_enc_h264.h").exists() {
            eprintln!("libva headers not installed; skipping");
            return;
        }
        macro_rules! probe {
            ($( $t:ident $( . $f:ident )? ),* $(,)?) => {{
                let mut c = String::from("#include <stdio.h>\n#include <stddef.h>\n#include <va/va.h>\n#include <va/va_enc_h264.h>\nint main(void){\n");
                let mut want = Vec::new();
                $(
                    probe!(@one c, want, $t $( . $f )?);
                )*
                c.push_str("return 0;}\n");
                (c, want)
            }};
            (@one $c:ident, $w:ident, $t:ident) => {
                $c.push_str(&format!("printf(\"%zu\\n\", sizeof({}));\n", stringify!($t)));
                $w.push((stringify!($t).to_string(), size_of::<$t>()));
            };
            (@one $c:ident, $w:ident, $t:ident . $f:ident) => {
                $c.push_str(&format!("printf(\"%zu\\n\", offsetof({}, {}));\n", stringify!($t), stringify!($f)));
                $w.push((format!("{}.{}", stringify!($t), stringify!($f)), offset_of!($t, $f)));
            };
        }
        let (src, want) = probe!(
            VAConfigAttrib, VAImageFormat, VAImage, VAImage.buf, VAImage.pitches, VAImage.offsets,
            VACodedBufferSegment, VACodedBufferSegment.buf, VACodedBufferSegment.next, VAPictureH264,
            VAEncSequenceParameterBufferH264, VAEncSequenceParameterBufferH264.seq_fields,
            VAEncSequenceParameterBufferH264.frame_cropping_flag, VAEncSequenceParameterBufferH264.vui_fields,
            VAEncSequenceParameterBufferH264.time_scale,
            VAEncPictureParameterBufferH264, VAEncPictureParameterBufferH264.coded_buf,
            VAEncPictureParameterBufferH264.frame_num, VAEncPictureParameterBufferH264.pic_fields,
            VAEncSliceParameterBufferH264, VAEncSliceParameterBufferH264.RefPicList0,
            VAEncSliceParameterBufferH264.RefPicList1, VAEncSliceParameterBufferH264.cabac_init_idc,
            VAEncMiscParameterRateControl, VAEncMiscParameterRateControl.rc_flags,
            VAEncMiscParameterHRD, VAEncMiscParameterFrameRate,
        );
        let dir = std::env::temp_dir().join(format!("booth-core-va-probe-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let (c, exe) = (dir.join("probe.c"), dir.join("probe"));
        std::fs::write(&c, src).unwrap();
        let built = std::process::Command::new("cc").arg(&c).arg("-o").arg(&exe).status();
        if !built.is_ok_and(|s| s.success()) {
            let _ = std::fs::remove_dir_all(&dir);
            eprintln!("no C compiler; skipping");
            return;
        }
        let out = std::process::Command::new(&exe).output().unwrap();
        let _ = std::fs::remove_dir_all(&dir);
        let got: Vec<usize> = String::from_utf8(out.stdout).unwrap().lines().map(|l| l.parse().unwrap()).collect();
        assert_eq!(got.len(), want.len());
        for ((name, rust), c) in want.iter().zip(got) {
            assert_eq!(*rust, c, "{name}: Rust {rust}, C {c}");
        }
    }

    /// Sizes and offsets as gcc reports them for the libva 2.22 headers.
    #[test]
    fn layout_matches_c() {
        assert_eq!(size_of::<VAConfigAttrib>(), 8);
        assert_eq!(size_of::<VAImageFormat>(), 48);
        assert_eq!(size_of::<VAImage>(), 120);
        assert_eq!(offset_of!(VAImage, format), 4);
        assert_eq!(offset_of!(VAImage, buf), 52);
        assert_eq!(offset_of!(VAImage, width), 56);
        assert_eq!(offset_of!(VAImage, data_size), 60);
        assert_eq!(offset_of!(VAImage, pitches), 68);
        assert_eq!(offset_of!(VAImage, offsets), 80);
        assert_eq!(size_of::<VACodedBufferSegment>(), 48);
        assert_eq!(offset_of!(VACodedBufferSegment, buf), 16);
        assert_eq!(offset_of!(VACodedBufferSegment, next), 24);
        assert_eq!(size_of::<VAPictureH264>(), 36);

        type Seq = VAEncSequenceParameterBufferH264;
        assert_eq!(size_of::<Seq>(), 1132);
        assert_eq!(offset_of!(Seq, intra_period), 4);
        assert_eq!(offset_of!(Seq, picture_width_in_mbs), 24);
        assert_eq!(offset_of!(Seq, seq_fields), 28);
        assert_eq!(offset_of!(Seq, bit_depth_luma_minus8), 32);
        assert_eq!(offset_of!(Seq, offset_for_non_ref_pic), 36);
        assert_eq!(offset_of!(Seq, frame_cropping_flag), 1068);
        assert_eq!(offset_of!(Seq, frame_crop_left_offset), 1072);
        assert_eq!(offset_of!(Seq, vui_parameters_present_flag), 1088);
        assert_eq!(offset_of!(Seq, vui_fields), 1092);
        assert_eq!(offset_of!(Seq, aspect_ratio_idc), 1096);
        assert_eq!(offset_of!(Seq, sar_width), 1100);
        assert_eq!(offset_of!(Seq, time_scale), 1112);

        type Pic = VAEncPictureParameterBufferH264;
        assert_eq!(size_of::<Pic>(), 648);
        assert_eq!(offset_of!(Pic, ReferenceFrames), 36);
        assert_eq!(offset_of!(Pic, coded_buf), 612);
        assert_eq!(offset_of!(Pic, pic_parameter_set_id), 616);
        assert_eq!(offset_of!(Pic, frame_num), 620);
        assert_eq!(offset_of!(Pic, pic_init_qp), 622);
        assert_eq!(offset_of!(Pic, chroma_qp_index_offset), 625);
        assert_eq!(offset_of!(Pic, pic_fields), 628);

        type Slice = VAEncSliceParameterBufferH264;
        assert_eq!(size_of::<Slice>(), 3140);
        assert_eq!(offset_of!(Slice, slice_type), 12);
        assert_eq!(offset_of!(Slice, idr_pic_id), 14);
        assert_eq!(offset_of!(Slice, pic_order_cnt_lsb), 16);
        assert_eq!(offset_of!(Slice, delta_pic_order_cnt_bottom), 20);
        assert_eq!(offset_of!(Slice, direct_spatial_mv_pred_flag), 32);
        assert_eq!(offset_of!(Slice, RefPicList0), 36);
        assert_eq!(offset_of!(Slice, RefPicList1), 1188);
        assert_eq!(offset_of!(Slice, luma_log2_weight_denom), 2340);
        assert_eq!(offset_of!(Slice, luma_weight_l0), 2344);
        assert_eq!(offset_of!(Slice, chroma_weight_l0_flag), 2472);
        assert_eq!(offset_of!(Slice, chroma_weight_l0), 2474);
        assert_eq!(offset_of!(Slice, luma_weight_l1_flag), 2730);
        assert_eq!(offset_of!(Slice, chroma_weight_l1_flag), 2860);
        assert_eq!(offset_of!(Slice, cabac_init_idc), 3118);
        assert_eq!(offset_of!(Slice, va_reserved), 3124);

        assert_eq!(size_of::<VAEncMiscParameterRateControl>(), 60);
        assert_eq!(offset_of!(VAEncMiscParameterRateControl, rc_flags), 24);
        assert_eq!(offset_of!(VAEncMiscParameterRateControl, max_qp), 32);
        assert_eq!(size_of::<VAEncMiscParameterHRD>(), 24);
        assert_eq!(size_of::<VAEncMiscParameterFrameRate>(), 24);
        // The misc header is a single u32 followed directly by the data.
        assert_eq!(offset_of!(MiscParam<VAEncMiscParameterHRD>, data), 4);
    }
}

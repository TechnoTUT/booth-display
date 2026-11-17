//! Raw C ABI of libx264 (subset), transcribed from `x264.h` (X264_BUILD 164).
//! libx264 is loaded at run time, so building needs no headers and no library.
//!
//! Only the fields booth-core touches are declared. `x264_param_t` grows from
//! build to build, so it is declared as its stable leading fields followed by
//! generous padding, and every other setting goes through `x264_param_parse`
//! by name. `x264_picture_t` gets the same treatment past `img`. The layout
//! tests below pin the declared fields to what a C compiler reports for the
//! installed header, and check that the padding covers the real struct.

#![allow(non_camel_case_types, dead_code)]

use std::os::raw::{c_char, c_int, c_void};

/// `X264_CSP_NV12`: one Y plane and one interleaved U/V plane.
pub const X264_CSP_NV12: c_int = 0x0004;
/// `X264_TYPE_AUTO`: let x264 choose the frame type.
pub const X264_TYPE_AUTO: c_int = 0;
/// `X264_TYPE_IDR`: force an IDR.
pub const X264_TYPE_IDR: c_int = 0x0001;
/// `X264_LOG_ERROR`.
pub const X264_LOG_ERROR: c_int = 0;

pub type x264_t = c_void;

/// Room for the whole `x264_param_t`. Build 164 needs about 1 KB.
const PARAM_SPACE: usize = 8192;
/// Room for the rest of `x264_picture_t` after `img`. Build 164 needs 160 bytes.
const PICTURE_SPACE: usize = 1024;

/// The leading fields of `x264_param_t`, unchanged since long before build 148.
#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct x264_param_head {
    pub cpu: u32,
    pub i_threads: c_int,
    pub i_lookahead_threads: c_int,
    pub b_sliced_threads: c_int,
    pub b_deterministic: c_int,
    pub b_cpu_independent: c_int,
    pub i_sync_lookahead: c_int,
    pub i_width: c_int,
    pub i_height: c_int,
    pub i_csp: c_int,
}

#[repr(C, align(16))]
pub struct x264_param_t {
    pub head: x264_param_head,
    _rest: [u8; PARAM_SPACE],
}

impl x264_param_t {
    pub fn zeroed() -> Box<Self> {
        Box::new(Self { head: x264_param_head::default(), _rest: [0; PARAM_SPACE] })
    }
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct x264_image_t {
    pub i_csp: c_int,
    pub i_plane: c_int,
    pub i_stride: [c_int; 4],
    pub plane: [*mut u8; 4],
}

#[repr(C, align(16))]
pub struct x264_picture_t {
    pub i_type: c_int,
    pub i_qpplus1: c_int,
    pub i_pic_struct: c_int,
    pub b_keyframe: c_int,
    pub i_pts: i64,
    pub i_dts: i64,
    pub param: *mut x264_param_t,
    pub img: x264_image_t,
    _rest: [u8; PICTURE_SPACE],
}

impl x264_picture_t {
    pub fn zeroed() -> Box<Self> {
        // SAFETY: all-zero is a valid value for every field (null pointers, 0).
        Box::new(unsafe { std::mem::zeroed() })
    }
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct x264_nal_t {
    pub i_ref_idc: c_int,
    pub i_type: c_int,
    pub b_long_startcode: c_int,
    pub i_first_mb: c_int,
    pub i_last_mb: c_int,
    pub i_payload: c_int,
    pub p_payload: *mut u8,
    pub i_padding: c_int,
}

/// The libx264 entry points. `x264_encoder_open` carries the build number in
/// its symbol name (`x264_encoder_open_164`), so it is looked up separately.
pub struct Api {
    pub build: u32,
    pub param_default_preset: unsafe extern "C" fn(*mut x264_param_t, *const c_char, *const c_char) -> c_int,
    pub param_parse: unsafe extern "C" fn(*mut x264_param_t, *const c_char, *const c_char) -> c_int,
    pub param_apply_profile: unsafe extern "C" fn(*mut x264_param_t, *const c_char) -> c_int,
    /// Frees strings x264_param_parse allocated; only in newer builds.
    pub param_cleanup: Option<unsafe extern "C" fn(*mut x264_param_t)>,
    pub picture_init: unsafe extern "C" fn(*mut x264_picture_t),
    pub encoder_open: unsafe extern "C" fn(*mut x264_param_t) -> *mut x264_t,
    pub encoder_encode:
        unsafe extern "C" fn(*mut x264_t, *mut *mut x264_nal_t, *mut c_int, *mut x264_picture_t, *mut x264_picture_t) -> c_int,
    pub encoder_close: unsafe extern "C" fn(*mut x264_t),
}

impl Api {
    /// # Safety
    /// `lib` must be libx264 of build `build`, so the symbols have the
    /// declared C signatures.
    pub unsafe fn load(lib: &libloading::Library, build: u32) -> Result<Self, libloading::Error> {
        let open = format!("x264_encoder_open_{build}");
        // SAFETY: per the function contract.
        unsafe {
            Ok(Self {
                build,
                param_default_preset: *lib.get(b"x264_param_default_preset")?,
                param_parse: *lib.get(b"x264_param_parse")?,
                param_apply_profile: *lib.get(b"x264_param_apply_profile")?,
                param_cleanup: lib.get(b"x264_param_cleanup").ok().map(|s| *s),
                picture_init: *lib.get(b"x264_picture_init")?,
                encoder_open: *lib.get(open.as_bytes())?,
                encoder_encode: *lib.get(b"x264_encoder_encode")?,
                encoder_close: *lib.get(b"x264_encoder_close")?,
            })
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::mem::{offset_of, size_of};

    /// Offsets as gcc reports them for the build 164 header on x86-64.
    #[test]
    fn layout_matches_c() {
        assert_eq!(offset_of!(x264_param_head, i_width), 28);
        assert_eq!(offset_of!(x264_param_head, i_csp), 36);
        assert_eq!(offset_of!(x264_picture_t, b_keyframe), 12);
        assert_eq!(offset_of!(x264_picture_t, i_pts), 16);
        assert_eq!(offset_of!(x264_picture_t, img), 40);
        assert_eq!(size_of::<x264_image_t>(), 56);
        assert_eq!(offset_of!(x264_nal_t, p_payload), 24);
        assert_eq!(size_of::<x264_nal_t>(), 40);
    }

    /// Compares the layouts with the installed x264.h (libx264-dev /
    /// x264-devel), compiling a probe with cc. Skipped when it is missing.
    #[test]
    fn layout_matches_system_header() {
        if !std::path::Path::new("/usr/include/x264.h").exists() {
            eprintln!("x264.h not installed; skipping");
            return;
        }
        // (C expression, Rust value, whether C may be smaller than Rust)
        let checks: Vec<(&str, usize, bool)> = vec![
            ("offsetof(x264_param_t, cpu)", offset_of!(x264_param_head, cpu), false),
            ("offsetof(x264_param_t, i_threads)", offset_of!(x264_param_head, i_threads), false),
            ("offsetof(x264_param_t, b_sliced_threads)", offset_of!(x264_param_head, b_sliced_threads), false),
            ("offsetof(x264_param_t, i_width)", offset_of!(x264_param_head, i_width), false),
            ("offsetof(x264_param_t, i_height)", offset_of!(x264_param_head, i_height), false),
            ("offsetof(x264_param_t, i_csp)", offset_of!(x264_param_head, i_csp), false),
            ("sizeof(x264_param_t)", size_of::<x264_param_t>(), true),
            ("offsetof(x264_picture_t, i_type)", offset_of!(x264_picture_t, i_type), false),
            ("offsetof(x264_picture_t, b_keyframe)", offset_of!(x264_picture_t, b_keyframe), false),
            ("offsetof(x264_picture_t, i_pts)", offset_of!(x264_picture_t, i_pts), false),
            ("offsetof(x264_picture_t, param)", offset_of!(x264_picture_t, param), false),
            ("offsetof(x264_picture_t, img)", offset_of!(x264_picture_t, img), false),
            ("sizeof(x264_picture_t)", size_of::<x264_picture_t>(), true),
            ("sizeof(x264_image_t)", size_of::<x264_image_t>(), false),
            ("offsetof(x264_image_t, i_stride)", offset_of!(x264_image_t, i_stride), false),
            ("offsetof(x264_image_t, plane)", offset_of!(x264_image_t, plane), false),
            ("sizeof(x264_nal_t)", size_of::<x264_nal_t>(), false),
            ("offsetof(x264_nal_t, i_payload)", offset_of!(x264_nal_t, i_payload), false),
            ("offsetof(x264_nal_t, p_payload)", offset_of!(x264_nal_t, p_payload), false),
            ("X264_CSP_NV12", X264_CSP_NV12 as usize, false),
            ("X264_TYPE_AUTO", X264_TYPE_AUTO as usize, false),
            ("X264_TYPE_IDR", X264_TYPE_IDR as usize, false),
            ("X264_LOG_ERROR", X264_LOG_ERROR as usize, false),
        ];
        let mut src = String::from("#include <stdio.h>\n#include <stddef.h>\n#include <stdint.h>\n#include <x264.h>\nint main(void){\n");
        for (expr, _, _) in &checks {
            src.push_str(&format!("printf(\"%zu\\n\", (size_t)({expr}));\n"));
        }
        src.push_str("return 0;}\n");

        let dir = std::env::temp_dir().join(format!("booth-core-x264-probe-{}", std::process::id()));
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
        assert_eq!(got.len(), checks.len());
        for ((expr, rust, room), c) in checks.iter().zip(got) {
            if *room {
                assert!(c <= *rust, "{expr}: C {c} does not fit in Rust {rust}");
            } else {
                assert_eq!(*rust, c, "{expr}: Rust {rust}, C {c}");
            }
        }
    }
}

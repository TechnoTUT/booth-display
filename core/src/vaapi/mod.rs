//! Direct VA-API (libva) access for hardware H.264 encoding.
//!
//! libva is loaded at run time (`libva.so.2` and `libva-drm.so.2`), like libndi
//! in moip-engine's ndi crate: building needs no libva headers, and a machine
//! without VA-API still runs (the encoder falls back to libx264).

pub mod sys;

use anyhow::{anyhow, bail, Result};
use std::ffi::CStr;
use std::fs::{File, OpenOptions};
use std::os::fd::AsRawFd;
use std::sync::OnceLock;

struct Runtime {
    api: sys::Api,
    _va: libloading::Library,
    _drm: libloading::Library,
}

static RUNTIME: OnceLock<std::result::Result<Runtime, String>> = OnceLock::new();

fn load() -> std::result::Result<Runtime, String> {
    // SAFETY: loading libva runs its (trusted) initialisers.
    let va = unsafe { libloading::Library::new("libva.so.2") }.map_err(|e| format!("libva.so.2: {e}"))?;
    let drm = unsafe { libloading::Library::new("libva-drm.so.2") }.map_err(|e| format!("libva-drm.so.2: {e}"))?;
    // SAFETY: the symbols are declared with libva's C signatures.
    let api = unsafe { sys::Api::load(&va, &drm) }.map_err(|e| format!("missing libva symbol: {e}"))?;
    Ok(Runtime { api, _va: va, _drm: drm })
}

pub(crate) fn api() -> Result<&'static sys::Api> {
    match RUNTIME.get_or_init(load) {
        Ok(rt) => Ok(&rt.api),
        Err(e) => Err(anyhow!("VA-API runtime not available: {e}")),
    }
}

/// An initialized VA display on a DRM render node. Each encoder opens its own,
/// so encoders on different threads never share libva state.
pub struct Display {
    pub(crate) api: &'static sys::Api,
    pub(crate) dpy: sys::VADisplay,
    pub vendor: String,
    _fd: File,
}

// SAFETY: a VADisplay may be used from any thread; each Display is used by one
// thread at a time (it is not Sync).
unsafe impl Send for Display {}

impl Display {
    pub fn open(device: &str) -> Result<Self> {
        let api = api()?;
        let fd = OpenOptions::new().read(true).write(true).open(device).map_err(|e| anyhow!("{device}: {e}"))?;
        // SAFETY: valid DRM fd, kept open for the display's lifetime.
        let dpy = unsafe { (api.vaGetDisplayDRM)(fd.as_raw_fd()) };
        if dpy.is_null() {
            bail!("vaGetDisplayDRM failed for {device}");
        }
        let (mut major, mut minor) = (0, 0);
        // SAFETY: valid display and out-pointers.
        let st = unsafe { (api.vaInitialize)(dpy, &mut major, &mut minor) };
        if st != sys::VA_STATUS_SUCCESS {
            let msg = status_str(api, st);
            // SAFETY: terminate the half-initialized display.
            unsafe { (api.vaTerminate)(dpy) };
            bail!("vaInitialize on {device}: {msg}");
        }
        // SAFETY: initialized display; the string is static in the driver.
        let vendor = unsafe { CStr::from_ptr((api.vaQueryVendorString)(dpy)) }.to_string_lossy().into_owned();
        Ok(Self { api, dpy, vendor, _fd: fd })
    }

    /// Turns a libva status into a Result.
    pub(crate) fn check(&self, st: sys::VAStatus, what: &str) -> Result<()> {
        if st == sys::VA_STATUS_SUCCESS {
            Ok(())
        } else {
            Err(anyhow!("{what}: {}", status_str(self.api, st)))
        }
    }
}

impl Drop for Display {
    fn drop(&mut self) {
        // SAFETY: initialized display, terminated once.
        unsafe { (self.api.vaTerminate)(self.dpy) };
    }
}

fn status_str(api: &sys::Api, st: sys::VAStatus) -> String {
    // SAFETY: vaErrorStr returns a static string for any status.
    let s = unsafe { CStr::from_ptr((api.vaErrorStr)(st)) };
    format!("{} ({st})", s.to_string_lossy())
}

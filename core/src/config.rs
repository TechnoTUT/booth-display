//! Configuration sent by the controller with the `configure` command. The field
//! names match `config.yaml` and the controller's JSON API.

use serde::{Deserialize, Serialize};

/// Full canvas width (3 displays x 1920 px), used when the configured width is
/// missing or invalid.
pub const DEFAULT_CANVAS_WIDTH: usize = 5760;
pub const DEFAULT_CANVAS_HEIGHT: usize = 540;
pub const DEFAULT_FPS: u32 = 30;

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(default)]
pub struct CanvasConfig {
    pub width: i64,
    pub height: i64,
    pub fps: i64,
}

/// Canvas geometry with defaults applied.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Canvas {
    pub width: usize,
    pub height: usize,
    pub fps: u32,
}

impl Canvas {
    pub fn frame_size(&self) -> usize {
        self.width * self.height * 4
    }
}

impl CanvasConfig {
    pub fn resolve(&self) -> Canvas {
        Canvas {
            width: if self.width > 0 { self.width as usize } else { DEFAULT_CANVAS_WIDTH },
            height: if self.height > 0 { self.height as usize } else { DEFAULT_CANVAS_HEIGHT },
            fps: if self.fps > 0 { self.fps as u32 } else { DEFAULT_FPS },
        }
    }
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(default)]
pub struct DisplayConfig {
    pub id: String,
    pub name: String,
    pub ip: String,
    pub port: u16,
    pub width: i64,
    pub height: i64,
    pub crop_x: i64,
    pub crop_y: i64,
    pub bezel_padding_right: i64,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(default)]
pub struct PipelineConfig {
    /// "auto", "vaapi" (in-process), "vaapi-ffmpeg" or "software".
    pub encoder: String,
    pub vaapi_device: String,
    /// Pacing of each display's packets in Mbps: 0 for the default, negative
    /// for no pacing.
    pub send_rate_mbps: i64,
}

/// Default pacing: a little under a 100BASE-TX display link.
pub const DEFAULT_SEND_RATE_MBPS: i64 = 80;

pub const DEFAULT_VAAPI_DEVICE: &str = "/dev/dri/renderD128";

/// Which encoder to run.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum EncoderMode {
    /// VA-API driven directly from booth-core, on this render node.
    Vaapi(String),
    /// VA-API through ffmpeg's h264_vaapi (the previous encoder; for comparison).
    FfmpegVaapi(String),
    /// libx264 through ffmpeg.
    Software,
}

impl PipelineConfig {
    /// The pacing rate for each display, None when pacing is off.
    pub fn send_rate(&self) -> Option<f64> {
        match self.send_rate_mbps {
            0 => Some(DEFAULT_SEND_RATE_MBPS as f64),
            r if r < 0 => None,
            r => Some(r as f64),
        }
    }

    /// The encoder to try first. "auto" uses VA-API when the render node exists.
    pub fn mode(&self) -> EncoderMode {
        let device = if self.vaapi_device.is_empty() {
            DEFAULT_VAAPI_DEVICE.to_string()
        } else {
            self.vaapi_device.clone()
        };
        match self.encoder.to_ascii_lowercase().as_str() {
            "software" | "x264" | "libx264" | "cpu" => EncoderMode::Software,
            "vaapi-ffmpeg" | "ffmpeg-vaapi" => EncoderMode::FfmpegVaapi(device),
            "vaapi" | "hw" | "gpu" => EncoderMode::Vaapi(device),
            _ if std::path::Path::new(&device).exists() => EncoderMode::Vaapi(device),
            _ => EncoderMode::Software,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn encoder_modes() {
        let m = |e: &str| PipelineConfig { encoder: e.into(), vaapi_device: "/dev/null".into(), ..Default::default() }.mode();
        assert_eq!(m("software"), EncoderMode::Software);
        assert_eq!(m("vaapi"), EncoderMode::Vaapi("/dev/null".into()));
        assert_eq!(m("vaapi-ffmpeg"), EncoderMode::FfmpegVaapi("/dev/null".into()));
        assert_eq!(m("auto"), EncoderMode::Vaapi("/dev/null".into()));
        let missing = PipelineConfig { encoder: "auto".into(), vaapi_device: "/nonexistent".into(), ..Default::default() };
        assert_eq!(missing.mode(), EncoderMode::Software);
    }

    #[test]
    fn send_rate() {
        let r = |send_rate_mbps| PipelineConfig { send_rate_mbps, ..Default::default() }.send_rate();
        assert_eq!(r(0), Some(DEFAULT_SEND_RATE_MBPS as f64));
        assert_eq!(r(50), Some(50.0));
        assert_eq!(r(-1), None);
    }
}

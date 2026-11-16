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
    /// "auto", "vaapi" or "software".
    pub encoder: String,
    pub vaapi_device: String,
}

pub const DEFAULT_VAAPI_DEVICE: &str = "/dev/dri/renderD128";

impl PipelineConfig {
    /// Whether to try VA-API, and on which render node.
    pub fn vaapi_device(&self) -> Option<String> {
        let device = if self.vaapi_device.is_empty() {
            DEFAULT_VAAPI_DEVICE.to_string()
        } else {
            self.vaapi_device.clone()
        };
        match self.encoder.to_ascii_lowercase().as_str() {
            "software" | "x264" | "libx264" | "cpu" => None,
            "vaapi" | "hw" | "gpu" => Some(device),
            _ => std::path::Path::new(&device).exists().then_some(device),
        }
    }
}

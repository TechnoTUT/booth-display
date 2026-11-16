//! The pipeline as the controller drives it: configuration, start/stop, scene
//! switching and status. One `Engine` lives for the whole process.

use crate::config::{CanvasConfig, DisplayConfig, PipelineConfig};
use crate::encoder::Encoder;
use crate::mixer::{Mixer, SceneStatus, Transition};
use crate::preview::Preview;
use crate::sender::{DisplayMetrics, DisplaySender};
use serde::Serialize;
use std::sync::Arc;

#[derive(Debug, Clone, Serialize)]
pub struct PipelineStatus {
    pub is_running: bool,
    pub active_mode: String,
    pub video_file: String,
    /// Set when the encoder or the NDI input stopped unexpectedly while running.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct Status {
    pub pipeline: PipelineStatus,
    pub scene: SceneStatus,
    pub metrics: Vec<DisplayMetrics>,
}

#[derive(Default)]
pub struct Engine {
    canvas: CanvasConfig,
    displays: Vec<DisplayConfig>,
    pipeline: PipelineConfig,
    senders: Vec<Arc<DisplaySender>>,

    // Dropped in this order on stop: the encoder and the preview end once the
    // mixer closes their frame streams.
    encoder: Encoder,
    mixer: Option<Mixer>,
    preview: Option<Preview>,

    running: bool,
    active_mode: String,
    video_file: String,
    ndi_source: String,
}

impl Engine {
    pub fn new() -> Self {
        let mut e = Self::default();
        e.active_mode = "testpattern".into();
        e
    }

    /// Applies a new configuration. Senders are rebuilt for the new display
    /// list; a running pipeline restarts its encoder, or everything when the
    /// canvas changed.
    pub fn configure(
        &mut self,
        canvas: CanvasConfig,
        displays: Vec<DisplayConfig>,
        pipeline: PipelineConfig,
    ) -> anyhow::Result<()> {
        let mut senders = Vec::with_capacity(displays.len());
        for d in &displays {
            let s = DisplaySender::new(d).map_err(|e| anyhow::anyhow!("display {}: {e}", d.id))?;
            senders.push(s);
        }
        let canvas_changed = canvas.resolve() != self.canvas.resolve();
        self.encoder.stop();
        self.canvas = canvas;
        self.displays = displays;
        self.pipeline = pipeline;
        self.senders = senders;

        if !self.running {
            return Ok(());
        }
        if canvas_changed {
            let (mode, target) = (self.active_mode.clone(), self.current_target());
            return self.start(&mode, &target);
        }
        let mixer = self.mixer.as_ref().expect("running pipeline has a mixer");
        mixer.set_displays(&self.displays);
        self.encoder.start(self.canvas.resolve(), &self.displays, &self.senders, &self.pipeline, &|| mixer.subscribe())
    }

    fn current_target(&self) -> String {
        match self.active_mode.as_str() {
            "ndi" => self.ndi_source.clone(),
            "video" => self.video_file.clone(),
            _ => String::new(),
        }
    }

    /// Starts streaming `mode` to every display. A failed start leaves
    /// nothing running.
    pub fn start(&mut self, mode: &str, target: &str) -> anyhow::Result<()> {
        self.stop();
        let mode = match mode {
            "" if self.active_mode.is_empty() => "testpattern".to_string(),
            "" => self.active_mode.clone(),
            m => m.to_string(),
        };
        let mut target = target.to_string();
        if mode == "ndi" {
            if target.is_empty() {
                target = self.ndi_source.clone();
            }
            anyhow::ensure!(!target.is_empty(), "no NDI source selected");
            self.ndi_source = target.clone();
        } else if mode == "video" {
            if target.is_empty() {
                target = self.video_file.clone();
            }
            self.video_file = target.clone();
        }

        let canvas = self.canvas.resolve();
        let mixer = Mixer::start(canvas, &self.displays)?;
        // The source must start, otherwise play would report success with no picture.
        mixer.switch_source(&mode, &target, Transition::Cut, 0)?;
        self.encoder
            .start(canvas, &self.displays, &self.senders, &self.pipeline, &|| mixer.subscribe())
            .map_err(|e| anyhow::anyhow!("failed to start encoder: {e}"))?;
        // A preview failure must not stop the stream.
        match Preview::start(canvas, mixer.subscribe(), crate::output::jpeg) {
            Ok(p) => self.preview = Some(p),
            Err(e) => eprintln!("[engine] preview unavailable: {e}"),
        }
        self.mixer = Some(mixer);
        self.running = true;
        self.active_mode = mode;
        Ok(())
    }

    /// Changes the source with a transition, without restarting the encoder.
    pub fn switch(&mut self, source_type: &str, target: &str, trans: Transition, duration_ms: u64) -> anyhow::Result<()> {
        let Some(mixer) = self.mixer.as_ref().filter(|_| self.running) else {
            return self.start(source_type, target);
        };
        mixer.switch_source(source_type, target, trans, duration_ms)?;
        match source_type {
            "ndi" => self.ndi_source = target.to_string(),
            "video" => self.video_file = target.to_string(),
            _ => {}
        }
        self.active_mode = source_type.to_string();
        Ok(())
    }

    pub fn stop(&mut self) {
        self.encoder.stop();
        self.mixer = None; // closes the preview's frame stream
        self.preview = None;
        self.running = false;
    }

    pub fn status(&self) -> Status {
        let error = self.encoder.err().or_else(|| self.mixer.as_ref().and_then(|m| m.source_err()));
        let scene = match &self.mixer {
            Some(m) => m.status(),
            None => SceneStatus {
                active_source: self.active_mode.clone(),
                active_target: String::new(),
                next_source: String::new(),
                next_target: String::new(),
                transition: Transition::Cut,
                duration_ms: 500,
                in_transition: false,
                progress: 0.0,
            },
        };
        let mut metrics: Vec<_> = self.senders.iter().map(|s| s.metrics()).collect();
        metrics.sort_by(|a, b| a.id.cmp(&b.id));
        Status {
            pipeline: PipelineStatus {
                is_running: self.running,
                active_mode: self.active_mode.clone(),
                video_file: self.video_file.clone(),
                error: if self.running { error } else { None },
            },
            scene,
            metrics,
        }
    }
}

impl Drop for Engine {
    fn drop(&mut self) {
        self.stop();
    }
}

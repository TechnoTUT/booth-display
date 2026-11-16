//! Built-in BGRX test pattern: SMPTE-style color bars, a grayscale gradient,
//! dashed display boundary lines and a scanner bar that sweeps the whole canvas
//! every 3 seconds, to check synchronization and latency across displays.

use crate::config::{Canvas, DisplayConfig};

pub struct TestPattern {
    width: usize,
    height: usize,
    fps: u32,
    base: Vec<u8>,
    frame_idx: u64,
}

impl TestPattern {
    pub fn new(canvas: Canvas, displays: &[DisplayConfig]) -> Self {
        let (w, h) = (canvas.width, canvas.height);
        let mut base = vec![0u8; w * h * 4];
        // BGR0
        let colors: [[u8; 3]; 8] = [
            [220, 220, 220], // light grey
            [0, 220, 220],   // yellow
            [220, 220, 0],   // cyan
            [0, 220, 0],     // green
            [220, 0, 220],   // magenta
            [0, 0, 220],     // red
            [220, 0, 0],     // blue
            [20, 20, 20],    // near black
        ];
        let bar_h = (h as f64 * 0.72) as usize;
        let bar_w = (w / colors.len()).max(1);
        for (y, row) in base.chunks_exact_mut(w * 4).enumerate() {
            for (x, px) in row.chunks_exact_mut(4).enumerate() {
                let c = if y < bar_h {
                    colors[(x / bar_w).min(colors.len() - 1)]
                } else {
                    let v = (x * 255 / w) as u8; // horizontal grayscale gradient
                    [v, v, v]
                };
                px.copy_from_slice(&[c[0], c[1], c[2], 0]);
            }
        }
        let mut line = |x: i64| {
            if x < 0 || x as usize >= w {
                return;
            }
            for y in (0..h).filter(|y| (y / 8) % 2 == 0) {
                let i = (y * w + x as usize) * 4;
                base[i..i + 4].copy_from_slice(&[255, 255, 255, 0]);
            }
        };
        for d in displays {
            if d.crop_x + d.width > w as i64 {
                continue;
            }
            line(d.crop_x);
            line(d.crop_x + d.width - 1);
        }
        Self { width: w, height: h, fps: canvas.fps, base, frame_idx: 0 }
    }

    /// Writes the next frame into `frame` (width*height*4 bytes).
    pub fn render(&mut self, frame: &mut [u8]) {
        if frame.len() < self.base.len() {
            return;
        }
        frame[..self.base.len()].copy_from_slice(&self.base);
        let speed = self.width as f64 / (self.fps as f64 * 3.0); // full sweep in 3 s
        let bar_x = (self.frame_idx as f64 * speed) as usize % self.width;
        self.frame_idx += 1;
        const BAR_W: usize = 24;
        for y in 0..self.height {
            for bx in 0..BAR_W {
                let i = (y * self.width + (bar_x + bx) % self.width) * 4;
                frame[i..i + 4].copy_from_slice(&[255, 255, 255, 0]);
            }
        }
    }
}

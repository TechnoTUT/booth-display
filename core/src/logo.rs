//! TechnoTUT logo crawl: the logo, rasterized once from its vector outline,
//! scrolls from right to left across the canvas on a dark navy background.

use crate::config::Canvas;

struct Shape {
    red: bool, // white otherwise
    min_x: f64,
    max_x: f64,
    min_y: f64,
    max_y: f64,
    loops: &'static [&'static [(f64, f64)]],
}

#[rustfmt::skip]
const SHAPES: &[Shape] = &[
    Shape { red: true, min_x: 28.46, max_x: 45.85, min_y: 16.94, max_y: 58.03, loops: &[
        &[(45.84, 26.97), (38.68, 22.83), (38.67, 43.92), (35.64, 45.68), (35.64, 21.08), (28.47, 16.94), (28.46, 58.03), (45.85, 47.99), (45.84, 26.97)],
    ]},
    Shape { red: false, min_x: 45.21, max_x: 64.05, min_y: 26.60, max_y: 48.36, loops: &[
        &[(45.21, 26.60), (45.21, 48.36), (52.35, 44.23), (52.38, 38.99), (56.92, 41.60), (64.05, 37.48), (45.21, 26.60)],
    ]},
    Shape { red: false, min_x: 7.11, max_x: 33.07, min_y: 4.60, max_y: 70.36, loops: &[
        &[(17.34, 10.51), (17.34, 10.51), (7.11, 4.60), (7.11, 70.36), (14.28, 66.21), (14.28, 17.00), (17.34, 18.75), (17.34, 64.44), (24.52, 60.30), (24.52, 22.93), (29.48, 25.81), (33.07, 19.59), (17.34, 10.51)],
    ]},
    Shape { red: false, min_x: 64.60, max_x: 90.14, min_y: 18.55, max_y: 57.00, loops: &[
        &[(73.74, 25.77), (64.60, 25.77), (64.60, 18.55), (90.14, 18.55), (90.14, 25.77), (81.00, 25.77), (81.00, 57.00), (73.74, 57.00)],
    ]},
    Shape { red: false, min_x: 92.23, max_x: 114.23, min_y: 27.70, max_y: 57.00, loops: &[
        &[(99.59, 49.70), (114.23, 49.70), (114.23, 57.00), (92.23, 57.00), (92.23, 27.70), (114.23, 27.70), (114.23, 45.58), (99.59, 45.58)],
        &[(99.59, 35.00), (99.59, 40.00), (106.90, 40.00), (106.90, 35.00)],
    ]},
    Shape { red: false, min_x: 116.31, max_x: 138.31, min_y: 27.70, max_y: 57.00, loops: &[
        &[(131.00, 45.77), (138.31, 45.77), (138.31, 57.00), (116.31, 57.00), (116.31, 27.70), (138.31, 27.70), (138.31, 39.00), (131.00, 39.00), (131.00, 35.00), (123.67, 35.00), (123.67, 49.70), (131.00, 49.70)],
    ]},
    Shape { red: false, min_x: 140.44, max_x: 162.42, min_y: 18.55, max_y: 57.00, loops: &[
        &[(140.44, 18.55), (147.75, 18.55), (147.75, 27.70), (162.42, 27.70), (162.42, 57.00), (155.11, 57.00), (155.11, 35.00), (147.78, 35.00), (147.78, 57.00), (140.45, 57.00)],
    ]},
    Shape { red: false, min_x: 164.53, max_x: 186.53, min_y: 27.70, max_y: 57.00, loops: &[
        &[(171.86, 57.00), (164.53, 57.00), (164.53, 27.70), (186.53, 27.70), (186.53, 57.00), (179.20, 57.00), (179.20, 35.00), (171.87, 35.00)],
    ]},
    Shape { red: false, min_x: 188.59, max_x: 210.59, min_y: 27.70, max_y: 57.00, loops: &[
        &[(210.59, 27.70), (210.59, 57.00), (188.59, 57.00), (188.59, 27.70)],
        &[(196.00, 35.00), (196.00, 49.70), (203.33, 49.70), (203.33, 35.00)],
    ]},
    Shape { red: false, min_x: 212.71, max_x: 238.25, min_y: 18.55, max_y: 57.00, loops: &[
        &[(221.86, 25.77), (212.71, 25.77), (212.71, 18.55), (238.25, 18.55), (238.25, 25.77), (229.10, 25.77), (229.10, 57.00), (221.85, 57.00)],
    ]},
    Shape { red: false, min_x: 240.37, max_x: 265.91, min_y: 18.55, max_y: 57.00, loops: &[
        &[(258.69, 18.55), (265.91, 18.55), (265.91, 57.00), (240.37, 57.00), (240.37, 18.55), (247.62, 18.55), (247.62, 49.78), (258.69, 49.78)],
    ]},
    Shape { red: false, min_x: 268.00, max_x: 293.54, min_y: 18.55, max_y: 57.00, loops: &[
        &[(277.18, 25.77), (268.00, 25.77), (268.00, 18.55), (293.54, 18.55), (293.54, 25.77), (284.39, 25.77), (284.39, 57.00), (277.14, 57.00)],
    ]},
];

const SCALE: f64 = 7.0;
const GAP: i64 = 280;
const SPEED: i64 = 8; // pixels per frame
const BACKGROUND: [u8; 4] = [10, 8, 5, 0]; // dark navy, BGR0

fn point_in_poly(px: f64, py: f64, poly: &[(f64, f64)]) -> bool {
    let mut inside = false;
    for i in 0..poly.len() {
        let (p1, p2) = (poly[i], poly[(i + 1) % poly.len()]);
        if (p1.1 > py) != (p2.1 > py) && px < (p2.0 - p1.0) * (py - p1.1) / (p2.1 - p1.1) + p1.0 {
            inside = !inside;
        }
    }
    inside
}

pub struct Logo {
    width: usize,
    height: usize,
    logo_w: usize,
    logo_h: usize,
    /// BGR plus coverage (0 = transparent), with the glowing edge already applied.
    pixels: Vec<[u8; 4]>,
    background: Vec<u8>,
    frame_idx: i64,
}

impl Logo {
    pub fn new(canvas: Canvas) -> Self {
        let logo_w = (300.41 * SCALE) as usize + 1;
        let logo_h = (74.96 * SCALE) as usize + 1;
        let mut px = vec![[0u8; 4]; logo_w * logo_h];
        for s in SHAPES {
            let color = if s.red { [10, 0, 199, 255] } else { [255, 255, 255, 255] };
            let (x0, x1) = ((s.min_x * SCALE) as usize, ((s.max_x * SCALE) as usize + 1).min(logo_w));
            let (y0, y1) = ((s.min_y * SCALE) as usize, ((s.max_y * SCALE) as usize + 1).min(logo_h));
            for y in y0..y1 {
                let sy = y as f64 / SCALE;
                for x in x0..x1 {
                    let sx = x as f64 / SCALE;
                    if s.loops.iter().filter(|l| point_in_poly(sx, sy, l)).count() % 2 == 1 {
                        px[y * logo_w + x] = color;
                    }
                }
            }
        }
        // Brighten a crisp 1-pixel edge outline.
        let opaque = |x: i64, y: i64| {
            x >= 0 && y >= 0 && (x as usize) < logo_w && (y as usize) < logo_h && px[y as usize * logo_w + x as usize][3] > 0
        };
        let mut pixels = px.clone();
        for y in 0..logo_h as i64 {
            for x in 0..logo_w as i64 {
                let i = y as usize * logo_w + x as usize;
                if px[i][3] == 0 {
                    continue;
                }
                let edge = (-1..=1).any(|dy| (-1..=1).any(|dx| !opaque(x + dx, y + dy)));
                if edge {
                    for c in &mut pixels[i][..3] {
                        *c = (*c as f64 * 1.25 + 35.0).min(255.0) as u8;
                    }
                }
            }
        }
        let background = BACKGROUND.repeat(canvas.width * canvas.height);
        Self { width: canvas.width, height: canvas.height, logo_w, logo_h, pixels, background, frame_idx: 0 }
    }

    /// Writes the next frame into `frame` (width*height*4 bytes).
    pub fn render(&mut self, frame: &mut [u8]) {
        if frame.len() < self.background.len() {
            return;
        }
        frame[..self.background.len()].copy_from_slice(&self.background);
        let top = (self.height as i64 / 2 - self.logo_h as i64 / 2).max(0);
        let pitch = self.logo_w as i64 + GAP;
        let moved = self.frame_idx * SPEED;
        self.frame_idx += 1;
        let offset = -(moved % pitch);
        let mut start = offset - pitch;
        while start < self.width as i64 + pitch {
            if start + (self.logo_w as i64) >= 0 && start < self.width as i64 {
                self.draw(frame, start, top);
            }
            start += pitch;
        }
    }

    fn draw(&self, frame: &mut [u8], left: i64, top: i64) {
        let x0 = (-left).max(0) as usize;
        let x1 = ((self.width as i64 - left).min(self.logo_w as i64)).max(0) as usize;
        for ly in 0..self.logo_h {
            let fy = top + ly as i64;
            if fy < 0 || fy >= self.height as i64 {
                continue;
            }
            let src = &self.pixels[ly * self.logo_w..(ly + 1) * self.logo_w];
            let row = fy as usize * self.width;
            for (lx, p) in src.iter().enumerate().take(x1).skip(x0) {
                if p[3] == 0 {
                    continue;
                }
                let i = (row + (left + lx as i64) as usize) * 4;
                for c in 0..3 {
                    frame[i + c] = frame[i + c].max(p[c]);
                }
            }
        }
    }
}

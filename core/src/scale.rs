//! Crops and bilinearly resizes 32-bit BGRX frames (or UYVY 4:2:2 frames, as
//! NDI delivers them) onto the canvas.
//!
//! The lookup tables are built once per source geometry, and output rows are
//! split across all cores. The horizontal pass runs on source rows and its
//! results are cached, so each source row is resampled once instead of once
//! per output row that uses it. For the common case, a 1920x180 crop of a
//! 1080p source stretched to 5760x540, that is 180 horizontal passes instead
//! of 540. The vertical pass then only blends two contiguous rows, byte by
//! byte in 16-bit lanes, which the compiler vectorizes with plain SSE2 (the
//! target i5-3470 has no AVX2).
//!
//! Weights are fixed-point (0..256).
//!
//! A UYVY scaler converts only the cropped part of the source rows it reads to
//! BGRX, just before the horizontal pass. For a 1080p source on the default
//! canvas that is 180 of 1080 rows, far less work than having the NDI SDK
//! convert every frame to BGRX in full.

pub struct Scaler {
    pub src_w: usize,
    pub src_h: usize,
    pub crop_x: usize,
    pub crop_y: usize,
    pub crop_w: usize,
    pub crop_h: usize,
    pub dst_w: usize,
    pub dst_h: usize,

    x_idx: Vec<u32>, // left source column (relative to crop_x), at most crop_w-2
    x_wt: Vec<u32>,  // weight of the right column, 0..256
    y_idx: Vec<u32>, // upper source row (absolute), at most crop_y+crop_h-2
    y_wt: Vec<u32>,  // weight of the lower row, 0..256

    workers: usize,
    caches: Vec<RowCache>,
    uyvy: Option<Box<YuvTables>>,
}

/// The two most recent horizontally resampled source rows of one worker.
struct RowCache {
    row: [Vec<u32>; 2],
    tag: [isize; 2], // source row held in row[i], -1 if none
    conv: Vec<u32>,  // UYVY input: the cropped source row converted to BGRX
}

pub fn default_workers() -> usize {
    std::thread::available_parallelism().map_or(1, |n| n.get())
}

impl Scaler {
    /// A scaler from a src_w x src_h BGRX frame to dst_w x dst_h. With center
    /// crop, the source is cropped to the destination aspect ratio first (no
    /// letterboxing, no distortion); otherwise it is stretched.
    pub fn bgrx(src_w: usize, src_h: usize, dst_w: usize, dst_h: usize, center_crop: bool, workers: usize) -> Self {
        let (cx, cy, cw, ch) = if center_crop {
            center_crop_rect(src_w, src_h, dst_w, dst_h)
        } else {
            (0, 0, src_w, src_h)
        };
        let (x_idx, x_wt) = bilinear_table(cw, dst_w, 0);
        let (y_idx, y_wt) = bilinear_table(ch, dst_h, cy);
        let workers = workers.max(1);
        let caches = (0..workers)
            .map(|_| RowCache { row: [vec![0; dst_w], vec![0; dst_w]], tag: [-1, -1], conv: Vec::new() })
            .collect();
        Self {
            src_w,
            src_h,
            crop_x: cx,
            crop_y: cy,
            crop_w: cw,
            crop_h: ch,
            dst_w,
            dst_h,
            x_idx,
            x_wt,
            y_idx,
            y_wt,
            workers,
            caches,
            uyvy: None,
        }
    }

    /// Like [`Scaler::bgrx`] for UYVY input. Sources with fewer than 720 lines
    /// use BT.601 colors, others BT.709, as NDI does.
    pub fn uyvy(src_w: usize, src_h: usize, dst_w: usize, dst_h: usize, center_crop: bool, workers: usize) -> Self {
        let mut s = Self::bgrx(src_w, src_h, dst_w, dst_h, center_crop, workers);
        s.uyvy = Some(Box::new(YuvTables::new(src_h < 720)));
        for c in &mut s.caches {
            c.conv = vec![0; s.crop_w];
        }
        s
    }

    /// Writes the resized frame into dst (dst_w*dst_h*4 bytes). src holds
    /// src_h rows of `stride` bytes. Both must be 4-byte aligned, which heap
    /// buffers and NDI frame buffers are; misaligned or short input is ignored.
    pub fn scale(&mut self, dst: &mut [u8], src: &[u8], stride: usize) {
        let row_bytes = if self.uyvy.is_some() { self.src_w.div_ceil(2) * 4 } else { self.src_w * 4 };
        let n = self.dst_w * self.dst_h;
        if dst.len() < n * 4 || self.src_h == 0 || src.len() < (self.src_h - 1) * stride + row_bytes || stride % 4 != 0 {
            return;
        }
        // SAFETY: u32 has no invalid bit patterns; alignment is checked below.
        let (pre, d, _) = unsafe { dst[..n * 4].align_to_mut::<u32>() };
        let (spre, src32, _) = unsafe { src.align_to::<u32>() };
        if !pre.is_empty() || !spre.is_empty() {
            return;
        }
        let stride = stride / 4;

        if self.crop_w == self.dst_w && self.crop_h == self.dst_h && self.uyvy.is_none() {
            for (y, out) in d.chunks_exact_mut(self.dst_w).enumerate() {
                let row = (self.crop_y + y) * stride + self.crop_x;
                out.copy_from_slice(&src32[row..row + self.dst_w]);
            }
            return;
        }

        let mut caches = std::mem::take(&mut self.caches);
        let band = self.dst_h.div_ceil(self.workers);
        let this = &*self;
        if self.workers == 1 {
            let c = &mut caches[0];
            c.tag = [-1, -1];
            this.scale_rows(d, src32, stride, c, 0);
        } else {
            std::thread::scope(|scope| {
                for (i, (rows, c)) in d.chunks_mut(band * this.dst_w).zip(caches.iter_mut()).enumerate() {
                    scope.spawn(move || {
                        c.tag = [-1, -1];
                        this.scale_rows(rows, src32, stride, c, i * band);
                    });
                }
            });
        }
        self.caches = caches;
    }

    /// Makes source row sy, resampled to dst_w pixels, present in the cache
    /// and returns its slot.
    fn hrow(&self, src: &[u32], stride: usize, c: &mut RowCache, sy: usize) -> usize {
        let sy_tag = sy as isize;
        if c.tag[0] == sy_tag {
            return 0;
        }
        if c.tag[1] == sy_tag {
            return 1;
        }
        // Replace the entry that is not the other row in use: rows are
        // requested in increasing order, so the lower tag is the stale one.
        let slot = if c.tag[1] < c.tag[0] { 1 } else { 0 };
        c.tag[slot] = sy_tag;

        let RowCache { row, conv, .. } = c;
        let r: &[u32] = match &self.uyvy {
            Some(t) => {
                let start = sy * stride;
                t.convert_row(conv, &src[start..start + self.src_w.div_ceil(2)], self.crop_x);
                conv
            }
            None => &src[sy * stride + self.crop_x..sy * stride + self.crop_x + self.crop_w],
        };
        let out = &mut row[slot];
        if r.len() < 2 {
            out.fill(r[0]);
            return slot;
        }
        for ((o, &i), &w) in out.iter_mut().zip(&self.x_idx).zip(&self.x_wt) {
            let i = i as usize;
            *o = blend32(r[i], r[i + 1], w);
        }
        slot
    }

    fn scale_rows(&self, d: &mut [u32], src: &[u32], stride: usize, c: &mut RowCache, y0: usize) {
        let single = self.crop_h < 2;
        for (k, out) in d.chunks_exact_mut(self.dst_w).enumerate() {
            let y = y0 + k;
            let (sy, wy) = (self.y_idx[y] as usize, self.y_wt[y]);
            if single || wy == 0 {
                let s = self.hrow(src, stride, c, sy);
                out.copy_from_slice(&c.row[s]);
                continue;
            }
            if wy == 256 {
                let s = self.hrow(src, stride, c, sy + 1);
                out.copy_from_slice(&c.row[s]);
                continue;
            }
            let sa = self.hrow(src, stride, c, sy);
            let sb = self.hrow(src, stride, c, sy + 1);
            blend_rows(as_bytes_mut(out), as_bytes(&c.row[sa]), as_bytes(&c.row[sb]), wy as u16);
        }
    }
}

/// out = a*(256-w)/256 + b*w/256 for every byte. Since the weights sum to 256
/// the sum fits in 16 bits, so this vectorizes to SSE2 pmullw.
pub fn blend_rows(out: &mut [u8], a: &[u8], b: &[u8], w: u16) {
    let iw = 256 - w;
    for ((o, &x), &y) in out.iter_mut().zip(a).zip(b) {
        *o = ((x as u16 * iw + y as u16 * w) >> 8) as u8;
    }
}

/// out = a*w/256 for every byte.
pub fn scale_bytes(out: &mut [u8], a: &[u8], w: u16) {
    for (o, &x) in out.iter_mut().zip(a) {
        *o = ((x as u16 * w) >> 8) as u8;
    }
}

fn as_bytes(s: &[u32]) -> &[u8] {
    // SAFETY: u8 has alignment 1 and any bit pattern is valid.
    unsafe { std::slice::from_raw_parts(s.as_ptr().cast(), s.len() * 4) }
}

fn as_bytes_mut(s: &mut [u32]) -> &mut [u8] {
    // SAFETY: as above; the borrow is exclusive.
    unsafe { std::slice::from_raw_parts_mut(s.as_mut_ptr().cast(), s.len() * 4) }
}

/// The largest centered rectangle of the source that has the destination's
/// aspect ratio, as (x, y, w, h).
pub fn center_crop_rect(src_w: usize, src_h: usize, dst_w: usize, dst_h: usize) -> (usize, usize, usize, usize) {
    // Compare src_w/src_h with dst_w/dst_h without floating point.
    let (a, b) = (src_w * dst_h, dst_w * src_h);
    if a > b {
        // source is wider: crop left and right
        let w = ((src_h * dst_w + dst_h / 2) / dst_h).clamp(1, src_w);
        ((src_w - w) / 2, 0, w, src_h)
    } else if a < b {
        // source is taller: crop top and bottom
        let h = ((src_w * dst_h + dst_w / 2) / dst_w).clamp(1, src_h);
        (0, (src_h - h) / 2, src_w, h)
    } else {
        (0, 0, src_w, src_h)
    }
}

/// Maps each of dst_n output samples to the lower of the two source samples it
/// lies between (plus offset) and the weight of the upper one, using
/// pixel-center alignment. The lower index never exceeds src_n-2, so index+1
/// is always valid; past the last sample the weight is 256. A single-sample
/// source maps everything to sample 0.
fn bilinear_table(src_n: usize, dst_n: usize, offset: usize) -> (Vec<u32>, Vec<u32>) {
    let mut idx = vec![0u32; dst_n];
    let mut wt = vec![0u32; dst_n];
    for i in 0..dst_n {
        if src_n < 2 {
            idx[i] = offset as u32;
            continue;
        }
        // Source coordinate of the output pixel center, in 1/256 pixel units.
        let pos = (((2 * i + 1) * src_n * 256) / (2 * dst_n)) as isize - 128;
        let pos = pos.max(0) as usize;
        let (mut p, mut f) = (pos >> 8, pos & 0xFF);
        if p >= src_n - 1 {
            p = src_n - 2;
            f = 256;
        }
        idx[i] = (p + offset) as u32;
        wt[i] = f as u32;
    }
    (idx, wt)
}

/// a*(256-w)/256 + b*w/256 for each byte of a BGRX pixel, two channels per multiply.
#[inline(always)]
fn blend32(a: u32, b: u32, w: u32) -> u32 {
    const M: u32 = 0x00FF00FF;
    let iw = 256 - w;
    let lo = (((a & M) * iw + (b & M) * w) >> 8) & M;
    let hi = (((a >> 8) & M) * iw + ((b >> 8) & M) * w) & !M;
    lo | hi
}

/// Converts limited-range Y'CbCr to BGRX with 16-bit fixed-point lookup
/// tables, so each pixel costs a few table loads and adds and no branches.
pub struct YuvTables {
    y: [i32; 256],  // (Y-16) * 255/219, scaled by 2^16, plus rounding
    rv: [i32; 256], // Cr contribution to R
    gu: [i32; 256], // Cb contribution to G (negative)
    gv: [i32; 256], // Cr contribution to G (negative)
    bu: [i32; 256], // Cb contribution to B
    // Maps an integer result v (-256 <= v < 768; actual results stay within
    // about -250..510) at index v+256 to 0..255.
    clamp: [u32; 1024],
}

impl YuvTables {
    pub fn new(bt601: bool) -> Self {
        // Kr/Kb of the color matrix; chroma scaled by 255/224 for limited range.
        let (kr, kb) = if bt601 { (0.299, 0.114) } else { (0.2126, 0.0722) };
        let kg = 1.0 - kr - kb;
        let (y_scale, c_scale) = (255.0 / 219.0, 255.0 / 224.0);
        let rv = 2.0 * (1.0 - kr) * c_scale;
        let bu = 2.0 * (1.0 - kb) * c_scale;
        let gu = 2.0 * (1.0 - kb) * kb / kg * c_scale;
        let gv = 2.0 * (1.0 - kr) * kr / kg * c_scale;
        let fix = |f: f64| -> i32 {
            if f < 0.0 {
                (f * 65536.0 - 0.5) as i32
            } else {
                (f * 65536.0 + 0.5) as i32
            }
        };
        let mut t = YuvTables { y: [0; 256], rv: [0; 256], gu: [0; 256], gv: [0; 256], bu: [0; 256], clamp: [0; 1024] };
        for i in 0..256 {
            let c = i as f64 - 128.0;
            t.y[i] = fix((i as f64 - 16.0) * y_scale) + (1 << 15) + (256 << 16); // +256: clamp table offset
            t.rv[i] = fix(rv * c);
            t.gu[i] = fix(-gu * c);
            t.gv[i] = fix(-gv * c);
            t.bu[i] = fix(bu * c);
        }
        for (i, v) in t.clamp.iter_mut().enumerate() {
            *v = (i as i32 - 256).clamp(0, 255) as u32;
        }
        t
    }

    #[inline(always)]
    fn pixel(&self, y: u8, r: i32, g: i32, b: i32) -> u32 {
        let yy = self.y[y as usize];
        let c = |v: i32| self.clamp[((v as u32) >> 16 & 1023) as usize];
        c(yy + b) | c(yy + g) << 8 | c(yy + r) << 16 | 0xFF00_0000
    }

    #[inline(always)]
    fn chroma(&self, u: u8, v: u8) -> (i32, i32, i32) {
        (self.rv[v as usize], self.gu[u as usize] + self.gv[v as usize], self.bu[u as usize])
    }

    /// Converts out.len() pixels of a UYVY row, starting at pixel x0, to BGRX.
    /// row holds the row's macropixels (U Y0 V Y1, little-endian u32). Chroma is
    /// co-sited with even pixels; odd pixels use the average of the two
    /// neighboring chroma samples.
    pub fn convert_row(&self, out: &mut [u32], row: &[u32], x0: usize) {
        let n = out.len();
        if n == 0 || row.is_empty() {
            return;
        }
        let last = row.len() - 1;
        let mut m = x0 >> 1;
        let mut i = 0;
        let avg = |a: u32, b: u32| ((a + b + 1) >> 1) as u8;
        if x0 & 1 == 1 {
            // starts on an odd pixel: second half of macropixel m
            let (mp, next) = (row[m], row[(m + 1).min(last)]);
            let (r, g, b) = self.chroma(avg(mp & 0xFF, next & 0xFF), avg(mp >> 16 & 0xFF, next >> 16 & 0xFF));
            out[0] = self.pixel((mp >> 24) as u8, r, g, b);
            i = 1;
            m += 1;
        }
        // Pairs whose next macropixel exists; the last pair is handled after.
        let pairs = ((n - i) / 2).min(last.saturating_sub(m));
        if pairs > 0 && m < last {
            let src = &row[m..m + pairs + 1];
            let dst = &mut out[i..i + 2 * pairs];
            for (k, d) in dst.chunks_exact_mut(2).enumerate() {
                let (mp, next) = (src[k], src[k + 1]);
                let (u0, v0) = (mp as u8, (mp >> 16) as u8);
                let (r, g, b) = self.chroma(u0, v0);
                d[0] = self.pixel((mp >> 8) as u8, r, g, b);
                let (r, g, b) = self.chroma(avg(u0 as u32, next & 0xFF), avg(v0 as u32, next >> 16 & 0xFF));
                d[1] = self.pixel((mp >> 24) as u8, r, g, b);
            }
            i += 2 * pairs;
            m += pairs;
        }
        while i < n {
            // at most one pair at the row end
            let mp = row[m];
            let (r, g, b) = self.chroma(mp as u8, (mp >> 16) as u8);
            out[i] = self.pixel((mp >> 8) as u8, r, g, b);
            if i + 1 < n {
                out[i + 1] = self.pixel((mp >> 24) as u8, r, g, b);
            }
            i += 2;
            m += 1;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Instant;

    fn make_bgrx(w: usize, h: usize, stride: usize, f: impl Fn(usize, usize) -> u32) -> Vec<u8> {
        let mut b = vec![0u8; stride * h];
        for y in 0..h {
            for x in 0..w {
                b[y * stride + x * 4..][..4].copy_from_slice(&f(x, y).to_le_bytes());
            }
        }
        b
    }

    fn make_uyvy(w: usize, h: usize, stride: usize, f: impl Fn(usize, usize) -> u32) -> Vec<u8> {
        make_bgrx(w.div_ceil(2), h, stride, f)
    }

    fn pixel(b: &[u8], w: usize, x: usize, y: usize) -> u32 {
        u32::from_le_bytes(b[(y * w + x) * 4..][..4].try_into().unwrap())
    }

    fn uyvy(u: u32, y0: u32, v: u32, y1: u32) -> u32 {
        u | y0 << 8 | v << 16 | y1 << 24
    }

    #[test]
    fn center_crop() {
        assert_eq!(center_crop_rect(1920, 1080, 5760, 540), (0, 450, 1920, 180));
        assert_eq!(center_crop_rect(5760, 540, 5760, 540), (0, 0, 5760, 540));
        assert_eq!(center_crop_rect(1920, 1080, 1920, 1080), (0, 0, 1920, 1080));
        assert_eq!(center_crop_rect(1000, 100, 100, 100), (450, 0, 100, 100));
    }

    #[test]
    fn identity_and_crop() {
        // Padded stride; the destination equals the center crop exactly.
        let (w, h, stride) = (8, 8, 8 * 4 + 16);
        let src = make_bgrx(w, h, stride, |x, y| (x << 16 | y << 8 | 0x7F) as u32);
        let mut s = Scaler::bgrx(w, h, 8, 2, true, 4); // crop rows 3..4
        let mut dst = vec![0u8; 8 * 2 * 4];
        s.scale(&mut dst, &src, stride);
        for y in 0..2 {
            for x in 0..8 {
                assert_eq!(pixel(&dst, 8, x, y) & 0xFFFFFF, (x << 16 | (y + 3) << 8 | 0x7F) as u32);
            }
        }
    }

    #[test]
    fn flat_and_gradient() {
        // A flat color must stay exactly flat (the X byte is ignored).
        let src = make_bgrx(64, 36, 64 * 4, |_, _| 0xFF336699);
        let mut s = Scaler::bgrx(64, 36, 192, 18, true, 3);
        let mut dst = vec![0u8; 192 * 18 * 4];
        s.scale(&mut dst, &src, 64 * 4);
        for p in dst.chunks_exact(4) {
            assert_eq!(u32::from_le_bytes(p.try_into().unwrap()) & 0xFFFFFF, 0x336699);
        }

        // A horizontal gradient must stay monotonic and within its range.
        let src = make_bgrx(64, 4, 64 * 4, |x, _| (x * 4) as u32);
        let mut s = Scaler::bgrx(64, 4, 640, 4, false, 2);
        let mut dst = vec![0u8; 640 * 4 * 4];
        s.scale(&mut dst, &src, 64 * 4);
        let mut prev = 0;
        for x in 0..640 {
            let v = pixel(&dst, 640, x, 1) & 0xFF;
            assert!(v >= prev && v <= 252, "gradient broken at x={x}: {v} after {prev}");
            prev = v;
        }
        assert!(prev >= 248);
    }

    #[test]
    fn yuv_known_colors() {
        let near = |got: u32, want: u32| (0..4).all(|i| ((got >> (i * 8) & 0xFF) as i32 - (want >> (i * 8) & 0xFF) as i32).abs() <= 2);
        let (t709, t601) = (YuvTables::new(false), YuvTables::new(true));
        let cases = [
            ("white", &t709, uyvy(128, 235, 128, 235), 0xFFFFFFFF),
            ("black", &t709, uyvy(128, 16, 128, 16), 0xFF000000),
            ("709 red", &t709, uyvy(102, 63, 240, 63), 0xFFFF0000),
            ("709 green", &t709, uyvy(42, 173, 26, 173), 0xFF00FF00),
            ("709 blue", &t709, uyvy(240, 32, 118, 32), 0xFF0000FF),
            ("601 red", &t601, uyvy(90, 81, 240, 81), 0xFFFF0000),
        ];
        for (name, tab, mp, want) in cases {
            let mut out = [0u32; 2];
            tab.convert_row(&mut out, &[mp], 0);
            for (i, &got) in out.iter().enumerate() {
                assert!(near(got, want), "{name} pixel {i} = {got:08x}, want {want:08x}");
            }
        }
    }

    /// Converting only the cropped part of each row (here starting on an odd
    /// pixel) gives exactly what converting the whole frame first and scaling
    /// it as BGRX gives.
    #[test]
    fn uyvy_matches_bgrx() {
        let (src_w, src_h, dst_w, dst_h): (usize, usize, usize, usize) = (1003, 120, 300, 300); // crop 120x120 at x=441
        let stride = src_w.div_ceil(2) * 4 + 8;
        let src = make_uyvy(src_w, src_h, stride, |m, y| {
            let h = ((m * 7919 + y * 104729) as u32).wrapping_mul(2654435761);
            uyvy(h & 0xFF, 16 + (h >> 8 & 0xFF) % 220, h >> 16 & 0xFF, 16 + (h >> 24) % 220)
        });
        let mut us = Scaler::uyvy(src_w, src_h, dst_w, dst_h, true, 4);
        assert_eq!(us.crop_x % 2, 1, "test wants an odd crop start");

        let tab = YuvTables::new(true);
        let mut reference = vec![0u32; src_w * src_h];
        let (_, src32, _) = unsafe { src.align_to::<u32>() };
        for y in 0..src_h {
            let row = &src32[y * stride / 4..y * stride / 4 + src_w.div_ceil(2)];
            tab.convert_row(&mut reference[y * src_w..(y + 1) * src_w], row, 0);
        }
        let mut bs = Scaler::bgrx(src_w, src_h, dst_w, dst_h, true, 4);
        let (mut got, mut want) = (vec![0u8; dst_w * dst_h * 4], vec![0u8; dst_w * dst_h * 4]);
        us.scale(&mut got, &src, stride);
        bs.scale(&mut want, as_bytes(&reference), src_w * 4);
        assert!(got == want, "UYVY path differs from BGRX path");
    }

    /// Timing of the NDI receive path; run with
    /// `cargo test --release scale::tests::bench -- --ignored --nocapture`.
    #[test]
    #[ignore]
    fn bench() {
        let canvas = (5760, 540);
        let run = |name: &str, s: &mut Scaler, src: &[u8], stride: usize| {
            let mut dst = vec![0u8; canvas.0 * canvas.1 * 4];
            s.scale(&mut dst, src, stride);
            let n = 300;
            let t = Instant::now();
            for _ in 0..n {
                s.scale(&mut dst, src, stride);
            }
            eprintln!("{name}: {:.2} ms/frame", t.elapsed().as_secs_f64() * 1000.0 / n as f64);
        };
        let w = default_workers();
        let bgrx = make_bgrx(1920, 1080, 1920 * 4, |x, y| ((x * y) as u32).wrapping_mul(2654435761));
        let uyvy_src = make_uyvy(1920, 1080, 1920 * 2, |m, y| ((m * y) as u32).wrapping_mul(2654435761));
        run("BGRX 1080p -> canvas", &mut Scaler::bgrx(1920, 1080, canvas.0, canvas.1, true, w), &bgrx, 1920 * 4);
        run("BGRX 1080p -> canvas, 1 core", &mut Scaler::bgrx(1920, 1080, canvas.0, canvas.1, true, 1), &bgrx, 1920 * 4);
        run("UYVY 1080p -> canvas", &mut Scaler::uyvy(1920, 1080, canvas.0, canvas.1, true, w), &uyvy_src, 1920 * 2);
        run("UYVY 1080p -> canvas, 1 core", &mut Scaler::uyvy(1920, 1080, canvas.0, canvas.1, true, 1), &uyvy_src, 1920 * 2);
    }
}

//! BGRX to NV12 conversion of one display's crop of the canvas, written
//! straight into a mapped VA surface.
//!
//! BT.601 limited range, like ffmpeg's `format=nv12` on a `bgr0` input (the
//! colors the displays showed with the ffmpeg encoder). Chroma is the average
//! of each 2x2 block. The loops work on whole rows in integer arithmetic so the
//! compiler can vectorize them with SSE2.

/// The planes of a mapped NV12 image.
pub struct Nv12Planes<'a> {
    pub width: usize,
    pub height: usize,
    pub y: &'a mut [u8],
    pub y_pitch: usize,
    pub uv: &'a mut [u8],
    pub uv_pitch: usize,
}

/// Converts the `planes.width` x `src_h` region at (x0, y0) of a BGRX frame
/// (`stride` bytes per row) into `planes`. Rows of the planes past `src_h`
/// (macroblock padding) repeat the last row. Width and height must be even.
pub fn bgrx_to_nv12(src: &[u8], stride: usize, x0: usize, y0: usize, src_h: usize, planes: &mut Nv12Planes) {
    let w = planes.width;
    debug_assert!(w % 2 == 0 && src_h % 2 == 0 && src_h <= planes.height);
    let row = |y: usize| &src[(y0 + y) * stride + x0 * 4..(y0 + y) * stride + (x0 + w) * 4];
    // Per-row sums of B, G, R over pixel pairs, for the chroma average.
    let mut sum = vec![[0u16; 3]; w / 2];

    for cy in 0..src_h / 2 {
        for s in sum.iter_mut() {
            *s = [0; 3];
        }
        for dy in 0..2 {
            let y = cy * 2 + dy;
            let px = row(y);
            let out = &mut planes.y[y * planes.y_pitch..y * planes.y_pitch + w];
            for (o, p) in out.iter_mut().zip(px.chunks_exact(4)) {
                let (b, g, r) = (p[0] as u32, p[1] as u32, p[2] as u32);
                *o = (((66 * r + 129 * g + 25 * b + 128) >> 8) + 16) as u8;
            }
            for (s, p) in sum.iter_mut().zip(px.chunks_exact(8)) {
                s[0] += p[0] as u16 + p[4] as u16;
                s[1] += p[1] as u16 + p[5] as u16;
                s[2] += p[2] as u16 + p[6] as u16;
            }
        }
        let out = &mut planes.uv[cy * planes.uv_pitch..cy * planes.uv_pitch + w];
        for (o, s) in out.chunks_exact_mut(2).zip(&sum) {
            // Average of 4 samples, with rounding.
            let (b, g, r) = (((s[0] + 2) >> 2) as i32, ((s[1] + 2) >> 2) as i32, ((s[2] + 2) >> 2) as i32);
            o[0] = (((-38 * r - 74 * g + 112 * b + 128) >> 8) + 128) as u8;
            o[1] = (((112 * r - 94 * g - 18 * b + 128) >> 8) + 128) as u8;
        }
    }

    // Padding below the picture (cropped away by the SPS): repeat the last row.
    for y in src_h..planes.height {
        let (done, rest) = planes.y.split_at_mut(y * planes.y_pitch);
        rest[..w].copy_from_slice(&done[(src_h - 1) * planes.y_pitch..(src_h - 1) * planes.y_pitch + w]);
    }
    for cy in src_h / 2..planes.height / 2 {
        let last = src_h / 2 - 1;
        let (done, rest) = planes.uv.split_at_mut(cy * planes.uv_pitch);
        rest[..w].copy_from_slice(&done[last * planes.uv_pitch..last * planes.uv_pitch + w]);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn convert(bgrx: [u8; 4]) -> (u8, u8, u8) {
        let src: Vec<u8> = bgrx.repeat(4 * 2);
        let (mut y, mut uv) = (vec![0u8; 4 * 4], vec![0u8; 4 * 2]);
        let mut p = Nv12Planes { width: 4, height: 4, y: &mut y, y_pitch: 4, uv: &mut uv, uv_pitch: 4 };
        bgrx_to_nv12(&src, 16, 0, 0, 2, &mut p);
        assert!(y.iter().all(|&v| v == y[0]), "padding rows repeat the picture");
        (y[0], uv[0], uv[1])
    }

    #[test]
    fn known_colors_bt601_limited() {
        let near = |a: (u8, u8, u8), b: (u8, u8, u8)| {
            (a.0 as i32 - b.0 as i32).abs() <= 1 && (a.1 as i32 - b.1 as i32).abs() <= 1 && (a.2 as i32 - b.2 as i32).abs() <= 1
        };
        for (bgrx, want) in [
            ([255, 255, 255, 0], (235, 128, 128)),
            ([0, 0, 0, 0], (16, 128, 128)),
            ([0, 0, 255, 0], (82, 90, 240)),  // red
            ([0, 255, 0, 0], (145, 54, 34)),  // green
            ([255, 0, 0, 0], (41, 240, 110)), // blue
        ] {
            let got = convert(bgrx);
            assert!(near(got, want), "{bgrx:?} -> {got:?}, want {want:?}");
        }
    }

    #[test]
    fn crop_offset_and_pitch() {
        // 8x4 BGRX frame: left half black, right half white; convert the right half.
        let mut src = vec![0u8; 8 * 4 * 4];
        for y in 0..4 {
            for x in 4..8 {
                src[(y * 8 + x) * 4..][..3].copy_from_slice(&[255, 255, 255]);
            }
        }
        let (mut y, mut uv) = (vec![0u8; 6 * 4], vec![0u8; 6 * 2]);
        let mut p = Nv12Planes { width: 4, height: 4, y: &mut y, y_pitch: 6, uv: &mut uv, uv_pitch: 6 };
        bgrx_to_nv12(&src, 32, 4, 0, 4, &mut p);
        for r in 0..4 {
            assert_eq!(&y[r * 6..r * 6 + 4], &[235; 4]);
        }
        assert_eq!(&uv[..4], &[128; 4]);
    }
}

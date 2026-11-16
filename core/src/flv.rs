//! Turns an FLV stream carrying H.264 (as written by ffmpeg `-f flv`) into
//! Annex-B Access Units.
//!
//! The encoder output is read as FLV rather than as a raw Annex-B stream
//! because every FLV tag carries its size: an Access Unit is complete the
//! moment its last byte arrives. In a raw stream the end of a frame is only
//! known when the next frame's start code arrives, which holds every frame back
//! by one frame interval.

use std::io::{self, Read};

const TAG_VIDEO: u8 = 9;
const CODEC_AVC: u8 = 7;
const AVC_SEQ_HEADER: u8 = 0;
const AVC_NALU: u8 = 1;
const MAX_TAG_SIZE: usize = 16 << 20;
const START_CODE: [u8; 4] = [0, 0, 0, 1];

const NAL_IDR: u8 = 5;
const NAL_SPS: u8 = 7;
const NAL_AUD: u8 = 9;

pub struct FlvReader<R> {
    r: R,
    tag: Vec<u8>,
    out: Vec<u8>,
    /// SPS and PPS from the AVC sequence header, in Annex-B form.
    param_sets: Vec<u8>,
    nal_len_size: usize,
    got_header: bool,
}

fn invalid(msg: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, format!("flv: {msg}"))
}

impl<R: Read> FlvReader<R> {
    pub fn new(r: R) -> Self {
        Self { r, tag: Vec::new(), out: Vec::new(), param_sets: Vec::new(), nal_len_size: 4, got_header: false }
    }

    /// Returns the next Access Unit and whether it is a keyframe, or `None` at
    /// the end of the stream. The data is valid until the next call.
    pub fn next_au(&mut self) -> io::Result<Option<(&[u8], bool)>> {
        if !self.got_header {
            if !self.read_file_header()? {
                return Ok(None);
            }
            self.got_header = true;
        }
        loop {
            // Tag header: type(1) size(3) timestamp(3) timestamp_ext(1) stream_id(3)
            let mut hdr = [0u8; 11];
            if !read_full_or_eof(&mut self.r, &mut hdr)? {
                return Ok(None);
            }
            let tag_type = hdr[0] & 0x1F;
            let size = (hdr[1] as usize) << 16 | (hdr[2] as usize) << 8 | hdr[3] as usize;
            if size > MAX_TAG_SIZE {
                return Err(invalid(&format!("tag size {size} too large")));
            }
            // Tag body followed by the 4-byte PreviousTagSize.
            self.tag.resize(size + 4, 0);
            self.r.read_exact(&mut self.tag)?;
            let body = &self.tag[..size];
            if tag_type != TAG_VIDEO || body.len() < 5 || body[0] & 0x0F != CODEC_AVC {
                continue;
            }
            match body[1] {
                AVC_SEQ_HEADER => {
                    let (ps, len_size) = parse_avc_config(&body[5..])?;
                    self.param_sets = ps;
                    self.nal_len_size = len_size;
                }
                AVC_NALU => {
                    let key = to_annex_b(&body[5..], self.nal_len_size, &self.param_sets, &mut self.out)?;
                    if !self.out.is_empty() {
                        return Ok(Some((&self.out, key)));
                    }
                }
                _ => {}
            }
        }
    }

    fn read_file_header(&mut self) -> io::Result<bool> {
        let mut h = [0u8; 9];
        if !read_full_or_eof(&mut self.r, &mut h)? {
            return Ok(false);
        }
        if &h[..3] != b"FLV" {
            return Err(invalid("bad signature"));
        }
        // Skip any extra header bytes plus PreviousTagSize0.
        let header_len = u32::from_be_bytes(h[5..9].try_into().unwrap()) as i64;
        let skip = header_len - 9 + 4;
        if skip < 0 {
            return Err(invalid("bad header size"));
        }
        io::copy(&mut (&mut self.r).take(skip as u64), &mut io::sink())?;
        Ok(true)
    }
}

/// Like `read_exact`, but a clean EOF before the first byte returns false.
fn read_full_or_eof(r: &mut impl Read, buf: &mut [u8]) -> io::Result<bool> {
    let mut n = 0;
    while n < buf.len() {
        match r.read(&mut buf[n..]) {
            Ok(0) if n == 0 => return Ok(false),
            Ok(0) => return Err(io::ErrorKind::UnexpectedEof.into()),
            Ok(k) => n += k,
            Err(e) if e.kind() == io::ErrorKind::Interrupted => {}
            Err(e) => return Err(e),
        }
    }
    Ok(true)
}

/// Reads an AVCDecoderConfigurationRecord: its SPS/PPS in Annex-B form and the
/// NAL length size.
fn parse_avc_config(b: &[u8]) -> io::Result<(Vec<u8>, usize)> {
    if b.len() < 6 {
        return Err(invalid("short AVC config"));
    }
    let len_size = (b[4] & 0x03) as usize + 1;
    let mut ps = Vec::new();
    let mut p = 5;
    for mask in [0x1F, 0xFF] {
        // SPS count, then PPS count
        let n = *b.get(p).ok_or_else(|| invalid("truncated AVC config"))? & mask;
        p += 1;
        for _ in 0..n {
            let l = b.get(p..p + 2).ok_or_else(|| invalid("truncated AVC config"))?;
            let l = u16::from_be_bytes([l[0], l[1]]) as usize;
            p += 2;
            let nal = b.get(p..p + l).ok_or_else(|| invalid("truncated AVC config"))?;
            ps.extend_from_slice(&START_CODE);
            ps.extend_from_slice(nal);
            p += l;
        }
    }
    Ok((ps, len_size))
}

/// Converts length-prefixed NAL units to Annex-B into `out`. For an IDR access
/// unit without in-band SPS/PPS, the parameter sets from the sequence header
/// are inserted (after the AUD, which must stay first) so receivers can join on
/// any keyframe. Returns whether the access unit is a keyframe.
fn to_annex_b(b: &[u8], len_size: usize, param_sets: &[u8], out: &mut Vec<u8>) -> io::Result<bool> {
    out.clear();
    let (mut key, mut has_sps) = (false, false);
    let mut after_aud = 0; // insertion point for parameter sets: just past a leading AUD
    let mut p = 0;
    while p < b.len() {
        let len_bytes = b.get(p..p + len_size).ok_or_else(|| invalid("truncated NAL length"))?;
        let l = len_bytes.iter().fold(0usize, |acc, &x| acc << 8 | x as usize);
        p += len_size;
        if l == 0 {
            continue;
        }
        let nal = b.get(p..p + l).ok_or_else(|| invalid("truncated NAL unit"))?;
        p += l;
        match nal[0] & 0x1F {
            NAL_IDR => key = true,
            NAL_SPS => has_sps = true,
            _ => {}
        }
        out.extend_from_slice(&START_CODE);
        out.extend_from_slice(nal);
        if nal[0] & 0x1F == NAL_AUD && out.len() == 4 + nal.len() {
            after_aud = out.len();
        }
    }
    if key && !has_sps && !param_sets.is_empty() {
        out.splice(after_aud..after_aud, param_sets.iter().copied());
    }
    Ok(key)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn flv_tag(tag_type: u8, body: &[u8]) -> Vec<u8> {
        let n = body.len();
        let mut b = vec![tag_type, (n >> 16) as u8, (n >> 8) as u8, n as u8, 0, 0, 0, 0, 0, 0, 0];
        b.extend_from_slice(body);
        b.extend_from_slice(&((11 + n) as u32).to_be_bytes());
        b
    }

    fn avcc(nal: &[u8]) -> Vec<u8> {
        let mut v = (nal.len() as u32).to_be_bytes().to_vec();
        v.extend_from_slice(nal);
        v
    }

    #[test]
    fn synthetic_stream() {
        let mut s = vec![b'F', b'L', b'V', 1, 1, 0, 0, 0, 9, 0, 0, 0, 0];
        s.extend(flv_tag(18, b"script data"));
        // AVCDecoderConfigurationRecord: one SPS (0x67 0xAA), one PPS (0x68 0xBB)
        let cfg = [0x17, AVC_SEQ_HEADER, 0, 0, 0, 1, 0x42, 0, 0x1F, 0xFF, 0xE1, 0, 2, 0x67, 0xAA, 1, 0, 2, 0x68, 0xBB];
        s.extend(flv_tag(TAG_VIDEO, &cfg));
        // IDR access unit without in-band SPS/PPS: AUD, IDR slice
        let mut idr = vec![0x17, AVC_NALU, 0, 0, 0];
        idr.extend(avcc(&[0x09, 0xF0]));
        idr.extend(avcc(&[0x65, 0x01, 0x02]));
        s.extend(flv_tag(TAG_VIDEO, &idr));
        // P access unit
        let mut p = vec![0x27, AVC_NALU, 0, 0, 0];
        p.extend(avcc(&[0x09, 0xF0]));
        p.extend(avcc(&[0x41, 0x03]));
        s.extend(flv_tag(TAG_VIDEO, &p));

        let mut r = FlvReader::new(&s[..]);
        let (data, key) = r.next_au().unwrap().unwrap();
        assert!(key);
        assert_eq!(
            data,
            [0, 0, 0, 1, 0x09, 0xF0, 0, 0, 0, 1, 0x67, 0xAA, 0, 0, 0, 1, 0x68, 0xBB, 0, 0, 0, 1, 0x65, 0x01, 0x02]
        );
        let (data, key) = r.next_au().unwrap().unwrap();
        assert!(!key);
        assert_eq!(data, [0, 0, 0, 1, 0x09, 0xF0, 0, 0, 0, 1, 0x41, 0x03]);
        assert!(r.next_au().unwrap().is_none());
    }

    #[test]
    fn truncated_tag_is_an_error() {
        let mut s = vec![b'F', b'L', b'V', 1, 1, 0, 0, 0, 9, 0, 0, 0, 0];
        let tag = flv_tag(TAG_VIDEO, &[0x17, AVC_NALU, 0, 0, 0, 0, 0, 0, 2, 0x65]);
        s.extend_from_slice(&tag[..tag.len() - 6]);
        assert!(FlvReader::new(&s[..]).next_au().is_err());
    }

    /// Access units recovered from real encoder output (the encoders and
    /// options the pipeline uses) decode back into the same number of frames,
    /// and every keyframe carries SPS so a receiver can join on it.
    #[test]
    fn ffmpeg_round_trip() {
        use std::process::{Command, Stdio};
        if Command::new("ffmpeg").arg("-version").stdout(Stdio::null()).status().is_err() {
            eprintln!("ffmpeg not installed; skipping");
            return;
        }
        const FRAMES: usize = 45;
        let mut encoders: Vec<(&str, Vec<&str>)> = vec![(
            "libx264",
            vec![
                "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
                "-profile:v", "baseline", "-g", "30", "-x264-params", "slices=1:aud=1:repeat-headers=1",
            ],
        )];
        if std::path::Path::new("/dev/dri/renderD128").exists() {
            encoders.push((
                "h264_vaapi",
                vec![
                    "-vaapi_device", "/dev/dri/renderD128", "-vf", "format=nv12,hwupload", "-c:v", "h264_vaapi",
                    "-profile:v", "constrained_baseline", "-bf", "0", "-aud", "1", "-g", "30", "-idr_interval", "0",
                ],
            ));
        }
        let dir = std::env::temp_dir();
        for (name, enc) in encoders {
            // A file, not stdout: the legacy i965 VA-API driver prints a notice
            // to stdout. (The encoder writes to fd 3+, so it is unaffected.)
            let path = dir.join(format!("booth-core-flv-{}-{name}.flv", std::process::id()));
            let ok = Command::new("ffmpeg")
                .args(["-y", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30"])
                .args(["-frames:v", &FRAMES.to_string()])
                .args(&enc)
                .args(["-an", "-f", "flv", "-flvflags", "no_duration_filesize"])
                .arg(&path)
                .status()
                .is_ok_and(|s| s.success());
            if !ok {
                eprintln!("{name}: encoder unavailable; skipping");
                continue;
            }
            let data = std::fs::read(&path).unwrap();
            let _ = std::fs::remove_file(&path);

            let mut r = FlvReader::new(&data[..]);
            let mut annexb = Vec::new();
            let (mut n, mut keys) = (0, 0);
            while let Some((au, key)) = r.next_au().unwrap() {
                assert!(n > 0 || key, "{name}: first access unit is not a keyframe");
                if key {
                    keys += 1;
                    assert!(au.windows(5).any(|w| w == [0, 0, 0, 1, 0x67]), "{name}: keyframe {n} has no SPS");
                }
                annexb.extend_from_slice(au);
                n += 1;
            }
            assert_eq!(n, FRAMES, "{name}: access units");
            assert!(keys >= 2, "{name}: got {keys} keyframes, want at least 2 (GOP 30)");

            let mut dec = Command::new("ffmpeg")
                .args(["-hide_banner", "-loglevel", "error", "-f", "h264", "-i", "pipe:0", "-f", "framecrc", "pipe:1"])
                .stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .spawn()
                .unwrap();
            let mut stdin = dec.stdin.take().unwrap();
            let feeder = std::thread::spawn(move || {
                use std::io::Write;
                stdin.write_all(&annexb).unwrap();
            });
            let out = dec.wait_with_output().unwrap();
            feeder.join().unwrap();
            assert!(out.status.success(), "{name}: decoding the recovered stream failed");
            let decoded = out.stdout.split(|&b| b == b'\n').filter(|l| !l.is_empty() && l[0] != b'#').count();
            assert_eq!(decoded, FRAMES, "{name}: decoded frames");
        }
    }
}

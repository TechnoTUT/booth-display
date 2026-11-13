package pipeline

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
)

func flvTag(tagType byte, body []byte) []byte {
	var b []byte
	n := len(body)
	b = append(b, tagType, byte(n>>16), byte(n>>8), byte(n), 0, 0, 0, 0, 0, 0, 0)
	b = append(b, body...)
	var prev [4]byte
	binary.BigEndian.PutUint32(prev[:], uint32(11+n))
	return append(b, prev[:]...)
}

func avccNAL(nal ...byte) []byte {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(nal)))
	return append(l[:], nal...)
}

func TestFLVReaderSynthetic(t *testing.T) {
	var s []byte
	s = append(s, 'F', 'L', 'V', 1, 1, 0, 0, 0, 9, 0, 0, 0, 0)
	s = append(s, flvTag(18, []byte("script data"))...)

	// AVCDecoderConfigurationRecord: one SPS (0x67 0xAA), one PPS (0x68 0xBB)
	cfg := []byte{0x17, flvAVCSeqHeader, 0, 0, 0, 1, 0x42, 0, 0x1F, 0xFF, 0xE1, 0, 2, 0x67, 0xAA, 1, 0, 2, 0x68, 0xBB}
	s = append(s, flvTag(flvTagVideo, cfg)...)

	// IDR access unit without in-band SPS/PPS: AUD, IDR slice
	idr := []byte{0x17, flvAVCNALU, 0, 0, 0}
	idr = append(idr, avccNAL(0x09, 0xF0)...)
	idr = append(idr, avccNAL(0x65, 0x01, 0x02)...)
	s = append(s, flvTag(flvTagVideo, idr)...)

	// P access unit
	p := []byte{0x27, flvAVCNALU, 0, 0, 0}
	p = append(p, avccNAL(0x09, 0xF0)...)
	p = append(p, avccNAL(0x41, 0x03)...)
	s = append(s, flvTag(flvTagVideo, p)...)

	fr := NewFLVReader(bytes.NewReader(s))

	data, key, err := fr.Next()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 0, 0, 1, 0x09, 0xF0, 0, 0, 0, 1, 0x67, 0xAA, 0, 0, 0, 1, 0x68, 0xBB, 0, 0, 0, 1, 0x65, 0x01, 0x02}
	if !key || !bytes.Equal(data, want) {
		t.Errorf("IDR AU = %x (key=%v), want %x", data, key, want)
	}

	data, key, err = fr.Next()
	if err != nil {
		t.Fatal(err)
	}
	want = []byte{0, 0, 0, 1, 0x09, 0xF0, 0, 0, 0, 1, 0x41, 0x03}
	if key || !bytes.Equal(data, want) {
		t.Errorf("P AU = %x (key=%v), want %x", data, key, want)
	}

	if _, _, err := fr.Next(); err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

// TestFLVReaderFFmpeg checks that the access units recovered from real encoder
// output decode back into the same number of frames.
func TestFLVReaderFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	const frames = 45
	encoders := map[string][]string{
		"libx264": {"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
			"-profile:v", "baseline", "-g", "30", "-x264-params", "slices=1:aud=1:repeat-headers=1"},
	}
	if _, err := os.Stat("/dev/dri/renderD128"); err == nil {
		encoders["h264_vaapi"] = []string{"-vaapi_device", "/dev/dri/renderD128", "-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi", "-profile:v", "constrained_baseline", "-bf", "0", "-aud", "1", "-g", "30", "-idr_interval", "0"}
	}
	for name, encArgs := range encoders {
		t.Run(name, func(t *testing.T) {
			args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
				"-frames:v", fmt.Sprint(frames)}
			args = append(args, encArgs...)
			// Write to a file, not stdout: the legacy i965 VA-API driver prints a
			// notice to stdout. (MultiEncoder writes to fd 3+, so it is unaffected.)
			path := t.TempDir() + "/out.flv"
			args = append(args, "-an", "-f", "flv", "-flvflags", "no_duration_filesize", path)
			if err := exec.Command("ffmpeg", args...).Run(); err != nil {
				t.Skipf("encoder unavailable: %v", err)
			}
			out, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			fr := NewFLVReader(bytes.NewReader(out))
			var annexB []byte
			n, keys := 0, 0
			for {
				data, key, err := fr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if n == 0 && !key {
					t.Fatal("first access unit is not a keyframe")
				}
				if key {
					keys++
					if !bytes.Contains(data, []byte{0, 0, 0, 1, 0x67}) && !bytes.Contains(data, []byte{0, 0, 1, 0x67}) {
						t.Errorf("keyframe %d has no SPS", n)
					}
				}
				annexB = append(annexB, data...)
				n++
			}
			if n != frames {
				t.Errorf("got %d access units, want %d", n, frames)
			}
			if keys < 2 {
				t.Errorf("got %d keyframes, want at least 2 (GOP 30)", keys)
			}

			dec := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "h264", "-i", "pipe:0",
				"-f", "framecrc", "pipe:1")
			dec.Stdin = bytes.NewReader(annexB)
			crc, err := dec.Output()
			if err != nil {
				t.Fatalf("decoding recovered stream failed: %v", err)
			}
			decoded := 0
			for _, line := range bytes.Split(crc, []byte("\n")) {
				if len(line) > 0 && line[0] != '#' {
					decoded++
				}
			}
			if decoded != frames {
				t.Errorf("decoded %d frames, want %d", decoded, frames)
			}
		})
	}
}

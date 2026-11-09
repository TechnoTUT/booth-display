// Package ndi receives NDI video by calling the NDI SDK (libndi) directly.
//
// libndi is loaded at runtime with dlopen, so the controller builds and runs
// without the SDK installed; only NDI input is unavailable then. The few SDK
// types used here are declared below from the SDK's MIT-licensed headers
// (NDI SDK 6), so no SDK headers are needed at build time.
package ndi

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	const char* p_ndi_name;
	const char* p_url_address;
} ndi_source_t;

typedef struct {
	bool show_local_sources;
	const char* p_groups;
	const char* p_extra_ips;
} ndi_find_create_t;

typedef struct {
	ndi_source_t source_to_connect_to;
	int color_format;
	int bandwidth;
	bool allow_video_fields;
	const char* p_ndi_recv_name;
} ndi_recv_create_v3_t;

typedef struct {
	int xres, yres;
	uint32_t FourCC;
	int frame_rate_N, frame_rate_D;
	float picture_aspect_ratio;
	int frame_format_type;
	int64_t timecode;
	uint8_t* p_data;
	int line_stride_in_bytes;
	const char* p_metadata;
	int64_t timestamp;
} ndi_video_frame_v2_t;

static bool (*p_initialize)(void);
static void* (*p_find_create_v2)(const ndi_find_create_t*);
static void (*p_find_destroy)(void*);
static bool (*p_find_wait_for_sources)(void*, uint32_t);
static const ndi_source_t* (*p_find_get_current_sources)(void*, uint32_t*);
static void* (*p_recv_create_v3)(const ndi_recv_create_v3_t*);
static void (*p_recv_destroy)(void*);
static int (*p_recv_capture_v2)(void*, ndi_video_frame_v2_t*, void*, void*, uint32_t);
static void (*p_recv_free_video_v2)(void*, const ndi_video_frame_v2_t*);
static int (*p_recv_get_no_connections)(void*);
static const char* (*p_version)(void);

// ndi_load opens libndi at path and resolves the functions used here.
// It returns NULL on success or the name of what failed.
static const char* ndi_load(const char* path) {
	void* h = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	if (!h) return dlerror();
#define SYM(var, name) if (!(*(void**)(&var) = dlsym(h, name))) return name;
	SYM(p_initialize, "NDIlib_initialize")
	SYM(p_find_create_v2, "NDIlib_find_create_v2")
	SYM(p_find_destroy, "NDIlib_find_destroy")
	SYM(p_find_wait_for_sources, "NDIlib_find_wait_for_sources")
	SYM(p_find_get_current_sources, "NDIlib_find_get_current_sources")
	SYM(p_recv_create_v3, "NDIlib_recv_create_v3")
	SYM(p_recv_destroy, "NDIlib_recv_destroy")
	SYM(p_recv_capture_v2, "NDIlib_recv_capture_v2")
	SYM(p_recv_free_video_v2, "NDIlib_recv_free_video_v2")
	SYM(p_recv_get_no_connections, "NDIlib_recv_get_no_connections")
	SYM(p_version, "NDIlib_version")
#undef SYM
	if (!p_initialize()) return "NDIlib_initialize failed (CPU not supported?)";
	return NULL;
}

static void* ndi_find_create(void) {
	ndi_find_create_t c = { true, NULL, NULL };
	return p_find_create_v2(&c);
}
static void ndi_find_destroy(void* f) { p_find_destroy(f); }
static bool ndi_find_wait(void* f, uint32_t ms) { return p_find_wait_for_sources(f, ms); }
static const ndi_source_t* ndi_find_sources(void* f, uint32_t* n) { return p_find_get_current_sources(f, n); }

// Video only, in BGRX/BGRA, full quality, progressive frames. Audio and
// metadata are not requested, so the SDK discards them instead of queueing.
static void* ndi_recv_create(const char* name, const char* url, const char* recv_name) {
	ndi_recv_create_v3_t c = { { name, url }, 0, 100, false, recv_name };
	return p_recv_create_v3(&c);
}
static void ndi_recv_destroy(void* r) { p_recv_destroy(r); }
static int ndi_recv_capture_video(void* r, ndi_video_frame_v2_t* v, uint32_t ms) {
	return p_recv_capture_v2(r, v, NULL, NULL, ms);
}
static void ndi_recv_free_video(void* r, ndi_video_frame_v2_t* v) { p_recv_free_video_v2(r, v); }
static int ndi_recv_connections(void* r) { return p_recv_get_no_connections(r); }
static const char* ndi_version(void) { return p_version(); }
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	frameTypeVideo = 1
	frameTypeError = 4

	fourCCBGRX = 'B' | 'G'<<8 | 'R'<<16 | 'X'<<24
	fourCCBGRA = 'B' | 'G'<<8 | 'R'<<16 | 'A'<<24
)

var (
	loadOnce sync.Once
	loadErr  error
	version  string
)

// libraryCandidates lists where libndi is looked for, in order: the NDI runtime
// directory named by the SDK's environment variable, the system library path,
// and the copy bundled with the Python cyndilib package in the repository's
// .venv (created by "make setup-python").
func libraryCandidates() []string {
	var c []string
	if dir := os.Getenv("NDI_RUNTIME_DIR_V6"); dir != "" {
		c = append(c, filepath.Join(dir, "libndi.so.6"), filepath.Join(dir, "libndi.so"))
	}
	c = append(c, "libndi.so.6", "libndi.so")
	// The controller is normally started from the repository root; also look in
	// parent directories so tests and other working directories find it too.
	dir, _ := os.Getwd()
	for range 4 {
		m, _ := filepath.Glob(filepath.Join(dir, ".venv/lib/python*/site-packages/cyndilib/wrapper/bin/x86_64-linux-gnu/libndi.so*"))
		sort.Strings(m)
		c = append(c, m...)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return c
}

// Load loads and initializes libndi once. Every other function calls it.
func Load() error {
	loadOnce.Do(func() {
		var errs []string
		for _, path := range libraryCandidates() {
			cpath := C.CString(path)
			msg := C.ndi_load(cpath)
			C.free(unsafe.Pointer(cpath))
			if msg == nil {
				version = C.GoString(C.ndi_version())
				return
			}
			errs = append(errs, fmt.Sprintf("%s: %s", path, C.GoString(msg)))
		}
		loadErr = fmt.Errorf("NDI runtime (libndi) not found; install the NDI SDK/runtime, set NDI_RUNTIME_DIR_V6, or run \"make setup-python\" [%s]",
			strings.Join(errs, "; "))
	})
	return loadErr
}

// Version returns the loaded libndi version string.
func Version() string { return version }

// Source is an NDI sender found on the network.
type Source struct {
	Name string // "MACHINE (Stream)"
	URL  string
}

// Discover waits until the list of NDI sources stops changing for settle, or
// timeout elapses, and returns it. Senders are found over mDNS one by one, so
// stopping at the first hit returns an incomplete list on busy networks.
func Discover(timeout, settle time.Duration) ([]Source, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	f := C.ndi_find_create()
	if f == nil {
		return nil, errors.New("NDIlib_find_create_v2 failed")
	}
	defer C.ndi_find_destroy(f)

	end := time.Now().Add(timeout)
	var last []Source
	lastChange := time.Now()
	for time.Now().Before(end) {
		C.ndi_find_wait(f, 200)
		cur := currentSources(f)
		if !sameSources(cur, last) {
			last, lastChange = cur, time.Now()
		} else if len(cur) > 0 && time.Since(lastChange) >= settle {
			break
		}
	}
	return last, nil
}

func currentSources(f unsafe.Pointer) []Source {
	var n C.uint32_t
	p := C.ndi_find_sources(f, &n)
	if p == nil || n == 0 {
		return nil
	}
	list := unsafe.Slice(p, int(n))
	out := make([]Source, 0, len(list))
	for _, s := range list {
		out = append(out, Source{Name: C.GoString(s.p_ndi_name), URL: C.GoString(s.p_url_address)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sameSources(a, b []Source) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

// FindSource looks for a source whose name equals name, or whose stream part
// ("MACHINE (Stream)" -> "Stream") equals it, or that contains it, waiting up
// to timeout. It returns early with an error when cancel is closed.
func FindSource(name string, timeout time.Duration, cancel <-chan struct{}) (Source, error) {
	if err := Load(); err != nil {
		return Source{}, err
	}
	f := C.ndi_find_create()
	if f == nil {
		return Source{}, errors.New("NDIlib_find_create_v2 failed")
	}
	defer C.ndi_find_destroy(f)

	end := time.Now().Add(timeout)
	for {
		if s, ok := matchSource(currentSources(f), name); ok {
			return s, nil
		}
		if time.Now().After(end) {
			return Source{}, fmt.Errorf("NDI source %q not found on the network", name)
		}
		select {
		case <-cancel:
			return Source{}, errors.New("cancelled")
		default:
		}
		C.ndi_find_wait(f, 100)
	}
}

func matchSource(list []Source, name string) (Source, bool) {
	for _, s := range list {
		if s.Name == name || StreamName(s.Name) == name {
			return s, true
		}
	}
	for _, s := range list {
		if strings.Contains(s.Name, name) {
			return s, true
		}
	}
	return Source{}, false
}

// HostName and StreamName split "MACHINE (Stream)" into its parts.
func HostName(full string) string {
	if i := strings.Index(full, " ("); i >= 0 {
		return full[:i]
	}
	return full
}

func StreamName(full string) string {
	if i := strings.Index(full, " ("); i >= 0 && strings.HasSuffix(full, ")") {
		return full[i+2 : len(full)-1]
	}
	return ""
}

// Receiver receives video frames from one NDI source.
type Receiver struct {
	p    unsafe.Pointer
	cur  C.ndi_video_frame_v2_t
	have bool
}

// NewReceiver connects to src. Frames are delivered as BGRX/BGRA.
func NewReceiver(src Source, recvName string) (*Receiver, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	cname := C.CString(src.Name)
	defer C.free(unsafe.Pointer(cname))
	var curl *C.char
	if src.URL != "" {
		curl = C.CString(src.URL)
		defer C.free(unsafe.Pointer(curl))
	}
	crecv := C.CString(recvName)
	defer C.free(unsafe.Pointer(crecv))

	p := C.ndi_recv_create(cname, curl, crecv)
	if p == nil {
		return nil, errors.New("NDIlib_recv_create_v3 failed")
	}
	return &Receiver{p: p}, nil
}

// Frame is a received video frame. Data points into SDK memory and is valid
// only until the next call to Capture or Close.
type Frame struct {
	Width, Height int
	Stride        int
	Data          []byte
	// Dropped is how many older queued frames Capture skipped to return this one.
	Dropped int
}

// Capture waits up to timeout for a video frame. If more frames are already
// queued when one arrives, the older ones are released and the newest is
// returned, so a slow consumer never falls behind the source. ok is false when
// no frame arrived in time.
func (r *Receiver) Capture(timeout time.Duration) (f Frame, ok bool, err error) {
	r.release()

	var v C.ndi_video_frame_v2_t
	t := C.ndi_recv_capture_video(r.p, &v, C.uint32_t(timeout/time.Millisecond))
	switch t {
	case frameTypeVideo:
	case frameTypeError:
		return Frame{}, false, errors.New("NDI receiver reported a connection error")
	default:
		return Frame{}, false, nil
	}
	dropped := 0
	for {
		var next C.ndi_video_frame_v2_t
		if C.ndi_recv_capture_video(r.p, &next, 0) != frameTypeVideo {
			break
		}
		C.ndi_recv_free_video(r.p, &v)
		v = next
		dropped++
	}
	r.cur, r.have = v, true

	if v.FourCC != fourCCBGRX && v.FourCC != fourCCBGRA {
		return Frame{}, false, fmt.Errorf("unexpected NDI pixel format %08x", uint32(v.FourCC))
	}
	if v.p_data == nil || v.xres <= 0 || v.yres <= 0 || v.line_stride_in_bytes < v.xres*4 {
		return Frame{}, false, nil
	}
	size := int(v.line_stride_in_bytes) * int(v.yres)
	return Frame{
		Width:   int(v.xres),
		Height:  int(v.yres),
		Stride:  int(v.line_stride_in_bytes),
		Data:    unsafe.Slice((*byte)(unsafe.Pointer(v.p_data)), size),
		Dropped: dropped,
	}, true, nil
}

func (r *Receiver) release() {
	if r.have {
		C.ndi_recv_free_video(r.p, &r.cur)
		r.have = false
	}
}

// Connections returns the number of active connections to the source.
func (r *Receiver) Connections() int { return int(C.ndi_recv_connections(r.p)) }

// Close releases the current frame and destroys the receiver. It must not be
// called concurrently with Capture.
func (r *Receiver) Close() {
	if r.p == nil {
		return
	}
	r.release()
	C.ndi_recv_destroy(r.p)
	r.p = nil
}

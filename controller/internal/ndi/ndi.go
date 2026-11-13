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
#include <malloc.h>
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
static const ndi_source_t* (*p_find_get_current_sources)(void*, uint32_t*);
static void* (*p_recv_create_v3)(const ndi_recv_create_v3_t*);
static void (*p_recv_destroy)(void*);
static void (*p_recv_connect)(void*, const ndi_source_t*);
static int (*p_recv_get_no_connections)(void*);
static int (*p_recv_capture_v2)(void*, ndi_video_frame_v2_t*, void*, void*, uint32_t);
static void (*p_recv_free_video_v2)(void*, const ndi_video_frame_v2_t*);
static const char* (*p_version)(void);

// ndi_load opens libndi at path and resolves the functions used here.
// It returns NULL on success or the name of what failed.
static const char* ndi_load(const char* path) {
	void* h = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	if (!h) return dlerror();
#define SYM(var, name) if (!(*(void**)(&var) = dlsym(h, name))) return name;
	SYM(p_initialize, "NDIlib_initialize")
	SYM(p_find_create_v2, "NDIlib_find_create_v2")
	SYM(p_find_get_current_sources, "NDIlib_find_get_current_sources")
	SYM(p_recv_create_v3, "NDIlib_recv_create_v3")
	SYM(p_recv_destroy, "NDIlib_recv_destroy")
	SYM(p_recv_connect, "NDIlib_recv_connect")
	SYM(p_recv_get_no_connections, "NDIlib_recv_get_no_connections")
	SYM(p_recv_capture_v2, "NDIlib_recv_capture_v2")
	SYM(p_recv_free_video_v2, "NDIlib_recv_free_video_v2")
	SYM(p_version, "NDIlib_version")
#undef SYM
	if (!p_initialize()) return "NDIlib_initialize failed (CPU not supported?)";
	return NULL;
}

static void* ndi_find_create(void) {
	ndi_find_create_t c = { true, NULL, NULL };
	return p_find_create_v2(&c);
}
static const ndi_source_t* ndi_find_sources(void* f, uint32_t* n) { return p_find_get_current_sources(f, n); }

// An unconnected receiver for video only (audio and metadata are not requested,
// so the SDK discards them instead of queueing), full bandwidth, progressive
// frames. Color format UYVY_BGRA: frames come as UYVY as the SDK decodes them,
// without the SDK converting the whole picture to BGRX; only sources with alpha
// arrive as BGRA.
static void* ndi_recv_create(const char* recv_name) {
	ndi_recv_create_v3_t c = { { NULL, NULL }, 1, 100, false, recv_name };
	return p_recv_create_v3(&c);
}
static void ndi_recv_destroy(void* r) { p_recv_destroy(r); }
static void ndi_recv_connect(void* r, const char* name, const char* url) {
	ndi_source_t s = { name, url };
	p_recv_connect(r, name ? &s : NULL);
}
static int ndi_recv_connections(void* r) { return p_recv_get_no_connections(r); }
// Returns the frame type: 1 = video, 0 = none within ms, 4 = error.
static int ndi_recv_capture_video(void* r, ndi_video_frame_v2_t* v, uint32_t ms) {
	return p_recv_capture_v2(r, v, NULL, NULL, ms);
}
static void ndi_recv_free_video(void* r, ndi_video_frame_v2_t* v) { p_recv_free_video_v2(r, v); }
static const char* ndi_version(void) { return p_version(); }

// Hand memory freed by libndi (glibc malloc) back to the OS.
static void ndi_malloc_trim(void) { malloc_trim(0); }
*/
import "C"

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// Pixel formats of received frames (FourCC codes).
const (
	FourCCUYVY uint32 = 'U' | 'Y'<<8 | 'V'<<16 | 'Y'<<24
	FourCCBGRX uint32 = 'B' | 'G'<<8 | 'R'<<16 | 'X'<<24
	FourCCBGRA uint32 = 'B' | 'G'<<8 | 'R'<<16 | 'A'<<24
)

var (
	loadOnce sync.Once
	loadErr  error
	version  string
)

// libraryCandidates lists where libndi is looked for, in order: the NDI runtime
// directory named by the SDK's environment variable, the system library path,
// and /usr/local/lib, where "make setup-ndi" installs it (in case the dynamic
// linker was not told about that directory).
func libraryCandidates() []string {
	var c []string
	if dir := os.Getenv("NDI_RUNTIME_DIR_V6"); dir != "" {
		c = append(c, filepath.Join(dir, "libndi.so.6"), filepath.Join(dir, "libndi.so"))
	}
	return append(c, "libndi.so.6", "libndi.so", "/usr/local/lib/libndi.so.6")
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
		loadErr = fmt.Errorf("NDI runtime (libndi) not found; run \"make setup-ndi\" or set NDI_RUNTIME_DIR_V6 to the directory holding libndi.so.6 [%s]",
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

// The finder is created once and kept for the life of the process. Senders are
// announced over mDNS one by one, so a finder needs a few seconds to see all of
// them; a long-lived one already knows them when a source is looked up, and no
// finder instances are created and torn down on every scan.
var (
	finderMu      sync.Mutex
	finder        unsafe.Pointer
	finderCreated time.Time
)

func currentSources() ([]Source, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	finderMu.Lock()
	defer finderMu.Unlock()
	if finder == nil {
		finder = C.ndi_find_create()
		if finder == nil {
			return nil, errors.New("NDIlib_find_create_v2 failed")
		}
		finderCreated = time.Now()
	}
	var n C.uint32_t
	p := C.ndi_find_sources(finder, &n)
	if p == nil || n == 0 {
		return nil, nil
	}
	list := unsafe.Slice(p, int(n))
	out := make([]Source, 0, len(list))
	for _, s := range list {
		out = append(out, Source{Name: C.GoString(s.p_ndi_name), URL: C.GoString(s.p_url_address)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func finderAge() time.Duration {
	finderMu.Lock()
	defer finderMu.Unlock()
	if finder == nil {
		return 0
	}
	return time.Since(finderCreated)
}

// Discover returns the NDI sources on the network. Right after the finder is
// created it waits until the list stops changing for settle (or timeout
// elapses), since senders show up one by one; afterwards the finder keeps the
// list current and it is returned at once.
func Discover(timeout, settle time.Duration) ([]Source, error) {
	last, err := currentSources()
	if err != nil {
		return nil, err
	}
	end := time.Now().Add(timeout - finderAge())
	lastChange := time.Now()
	for time.Now().Before(end) {
		time.Sleep(100 * time.Millisecond)
		cur, err := currentSources()
		if err != nil {
			return nil, err
		}
		if !sameSources(cur, last) {
			last, lastChange = cur, time.Now()
		} else if len(cur) > 0 && time.Since(lastChange) >= settle {
			break
		}
	}
	return last, nil
}

func sameSources(a, b []Source) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// LookupSource returns the source matching name among those currently known,
// without waiting: a source whose name equals name, or whose stream part
// ("MACHINE (Stream)" -> "Stream") equals it, or that contains it.
func LookupSource(name string) (Source, bool, error) {
	list, err := currentSources()
	if err != nil {
		return Source{}, false, err
	}
	s, ok := matchSource(list, name)
	return s, ok, nil
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

// Receiver receives video from one NDI source at a time. Capture waits inside
// the SDK for the next frame and returns the moment it arrives; if frames have
// queued up meanwhile, the older ones are released at once and only the newest
// is returned, so the queue stays short however the consumer is paced.
//
// A receiver is meant to be reused: Connect switches it to another source, and
// Release returns it to a pool. Creating and destroying receivers repeatedly
// costs native memory, and destroying one whose session died blocks for many
// seconds, so that is only ever done off the caller's goroutine (Discard).
//
// A Receiver is not safe for concurrent use.
type Receiver struct {
	recv unsafe.Pointer
	cur  C.ndi_video_frame_v2_t
	have bool
}

// Frame is a received video frame. Data points into SDK memory and is valid
// only until the next call to Capture, Connect or Release.
type Frame struct {
	Width, Height int
	Stride        int
	FourCC        uint32 // FourCCUYVY, or FourCCBGRA/FourCCBGRX for sources with alpha
	Data          []byte
}

// NewReceiver creates an unconnected receiver.
func NewReceiver(recvName string) (*Receiver, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	crecv := C.CString(recvName)
	defer C.free(unsafe.Pointer(crecv))
	recv := C.ndi_recv_create(crecv)
	if recv == nil {
		return nil, errors.New("NDIlib_recv_create_v3 failed")
	}
	return &Receiver{recv: recv}, nil
}

// Connect switches the receiver to src. Frames still queued from a previous
// source are dropped.
func (r *Receiver) Connect(src Source) {
	r.release()
	cname := C.CString(src.Name)
	defer C.free(unsafe.Pointer(cname))
	var curl *C.char
	if src.URL != "" {
		curl = C.CString(src.URL)
		defer C.free(unsafe.Pointer(curl))
	}
	C.ndi_recv_connect(r.recv, cname, curl)
	r.drain()
}

// disconnect drops the connection and any queued frames. It is quick on a
// live session but can block for many seconds on one that died, so only
// Release calls it, in the background, and only for receivers that were
// delivering frames.
func (r *Receiver) disconnect() {
	r.release()
	C.ndi_recv_connect(r.recv, nil, nil)
	r.drain()
}

func (r *Receiver) drain() {
	var v C.ndi_video_frame_v2_t
	for C.ndi_recv_capture_video(r.recv, &v, 0) == frameTypeVideo {
		C.ndi_recv_free_video(r.recv, &v)
	}
}

const (
	frameTypeVideo = 1
	frameTypeError = 4
)

// ErrConnection is returned by Capture when the SDK reports a connection error.
var ErrConnection = errors.New("NDI receiver reported a connection error")

// Capture waits up to timeout for a video frame and returns the newest one
// available. ok is false when none arrived in time. Dropped is how many older
// queued frames were skipped.
func (r *Receiver) Capture(timeout time.Duration) (f Frame, dropped int, ok bool, err error) {
	r.release()
	var v C.ndi_video_frame_v2_t
	switch C.ndi_recv_capture_video(r.recv, &v, C.uint32_t(timeout/time.Millisecond)) {
	case frameTypeVideo:
	case frameTypeError:
		return Frame{}, 0, false, ErrConnection
	default:
		return Frame{}, 0, false, nil
	}
	for {
		var next C.ndi_video_frame_v2_t
		if C.ndi_recv_capture_video(r.recv, &next, 0) != frameTypeVideo {
			break
		}
		C.ndi_recv_free_video(r.recv, &v)
		v = next
		dropped++
	}
	r.cur, r.have = v, true
	f, ok = frameOf(&r.cur)
	return f, dropped, ok, nil
}

// frameOf describes a received frame, or returns false for one this package
// does not handle (unknown pixel format, inconsistent stride).
func frameOf(v *C.ndi_video_frame_v2_t) (Frame, bool) {
	if v.p_data == nil || v.xres <= 0 || v.yres <= 0 {
		return Frame{}, false
	}
	fourcc := uint32(v.FourCC)
	minStride := int(v.xres) * 4
	if fourcc == FourCCUYVY {
		minStride = (int(v.xres) + 1) / 2 * 4
	} else if fourcc != FourCCBGRX && fourcc != FourCCBGRA {
		return Frame{}, false
	}
	if int(v.line_stride_in_bytes) < minStride {
		return Frame{}, false
	}
	return Frame{
		Width:  int(v.xres),
		Height: int(v.yres),
		Stride: int(v.line_stride_in_bytes),
		FourCC: fourcc,
		Data:   unsafe.Slice((*byte)(unsafe.Pointer(v.p_data)), int(v.line_stride_in_bytes)*int(v.yres)),
	}, true
}

func (r *Receiver) release() {
	if r.have {
		C.ndi_recv_free_video(r.recv, &r.cur)
		r.have = false
	}
}

// Connections returns the number of active connections to the source. A dead
// session can keep reporting a connection for minutes after a network outage.
func (r *Receiver) Connections() int { return int(C.ndi_recv_connections(r.recv)) }

func (r *Receiver) destroy() {
	r.release()
	C.ndi_recv_destroy(r.recv)
	r.recv = nil
}

// Receivers no longer in use are handed back here. One disconnected receiver
// is kept for reuse; the rest are destroyed in the background.
var (
	poolMu     sync.Mutex
	idle       *Receiver
	discarding int // receivers being destroyed right now
)

// AcquireReceiver returns an idle receiver from the pool, or a new one.
func AcquireReceiver(recvName string) (*Receiver, error) {
	poolMu.Lock()
	r := idle
	idle = nil
	poolMu.Unlock()
	if r != nil {
		return r, nil
	}
	return NewReceiver(recvName)
}

// Release disconnects a receiver that was delivering frames (cheap on a live
// session) in the background and keeps it for reuse. Use Discard instead for a
// receiver whose session stalled.
func Release(r *Receiver) {
	go func() {
		r.disconnect()
		poolMu.Lock()
		if idle == nil {
			idle, r = r, nil
		}
		poolMu.Unlock()
		if r != nil {
			destroyAndTrim(r)
		}
	}()
}

// Discard destroys a receiver in the background. Tearing down a session that
// died (no frames although NDI still reports a connection, or a sender that
// went away) blocks inside the SDK for 10 s or more, so it must never happen
// on a goroutine that anything waits for.
func Discard(r *Receiver) {
	poolMu.Lock()
	discarding++
	n := discarding
	poolMu.Unlock()
	if n > 4 {
		log.Printf("[NDI] %d stalled receivers are still being torn down", n)
	}
	go func() {
		destroyAndTrim(r)
		poolMu.Lock()
		discarding--
		poolMu.Unlock()
	}()
}

func destroyAndTrim(r *Receiver) {
	start := time.Now()
	r.destroy()
	TrimMemory()
	if d := time.Since(start); d > time.Second {
		log.Printf("[NDI] Released a receiver in %.1fs", d.Seconds())
	}
}

// TrimMemory returns heap memory that libndi freed (frame buffers of destroyed
// receivers) to the OS, so the process does not keep it resident.
func TrimMemory() { C.ndi_malloc_trim() }

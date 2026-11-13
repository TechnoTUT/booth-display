// ndi-stamp-sender: NDI test sender for TestNDIMixerLatency
// (controller/internal/pipeline/ndi_latency_test.go).
//
// Sends 1920x1080 frames (UYVY, or BGRX with "bgrx") over NDI. A row of 48
// black/white blocks, 40x60 px at rows 510-569, carries the send time: bit i
// (block i, white = 1) is bit i of wall-clock milliseconds mod 2^32 for
// i < 32, and of the frame counter for i >= 32. The blocks sit in the middle
// of the frame, so they survive the center crop onto the canvas.
//
// It uses the system libndi (make setup-ndi), loaded at runtime like the
// controller does, so it builds without the NDI SDK headers:
//
//   make ndi-stamp-sender
//   tools/ndi-stamp-sender/ndi-stamp-sender [name] [fps] [seconds, 0 = forever] [bgrx]
//
// Defaults: name "booth-latency-test", 30 fps, forever, UYVY.
#include <dlfcn.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

typedef struct { const char* name; const char* groups; bool clock_video, clock_audio; } send_create_t;
typedef struct {
	int xres, yres; uint32_t FourCC; int fr_N, fr_D; float aspect; int format;
	int64_t timecode; uint8_t* p_data; int stride; const char* meta; int64_t timestamp;
} video_frame_t;

#define FOURCC(a,b,c,d) ((uint32_t)(a) | (uint32_t)(b)<<8 | (uint32_t)(c)<<16 | (uint32_t)(d)<<24)

static double now_s(void) { struct timespec t; clock_gettime(CLOCK_MONOTONIC, &t); return t.tv_sec + t.tv_nsec / 1e9; }
static uint64_t wall_ms(void) { struct timespec t; clock_gettime(CLOCK_REALTIME, &t); return (uint64_t)t.tv_sec * 1000 + t.tv_nsec / 1000000; }

int main(int argc, char** argv) {
	const char* name = argc > 1 ? argv[1] : "booth-latency-test";
	double fps = argc > 2 ? atof(argv[2]) : 30.0;
	double secs = argc > 3 ? atof(argv[3]) : 0;
	bool bgrx = argc > 4 && strcmp(argv[4], "bgrx") == 0;

	void* h = dlopen("libndi.so.6", RTLD_NOW);
	if (!h) { fprintf(stderr, "%s\n", dlerror()); return 1; }
	bool (*init)(void) = dlsym(h, "NDIlib_initialize");
	void* (*create)(const send_create_t*) = dlsym(h, "NDIlib_send_create");
	void (*send)(void*, const video_frame_t*) = dlsym(h, "NDIlib_send_send_video_v2");
	void (*destroy)(void*) = dlsym(h, "NDIlib_send_destroy");
	if (!init || !create || !send || !destroy || !init()) { fprintf(stderr, "libndi init failed\n"); return 1; }

	send_create_t c = { name, NULL, false, false };
	void* s = create(&c);
	if (!s) { fprintf(stderr, "send_create failed\n"); return 1; }

	const int W = 1920, H = 1080;
	int stride = bgrx ? W * 4 : W * 2;
	uint8_t* buf = malloc((size_t)stride * H);
	// background: gray gradient
	for (int y = 0; y < H; y++)
		for (int x = 0; x < W; x++) {
			uint8_t l = 16 + (uint8_t)((x + y) % 200);
			if (bgrx) { uint8_t* p = buf + y * stride + x * 4; p[0] = p[1] = p[2] = l; p[3] = 255; }
			else { uint8_t* p = buf + y * stride + x * 2; p[0] = 128; p[1] = l; }
		}
	video_frame_t f = { W, H, bgrx ? FOURCC('B','G','R','X') : FOURCC('U','Y','V','Y'), (int)(fps * 1000 + 0.5), 1000,
		16.0f / 9.0f, 1, INT64_MAX /* synthesize */, buf, stride, NULL, 0 };
	fprintf(stderr, "sending %s as %s at %.2f fps\n", bgrx ? "BGRX" : "UYVY", name, fps);

	double start = now_s();
	for (uint64_t n = 0; secs == 0 || now_s() - start < secs; n++) {
		double target = start + n / fps, d = target - now_s();
		if (d > 0) { struct timespec ts = { (time_t)d, (long)((d - (time_t)d) * 1e9) }; nanosleep(&ts, NULL); }
		uint64_t v = (wall_ms() & 0xFFFFFFFFu) | ((n & 0xFFFF) << 32);
		for (int i = 0; i < 48; i++) {
			bool on = (v >> i) & 1;
			for (int y = 510; y < 570; y++)
				for (int x = i * 40; x < (i + 1) * 40; x++) {
					if (bgrx) { uint8_t* p = buf + y * stride + x * 4; p[0] = p[1] = p[2] = on ? 255 : 0; }
					else { uint8_t* p = buf + y * stride + x * 2; p[0] = 128; p[1] = on ? 235 : 16; }
				}
		}
		send(s, &f);
	}
	destroy(s);
	return 0;
}

package com.technotut.boothdisplay.decoder

import android.media.MediaCodec
import android.media.MediaFormat
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.Process
import android.util.Log
import android.view.Choreographer
import android.view.Surface
import com.technotut.boothdisplay.protocol.ClockSync
import com.technotut.boothdisplay.protocol.Frame
import com.technotut.boothdisplay.protocol.FramePool
import com.technotut.boothdisplay.protocol.StreamStats

/**
 * H.264 hardware decoder (MediaCodec, asynchronous mode) rendering straight
 * onto a Surface.
 *
 * The receive thread hands over complete access units with [submit]. Codec
 * callbacks run on their own thread: an access unit goes into the first free
 * input buffer, and every decoded picture is rendered the moment it comes
 * out. Nothing waits with a timeout or polls.
 *
 * Each picture is rendered at the vsync it would get anyway, unless the
 * previous picture already has that vsync; then it goes to the next one
 * ([VsyncPacer]). Without this SurfaceFlinger drops one of two pictures
 * that arrive within one refresh, about a fifth of them at 60 fps.
 *
 * At most [MAX_PENDING] access units wait for an input buffer. Beyond that
 * the decoder is falling behind, so the waiting ones are discarded and the
 * receiver waits for a keyframe, instead of showing the picture later and
 * later.
 */
class HardwareH264Decoder(
    private val width: Int,
    private val height: Int,
    private val pool: FramePool,
    private val stats: StreamStats,
    private val clock: ClockSync,
) {
    companion object {
        private const val TAG = "HardwareH264Decoder"
        private const val MIME_TYPE = MediaFormat.MIMETYPE_VIDEO_AVC
        const val MAX_PENDING = 3

        /** Decode-speed hint: well above 30 fps so the codec runs at full clock. */
        private const val OPERATING_RATE = 120

        /** Rockchip vendor key (their libstagefright) for immediate decoder output. */
        private const val KEY_RK_IMMEDIATE_OUT = "rk-immediate-out"

        /** Plausible refresh periods (30 to 120 Hz) for the vsync estimate. */
        private const val MIN_PERIOD_NS = 8_000_000L
        private const val MAX_PERIOD_NS = 34_000_000L

        /** Frames in flight inside the codec that can be traced back. */
        private const val META_SLOTS = 32
    }

    private val lock = Any()

    // Guarded by lock.
    private var session: Session? = null
    private val pending = ArrayDeque<Frame>(MAX_PENDING + 1)

    @Volatile
    var renderedFrameCount: Long = 0L
        private set

    @Volatile
    var lastError: String? = null
        private set

    /** The configured size and codec, for the OSD. */
    @Volatile
    var description: String = "-"
        private set

    /**
     * One configured codec and its callback thread. Everything but [pending]
     * belongs to the callback thread; a session that is no longer current
     * does nothing, so a stopping codec cannot touch its successor.
     */
    private inner class Session(val codec: MediaCodec) : MediaCodec.Callback(), Choreographer.FrameCallback {
        val thread = HandlerThread("DecoderCallbacks", Process.THREAD_PRIORITY_URGENT_DISPLAY).apply { start() }
        val handler = Handler(thread.looper)
        val feedRunnable = Runnable { feed() }

        // Vsync tracking, on the callback thread like the codec callbacks.
        private val pacer = VsyncPacer()
        private var choreographer: Choreographer? = null
        private var lastFrameTimeNs = 0L
        private var periodNs = 0L
        @Volatile
        private var stopped = false

        fun startVsync() {
            handler.post {
                choreographer = Choreographer.getInstance().also { it.postFrameCallback(this) }
            }
        }

        override fun doFrame(frameTimeNanos: Long) {
            if (stopped) return
            val d = frameTimeNanos - lastFrameTimeNs
            // Consecutive callbacks are one refresh apart; a missed one shows
            // as a longer gap, so keep the shortest plausible interval, eased
            // towards new readings to follow small drifts.
            if (lastFrameTimeNs > 0 && d in MIN_PERIOD_NS..MAX_PERIOD_NS) {
                periodNs = if (periodNs == 0L || d < periodNs) d else periodNs + (d - periodNs) / 64
            }
            lastFrameTimeNs = frameTimeNanos
            if (periodNs > 0) pacer.onVsync(frameTimeNanos, periodNs)
            choreographer?.postFrameCallback(this)
        }

        private val freeInputs = ArrayDeque<Int>(16)
        private val metaPts = LongArray(META_SLOTS) { -1L }
        private val metaComplete = LongArray(META_SLOTS)
        private val metaQueued = LongArray(META_SLOTS)
        private val metaSenderTs = LongArray(META_SLOTS)
        private var metaNext = 0
        private var lastPtsUs = 0L

        override fun onInputBufferAvailable(mc: MediaCodec, index: Int) {
            freeInputs.addLast(index)
            feed()
        }

        override fun onOutputBufferAvailable(mc: MediaCodec, index: Int, info: MediaCodec.BufferInfo) {
            val now = System.nanoTime()
            try {
                if (pacer.hasVsync) {
                    mc.releaseOutputBuffer(index, pacer.presentTime(now))
                } else {
                    mc.releaseOutputBuffer(index, true)
                }
                stats.paced(pacer.delayed, pacer.resets)
            } catch (e: IllegalStateException) {
                return // released while stopping
            }
            renderedFrameCount++
            for (slot in 0 until META_SLOTS) {
                if (metaPts[slot] != info.presentationTimeUs) continue
                val e2e = clock.elapsedSince(metaSenderTs[slot], now / 1000L)
                stats.frameDecoded(now, metaComplete[slot], metaQueued[slot], e2e)
                metaPts[slot] = -1L
                break
            }
        }

        override fun onError(mc: MediaCodec, e: MediaCodec.CodecException) {
            lastError = "Codec error: ${e.diagnosticInfo}"
            Log.e(TAG, "MediaCodec error (recoverable=${e.isRecoverable}, transient=${e.isTransient})", e)
        }

        override fun onOutputFormatChanged(mc: MediaCodec, format: MediaFormat) {
            Log.i(TAG, "Output format: $format")
        }

        /** Moves waiting access units into free input buffers. */
        fun feed() {
            while (true) {
                val index = freeInputs.firstOrNull() ?: return
                val frame = synchronized(lock) {
                    if (session !== this) return
                    pending.removeFirstOrNull()
                } ?: return
                try {
                    val buf = codec.getInputBuffer(index)
                    if (buf == null || frame.size > buf.capacity()) {
                        lastError = "Frame too big: ${frame.size} > ${buf?.capacity()}"
                        Log.e(TAG, lastError!!)
                        continue
                    }
                    freeInputs.removeFirst()
                    buf.clear()
                    buf.put(frame.data, 0, frame.size)
                    // Strictly increasing timestamps; they also find the frame again on output.
                    val pts = maxOf(frame.completeNs / 1000L, lastPtsUs + 1)
                    lastPtsUs = pts
                    val slot = metaNext
                    metaNext = (metaNext + 1) % META_SLOTS
                    metaPts[slot] = pts
                    metaComplete[slot] = frame.completeNs
                    metaQueued[slot] = System.nanoTime()
                    metaSenderTs[slot] = frame.senderTimestamp
                    val flags = if (frame.isKeyframe) MediaCodec.BUFFER_FLAG_KEY_FRAME else 0
                    codec.queueInputBuffer(index, 0, frame.size, pts, flags)
                } catch (e: IllegalStateException) {
                    return // stopped meanwhile
                } finally {
                    pool.release(frame)
                }
            }
        }

        fun release() {
            stopped = true
            try {
                codec.stop()
            } catch (e: Exception) {
                Log.w(TAG, "Error stopping decoder: ${e.message}")
            }
            codec.release()
            thread.quitSafely()
        }
    }

    fun init(surface: Surface) {
        synchronized(lock) {
            stopLocked()
            var s: Session? = null
            try {
                s = Session(MediaCodec.createDecoderByType(MIME_TYPE))
                configure(s, surface)
                session = s
                renderedFrameCount = 0L
                lastError = null
                s.codec.start()
                s.startVsync()
                Log.i(TAG, "Decoder started: $description")
            } catch (e: Exception) {
                lastError = "Codec init fail: ${e.message}"
                Log.e(TAG, "Failed to start MediaCodec: ${e.message}", e)
                session = null
                s?.release()
            }
        }
    }

    /**
     * Tries the low-latency hints first, then a plain format, then the
     * 16-aligned height (540 is not a multiple of 16) for decoders that need it.
     */
    private fun configure(s: Session, surface: Surface) {
        val attempts = listOf(
            Triple(width, height, true),
            Triple(width, height, false),
            Triple(width, (height + 15) / 16 * 16, false),
        )
        var last: Exception? = null
        for ((w, h, hints) in attempts) {
            try {
                s.codec.setCallback(s, s.handler)
                s.codec.configure(format(w, h, hints), surface, null, 0)
                description = "${s.codec.name} ${w}x$h" + if (hints) " (low-latency hints)" else ""
                return
            } catch (e: Exception) {
                Log.w(TAG, "configure ${w}x$h (hints=$hints) failed: ${e.message}")
                last = e
                s.codec.reset()
            }
        }
        throw last!!
    }

    private fun format(w: Int, h: Int, hints: Boolean) = MediaFormat.createVideoFormat(MIME_TYPE, w, h).apply {
        setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, pool.frameCapacity)
        // Rockchip's decoder otherwise holds each IDR and the frame after it
        // until the next input arrives (measured: +30 ms each at 30 fps, every
        // keyframe). This vendor key turns on its immediate output mode
        // (VPU_API_SET_IMMEDIATE_OUT); other codecs ignore unknown keys. Set in
        // every attempt, since the fallbacks need it just as much.
        setInteger(KEY_RK_IMMEDIATE_OUT, 1)
        if (!hints) return@apply
        // Realtime priority and a high operating rate keep the codec clocked up.
        setInteger(MediaFormat.KEY_PRIORITY, 0)
        setInteger(MediaFormat.KEY_OPERATING_RATE, OPERATING_RATE)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            setInteger(MediaFormat.KEY_LOW_LATENCY, 1)
        }
    }

    /**
     * Takes a complete access unit (from the receive thread); the frame goes
     * back to the pool once it is in the codec or discarded. Returns false
     * when the decoder was too far behind: the waiting access units and this
     * one were discarded and the caller must wait for a keyframe. A keyframe
     * is always taken, replacing whatever was waiting.
     */
    fun submit(frame: Frame): Boolean {
        synchronized(lock) {
            val s = session
            if (s == null) {
                pool.release(frame)
                return true
            }
            if (pending.size >= MAX_PENDING) {
                stats.backlogDropped(pending.size)
                while (pending.isNotEmpty()) pool.release(pending.removeFirst())
                if (!frame.isKeyframe) {
                    stats.backlogDropped(1)
                    pool.release(frame)
                    return false
                }
            }
            pending.addLast(frame)
            s.handler.post(s.feedRunnable)
            return true
        }
    }

    fun stop() {
        synchronized(lock) { stopLocked() }
    }

    private fun stopLocked() {
        session?.release()
        session = null
        while (pending.isNotEmpty()) pool.release(pending.removeFirst())
    }
}

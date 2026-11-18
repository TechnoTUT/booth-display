package com.technotut.boothdisplay.protocol

/**
 * Per-second latency and smoothness figures for the OSD and logcat.
 *
 * Stages of a frame (milliseconds):
 * - net: first fragment to last fragment received
 * - wait: frame complete to handed to the decoder (waiting for an input buffer)
 * - decode: handed to the decoder to decoded picture out
 * - e2e: canvas frame reaching the controller's encoder to decoded picture
 *   out on this display, through the PING/PONG clock offset. Rendering (the
 *   next vsync) and the panel come on top.
 *
 * A stall is a gap of more than [STALL_MS] between two decoded pictures,
 * which the viewer sees as a stutter.
 */
class StreamStats {
    class Samples(capacity: Int = 512) {
        private val values = FloatArray(capacity)
        var count = 0
            private set

        fun add(v: Float) {
            if (count < values.size) values[count++] = v
        }

        /** Median, 90th percentile and maximum, or null when empty. */
        fun summary(): FloatArray? {
            if (count == 0) return null
            val s = values.copyOf(count)
            s.sort()
            return floatArrayOf(s[count / 2], s[(count * 9) / 10], s[count - 1])
        }

        fun clear() {
            count = 0
        }
    }

    data class Snapshot(
        val fps: Float,
        val net: FloatArray?,
        val wait: FloatArray?,
        val decode: FloatArray?,
        val e2e: FloatArray?,
        val stalls: Int,
        val lostTotal: Long,
        val skippedTotal: Long,
        val keyframeRequestsTotal: Long,
        val backlogDropsTotal: Long,
        val rttMs: Float?,
        val pacedTotal: Long = 0,
        val paceResetsTotal: Long = 0,
    ) {
        private fun fmt(s: FloatArray?) =
            if (s == null) "-" else String.format(java.util.Locale.ROOT, "%.1f/%.1f/%.1f", s[0], s[1], s[2])

        /** One line for logcat: stages as median/p90/max in ms. */
        fun logLine(): String = String.format(
            java.util.Locale.ROOT,
            "fps=%.1f e2e=%s net=%s wait=%s dec=%s stalls=%d lost=%d skipped=%d kfreq=%d backlog=%d rtt=%s paced=%d/%d",
            fps, fmt(e2e), fmt(net), fmt(wait), fmt(decode), stalls, lostTotal, skippedTotal,
            keyframeRequestsTotal, backlogDropsTotal,
            if (rttMs == null) "-" else String.format(java.util.Locale.ROOT, "%.2fms", rttMs),
            pacedTotal, paceResetsTotal,
        )
    }

    private val net = Samples()
    private val wait = Samples()
    private val decode = Samples()
    private val e2e = Samples()
    private var frames = 0
    private var stalls = 0
    private var lastOutputNs = 0L
    private var windowStartNs = -1L

    private var lost = 0L
    private var skipped = 0L
    private var keyframeRequests = 0L
    private var backlogDrops = 0L
    private var paced = 0L
    private var paceResets = 0L

    @Synchronized
    fun frameReceived(frame: Frame) {
        net.add((frame.completeNs - frame.firstPacketNs) / 1e6f)
    }

    /** A decoded picture came out at [nowNs]; [e2eUs] is null before clock sync. */
    @Synchronized
    fun frameDecoded(nowNs: Long, completeNs: Long, queuedNs: Long, e2eUs: Long?) {
        wait.add((queuedNs - completeNs) / 1e6f)
        decode.add((nowNs - queuedNs) / 1e6f)
        if (e2eUs != null) e2e.add(e2eUs / 1e3f)
        if (lastOutputNs != 0L && nowNs - lastOutputNs > STALL_MS * 1_000_000L) stalls++
        lastOutputNs = nowNs
        frames++
    }

    /** Totals from the vsync pacer: pictures moved to a later vsync, and schedule restarts. */
    @Synchronized
    fun paced(delayedTotal: Long, resetsTotal: Long) {
        paced = delayedTotal
        paceResets = resetsTotal
    }

    @Synchronized
    fun loss() {
        lost++
    }

    @Synchronized
    fun skipped() {
        skipped++
    }

    @Synchronized
    fun keyframeRequested() {
        keyframeRequests++
    }

    @Synchronized
    fun backlogDropped(frames: Int) {
        backlogDrops += frames
    }

    /** Figures since the previous call. */
    @Synchronized
    fun snapshot(nowNs: Long, rttUs: Long): Snapshot {
        val elapsed = if (windowStartNs < 0) 0L else nowNs - windowStartNs
        val fps = if (elapsed > 0) frames * 1e9f / elapsed else 0f
        val s = Snapshot(
            fps, net.summary(), wait.summary(), decode.summary(), e2e.summary(), stalls,
            lost, skipped, keyframeRequests, backlogDrops,
            if (rttUs < 0) null else rttUs / 1e3f,
            paced, paceResets,
        )
        net.clear(); wait.clear(); decode.clear(); e2e.clear()
        frames = 0
        stalls = 0
        windowStartNs = nowNs
        return s
    }

    companion object {
        /** 1.5 frame intervals at 30 fps. */
        const val STALL_MS = 50L
    }
}

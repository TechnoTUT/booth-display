package com.technotut.boothdisplay.protocol

/**
 * Offset between the controller clock and this display's clock, from PING/PONG
 * (docs/streaming-protocol.md). Controller timestamps are 32-bit microseconds
 * that wrap, so all arithmetic is modulo 2^32.
 *
 * Keeps the offset of the PONG with the shortest round trip among the last
 * [window], since a short round trip bounds the error best.
 */
class ClockSync(private val window: Int = 8) {
    private val rtts = LongArray(window)
    private val offsets = LongArray(window)
    private var count = 0
    private var next = 0

    /** Controller clock minus display clock, in microseconds (mod 2^32). */
    @Volatile
    var offsetUs = 0L
        private set

    /** Round-trip time of the PONG the offset comes from, -1 before the first. */
    @Volatile
    var rttUs = -1L
        private set

    /**
     * [t0]: the PING timestamp the PONG echoes (display clock, 32 bits).
     * [t1]: the PONG timestamp (controller clock). [t2]: display clock now.
     */
    @Synchronized
    fun onPong(t0: Long, t1: Long, t2: Long) {
        val rtt = (t2 - t0).toInt().toLong()
        if (rtt < 0 || rtt > MAX_RTT_US) return
        rtts[next] = rtt
        offsets[next] = t1 - t2 + rtt / 2
        next = (next + 1) % window
        if (count < window) count++
        var best = 0
        for (i in 1 until count) if (rtts[i] < rtts[best]) best = i
        offsetUs = offsets[best]
        rttUs = rtts[best]
    }

    /**
     * Microseconds from controller time [senderTimestamp] to display time
     * [displayUs], or null before the first PONG.
     */
    fun elapsedSince(senderTimestamp: Long, displayUs: Long): Long? {
        if (rttUs < 0) return null
        return (displayUs + offsetUs - senderTimestamp).toInt().toLong()
    }

    companion object {
        /** PONGs slower than this say nothing useful about the offset. */
        const val MAX_RTT_US = 1_000_000L
    }
}

package com.technotut.boothdisplay.decoder

/**
 * Gives each decoded picture its own vsync.
 *
 * A picture released for rendering "now" is latched by SurfaceFlinger at
 * the next vsync. When two pictures arrive within one vsync interval (the
 * network and the decoder both jitter by several milliseconds), the older is
 * dropped and the next vsync has nothing new: at 60 fps about a fifth of the
 * pictures never reached the screen. The pacer finds the vsync a picture
 * would get anyway and, if the previous picture already has it, moves it to
 * the following one. The result is a present timestamp for
 * MediaCodec.releaseOutputBuffer(index, timestampNs).
 *
 * A picture is never moved more than [maxDelaySlots] vsyncs past its own;
 * beyond that the schedule starts over from it, so the delay cannot build up.
 *
 * Times are System.nanoTime(). Used from one thread only.
 */
class VsyncPacer(
    private val latchMarginNs: Long = 1_000_000L,
    private val maxDelaySlots: Long = 2,
) {
    private var vsyncNs = 0L
    private var periodNs = 0L

    /** Present time given to the previous picture, 0 for none. */
    private var lastPresentNs = 0L

    /** Pictures moved to a later vsync since the start. */
    var delayed = 0L
        private set

    /** Times the schedule started over because it fell too far behind. */
    var resets = 0L
        private set

    val hasVsync get() = periodNs > 0

    /** A vsync happened at [frameTimeNs]; [refreshPeriodNs] is the display's frame period. */
    fun onVsync(frameTimeNs: Long, refreshPeriodNs: Long) {
        vsyncNs = frameTimeNs
        periodNs = refreshPeriodNs
    }

    /**
     * The present timestamp for a picture released at [nowNs], or [nowNs]
     * itself before the first vsync is known.
     */
    fun presentTime(nowNs: Long): Long {
        if (periodNs <= 0) return nowNs
        // Slots count vsyncs from the last known one; the picture is latched
        // at the first vsync it is queued for.
        val natural = Math.floorDiv(nowNs - latchMarginNs - vsyncNs + periodNs - 1, periodNs)
        var slot = natural
        // The previous picture's slot, counted from the current reference vsync.
        val lastSlot = if (lastPresentNs == 0L) Long.MIN_VALUE
            else Math.floorDiv(lastPresentNs - vsyncNs, periodNs)
        if (lastSlot != Long.MIN_VALUE && slot <= lastSlot) {
            if (lastSlot + 1 - natural > maxDelaySlots) {
                resets++
            } else {
                slot = lastSlot + 1
                delayed++
            }
        }
        // Half a period before the vsync after the latch: eligible at this
        // slot's latch, not at the one before.
        lastPresentNs = vsyncNs + slot * periodNs + periodNs / 2
        return lastPresentNs
    }
}

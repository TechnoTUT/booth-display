package com.technotut.boothdisplay.decoder

import org.junit.Assert.assertEquals
import org.junit.Test

class VsyncPacerTest {
    private val period = 16_666_667L
    private val v0 = 1_000_000_000L
    private val ms = 1_000_000L

    /** The vsync slot a present time is eligible at: v_slot < t <= v_slot+1. */
    private fun slotOf(t: Long) = Math.floorDiv(t - v0 - 1, period)

    private fun pacer() = VsyncPacer(latchMarginNs = 1 * ms, maxDelaySlots = 2).apply { onVsync(v0, period) }

    @Test
    fun beforeTheFirstVsyncRendersNow() {
        val p = VsyncPacer()
        assertEquals(123L, p.presentTime(123L))
    }

    @Test
    fun keepsTheNaturalVsyncWhenFramesAreSpreadOut() {
        val p = pacer()
        // One picture every period, 5 ms after each vsync.
        for (k in 1..5) {
            val t = p.presentTime(v0 + (k - 1) * period + 5 * ms)
            assertEquals(k.toLong(), slotOf(t))
        }
        assertEquals(0L, p.delayed)
    }

    @Test
    fun movesTheSecondOfTwoPicturesInOneIntervalToTheNextVsync() {
        val p = pacer()
        val a = p.presentTime(v0 + 3 * ms)
        val b = p.presentTime(v0 + 12 * ms) // same interval
        val c = p.presentTime(v0 + 2 * period + 5 * ms) // its own interval, after b's slot
        assertEquals(1L, slotOf(a))
        assertEquals(2L, slotOf(b))
        assertEquals(3L, slotOf(c))
        assertEquals(1L, p.delayed)
    }

    @Test
    fun aPictureJustBeforeTheLatchKeepsThatVsync() {
        val p = pacer()
        // Queued 2 ms before vsync 1 (margin 1 ms): latched there.
        assertEquals(1L, slotOf(p.presentTime(v0 + period - 2 * ms)))
        // Queued after vsync 1 minus the margin: the next one.
        assertEquals(2L, slotOf(p.presentTime(v0 + period - ms / 2)))
    }

    @Test
    fun startsOverInsteadOfFallingFurtherBehind() {
        val p = pacer()
        // A burst of four pictures within one interval.
        val slots = (0 until 4).map { slotOf(p.presentTime(v0 + 2 * ms + it * ms)) }
        assertEquals(listOf(1L, 2L, 3L, 1L), slots)
        assertEquals(2L, p.delayed)
        assertEquals(1L, p.resets)
    }

    @Test
    fun followsALaterVsyncReference() {
        val p = pacer()
        // A picture just before vsync 1, then vsync 1 becomes the reference.
        assertEquals(1L, slotOf(p.presentTime(v0 + 14 * ms)))
        val v1 = v0 + period
        p.onVsync(v1, period)
        // Right after vsync 1 the next latch is vsync 2, which is free.
        assertEquals(2L, slotOf(p.presentTime(v1 + 3 * ms)))
        assertEquals(0L, p.delayed)
        // A second picture in the same interval moves on, across the new reference.
        assertEquals(3L, slotOf(p.presentTime(v1 + 8 * ms)))
        assertEquals(1L, p.delayed)
    }
}

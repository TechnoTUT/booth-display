package com.technotut.boothdisplay.protocol

/**
 * Reassembles video packets into access units and detects losses
 * (docs/streaming-protocol.md, "Rules for the display").
 *
 * A frame is identified by the sequence number of its first fragment
 * (Sequence Number - Fragment Index). Every fragment but the last is
 * [PacketHeader.MAX_PAYLOAD] bytes, so each one is copied straight to its
 * place in the frame buffer as it arrives, in any order.
 *
 * A loss is reported when a frame is abandoned incomplete (a newer frame
 * starts first), when the sequence numbers between two frames have a gap,
 * or when a frame cannot be held. The caller then waits for a keyframe.
 *
 * Used from the receive thread only.
 */
class FrameAssembler(
    private val pool: FramePool,
    private val onFrame: (Frame) -> Unit,
    private val onLoss: () -> Unit,
) {
    private var current: Frame? = null
    private var currentBase = 0L
    private var currentTotal = 0
    private var received = 0
    private val have = BooleanArray(pool.frameCapacity / PacketHeader.MAX_PAYLOAD + 1)

    /** Sequence number the next frame should start at, once a frame has been seen. */
    private var expectedBase = 0L
    private var started = false

    /** Copies one video packet's payload ([offset], [length] in [data]) into its frame. */
    fun onPacket(h: PacketHeader, data: ByteArray, offset: Int, length: Int, nowNs: Long) {
        val base = (h.sequenceNumber - h.fragmentIndex) and 0xFFFFFFFFL
        val total = h.fragmentTotal
        val idx = h.fragmentIndex
        if (total == 0 || idx >= total) return

        if (current == null || base != currentBase) {
            // Fragments of a frame already finished or abandoned arrive late: ignore.
            val reference = if (current != null) currentBase else expectedBase
            if (started && seqDiff(base, reference) < 0) return

            var lost = false
            if (current != null) {
                abandon()
                lost = true
            }
            if (started && base != expectedBase) lost = true
            started = true
            expectedBase = (base + total) and 0xFFFFFFFFL
            if (lost) onLoss()

            val frame = pool.acquire()
            if (frame == null || total > have.size || (total - 1).toLong() * PacketHeader.MAX_PAYLOAD >= frame.data.size) {
                // No buffer, or the frame would not fit: it is lost.
                frame?.let { pool.release(it) }
                onLoss()
                return
            }
            current = frame
            currentBase = base
            currentTotal = total
            received = 0
            have.fill(false, 0, total)
            frame.size = 0
            frame.isKeyframe = h.isKeyframe
            frame.senderTimestamp = h.timestamp
            frame.firstPacketNs = nowNs
        }

        val frame = current!!
        if (total != currentTotal || have[idx]) return
        val last = idx == total - 1
        val at = idx * PacketHeader.MAX_PAYLOAD
        if ((!last && length != PacketHeader.MAX_PAYLOAD) || at + length > frame.data.size) {
            // Breaks the fixed fragment size the protocol promises.
            abandon()
            onLoss()
            return
        }
        System.arraycopy(data, offset, frame.data, at, length)
        have[idx] = true
        received++
        if (last) frame.size = at + length

        if (received == total) {
            frame.completeNs = nowNs
            current = null
            onFrame(frame)
        }
    }

    /** Returns a frame still being assembled to the pool (when the receiver stops). */
    fun reset() {
        abandon()
        started = false
    }

    private fun abandon() {
        current?.let { pool.release(it) }
        current = null
    }

    companion object {
        /** a - b for 32-bit wrapping sequence numbers: negative when a is older. */
        fun seqDiff(a: Long, b: Long): Int = (a - b).toInt()
    }
}

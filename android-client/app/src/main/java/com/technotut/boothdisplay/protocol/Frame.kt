package com.technotut.boothdisplay.protocol

import java.util.concurrent.ArrayBlockingQueue

/**
 * One H.264 access unit, reassembled in place. Frames come from a [FramePool]
 * and go back to it once the decoder has copied them, so the receive path
 * allocates nothing per frame.
 */
class Frame(capacity: Int) {
    val data = ByteArray(capacity)
    var size = 0
    var isKeyframe = false

    /** Sender timestamp (controller clock, microseconds, 32 bits). */
    var senderTimestamp = 0L

    /** System.nanoTime() when the first fragment arrived. */
    var firstPacketNs = 0L

    /** System.nanoTime() when the last fragment arrived. */
    var completeNs = 0L
}

/** A fixed set of [Frame]s shared by the receive and decode threads. */
class FramePool(count: Int, val frameCapacity: Int) {
    private val free = ArrayBlockingQueue<Frame>(count)

    init {
        repeat(count) { free.add(Frame(frameCapacity)) }
    }

    /** A free frame, or null if every frame is in use. */
    fun acquire(): Frame? = free.poll()

    fun release(frame: Frame) {
        free.offer(frame)
    }

    val available: Int
        get() = free.size
}

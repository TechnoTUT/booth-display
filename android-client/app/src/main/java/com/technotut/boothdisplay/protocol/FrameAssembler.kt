package com.technotut.boothdisplay.protocol

import java.io.ByteArrayOutputStream

/**
 * Reassembles fragmented UDP packets into complete H.264 NAL units / frames.
 * Keeps memory allocation minimal to prevent Out Of Memory on 1GB RAM devices.
 */
class FrameAssembler(
    private val onFrameComplete: (payload: ByteArray, isKeyframe: Boolean, timestamp: Long) -> Unit
) {
    private var currentTimestamp: Long = -1L
    private var expectedFragments: Int = 0
    private var receivedFragments: Int = 0
    private var isCurrentKeyframe: Boolean = false

    // Slot-based fragment storage to avoid heap reallocations
    private var fragmentBuffers = arrayOfNulls<ByteArray>(128)
    private var fragmentLengths = IntArray(128)

    fun onPacket(header: PacketHeader, payload: ByteArray, offset: Int, length: Int) {
        // Single packet NALU (not fragmented)
        if (header.fragmentTotal <= 1) {
            val completeFrame = ByteArray(length)
            System.arraycopy(payload, offset, completeFrame, 0, length)
            onFrameComplete(completeFrame, header.isKeyframe, header.timestamp)
            return
        }

        // Fragmented NALU: calculate base sequence number for this NALU
        val seqBase = (header.sequenceNumber - header.fragmentIndex).toLong()
        if (seqBase != currentTimestamp) {
            reset(seqBase, header.fragmentTotal.toInt(), header.isKeyframe)
        }

        val idx = header.fragmentIndex.toInt()
        if (idx in 0 until expectedFragments) {
            if (fragmentBuffers[idx] == null || fragmentBuffers[idx]!!.size < length) {
                fragmentBuffers[idx] = ByteArray(length)
            }
            System.arraycopy(payload, offset, fragmentBuffers[idx]!!, 0, length)
            fragmentLengths[idx] = length
            receivedFragments++
        }

        // Check if all fragments arrived
        if (receivedFragments == expectedFragments && expectedFragments > 0) {
            assembleAndEmit(header.timestamp)
        }
    }

    private fun reset(newTimestamp: Long, total: Int, keyframe: Boolean) {
        currentTimestamp = newTimestamp
        expectedFragments = total
        receivedFragments = 0
        isCurrentKeyframe = keyframe

        if (total > fragmentBuffers.size) {
            fragmentBuffers = arrayOfNulls(total + 16)
            fragmentLengths = IntArray(total + 16)
        }
    }

    private fun assembleAndEmit(timestamp: Long) {
        var totalBytes = 0
        for (i in 0 until expectedFragments) {
            totalBytes += fragmentLengths[i]
        }

        if (totalBytes > 0) {
            val completeFrame = ByteArray(totalBytes)
            var cursor = 0
            for (i in 0 until expectedFragments) {
                val len = fragmentLengths[i]
                if (fragmentBuffers[i] != null && len > 0) {
                    System.arraycopy(fragmentBuffers[i]!!, 0, completeFrame, cursor, len)
                    cursor += len
                }
            }
            onFrameComplete(completeFrame, isCurrentKeyframe, timestamp)
        }

        // Reset state
        expectedFragments = 0
        receivedFragments = 0
    }
}

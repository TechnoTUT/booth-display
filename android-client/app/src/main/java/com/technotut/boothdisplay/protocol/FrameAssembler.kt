package com.technotut.boothdisplay.protocol

/**
 * Reassembles fragmented UDP packets into complete H.264 NAL units / frames.
 * Handles packet reordering, duplicates, and missing fragments cleanly.
 */
class FrameAssembler(
    private val onFrameComplete: (payload: ByteArray, isKeyframe: Boolean, timestamp: Long) -> Unit
) {
    private var currentSeqBase: Long = -1L
    private var expectedFragments: Int = 0
    private var receivedFragments: Int = 0
    private var isCurrentKeyframe: Boolean = false
    private var currentTimestamp: Long = 0L

    // Map of fragment index to payload slice
    private val fragments = mutableMapOf<Int, ByteArray>()

    fun onPacket(header: PacketHeader, payload: ByteArray, offset: Int, length: Int) {
        // Single packet NALU (not fragmented)
        if (header.fragmentTotal <= 1) {
            val completeFrame = ByteArray(length)
            System.arraycopy(payload, offset, completeFrame, 0, length)
            onFrameComplete(completeFrame, header.isKeyframe, header.timestamp)
            return
        }

        // Fragmented NALU: calculate base sequence number for this NALU
        val seqBase = header.sequenceNumber - header.fragmentIndex
        if (seqBase != currentSeqBase) {
            currentSeqBase = seqBase
            expectedFragments = header.fragmentTotal
            receivedFragments = 0
            isCurrentKeyframe = header.isKeyframe
            currentTimestamp = header.timestamp
            fragments.clear()
        }

        val idx = header.fragmentIndex
        if (!fragments.containsKey(idx)) {
            val chunk = ByteArray(length)
            System.arraycopy(payload, offset, chunk, 0, length)
            fragments[idx] = chunk
            receivedFragments++
        }

        // Check if all fragments arrived
        if (receivedFragments == expectedFragments && expectedFragments > 0) {
            var totalBytes = 0
            var allPresent = true
            for (i in 0 until expectedFragments) {
                val f = fragments[i]
                if (f == null) {
                    allPresent = false
                    break
                }
                totalBytes += f.size
            }

            if (allPresent && totalBytes > 0) {
                val completeFrame = ByteArray(totalBytes)
                var cursor = 0
                for (i in 0 until expectedFragments) {
                    val f = fragments[i]!!
                    System.arraycopy(f, 0, completeFrame, cursor, f.size)
                    cursor += f.size
                }
                onFrameComplete(completeFrame, isCurrentKeyframe, currentTimestamp)
            }

            // Reset state
            expectedFragments = 0
            receivedFragments = 0
            fragments.clear()
        }
    }
}


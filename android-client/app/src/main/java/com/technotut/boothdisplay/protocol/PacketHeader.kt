package com.technotut.boothdisplay.protocol

/**
 * The 16-byte packet header, protocol version 2 (docs/streaming-protocol.md).
 *
 *  0                   1                   2                   3
 *  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |  Magic (0xBD) | Version (0x02)| PayloadType(1)|     Flags     |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |                       Sequence Number                         |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |                   Timestamp (32-bit, microseconds)            |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |          Fragment Index       |        Fragment Total         |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 *
 * One instance is reused for every packet, so parsing allocates nothing.
 * Unsigned 32-bit fields are held in Longs (0 until 2^32).
 */
class PacketHeader {
    var payloadType: Int = 0
    var flags: Int = 0
    var sequenceNumber: Long = 0L
    var timestamp: Long = 0L
    var fragmentIndex: Int = 0
    var fragmentTotal: Int = 0

    val isMarker: Boolean
        get() = (flags and FLAG_MARKER) != 0

    val isKeyframe: Boolean
        get() = (flags and FLAG_KEYFRAME) != 0

    enum class Result { OK, NOT_OURS, WRONG_VERSION }

    /** Reads the header at [offset]. Fields are only valid when it returns [Result.OK]. */
    fun parse(data: ByteArray, offset: Int, length: Int): Result {
        if (length < HEADER_SIZE || data[offset] != MAGIC_BYTE) return Result.NOT_OURS
        if (data[offset + 1] != VERSION_BYTE) return Result.WRONG_VERSION
        payloadType = data[offset + 2].toInt() and 0xFF
        flags = data[offset + 3].toInt() and 0xFF
        sequenceNumber = u32(data, offset + 4)
        timestamp = u32(data, offset + 8)
        fragmentIndex = u16(data, offset + 12)
        fragmentTotal = u16(data, offset + 14)
        return Result.OK
    }

    companion object {
        const val MAGIC_BYTE: Byte = 0xBD.toByte()
        const val VERSION_BYTE: Byte = 0x02
        const val HEADER_SIZE: Int = 16

        /** Every fragment but the last of a frame carries exactly this many bytes. */
        const val MAX_PAYLOAD: Int = 1400

        const val PAYLOAD_TYPE_H264: Int = 0x01
        const val PAYLOAD_TYPE_KEYFRAME_REQUEST: Int = 0x10
        const val PAYLOAD_TYPE_PING: Int = 0x11
        const val PAYLOAD_TYPE_PONG: Int = 0x12

        const val FLAG_MARKER: Int = 0x01
        const val FLAG_KEYFRAME: Int = 0x02

        /** Size of a PONG: the header and the echoed PING timestamp. */
        const val PONG_SIZE: Int = HEADER_SIZE + 4

        fun u32(d: ByteArray, i: Int): Long =
            ((d[i].toLong() and 0xFF) shl 24) or ((d[i + 1].toLong() and 0xFF) shl 16) or
                ((d[i + 2].toLong() and 0xFF) shl 8) or (d[i + 3].toLong() and 0xFF)

        fun u16(d: ByteArray, i: Int): Int = ((d[i].toInt() and 0xFF) shl 8) or (d[i + 1].toInt() and 0xFF)

        /** Writes a control message (no payload, not fragmented) into [out]. */
        fun writeControl(out: ByteArray, payloadType: Int, sequence: Long, timestamp: Long) {
            out[0] = MAGIC_BYTE
            out[1] = VERSION_BYTE
            out[2] = payloadType.toByte()
            out[3] = 0
            putU32(out, 4, sequence)
            putU32(out, 8, timestamp)
            out[12] = 0
            out[13] = 0
            out[14] = 0
            out[15] = 1
        }

        private fun putU32(out: ByteArray, i: Int, v: Long) {
            out[i] = (v ushr 24).toByte()
            out[i + 1] = (v ushr 16).toByte()
            out[i + 2] = (v ushr 8).toByte()
            out[i + 3] = v.toByte()
        }
    }
}

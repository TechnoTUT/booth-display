package com.technotut.boothdisplay.protocol

import java.nio.ByteBuffer
import java.nio.ByteOrder

/**
 * 16-byte custom packet header specification.
 *
 *  0                   1                   2                   3
 *  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |  Magic (0xBD) | Version (0x01)| PayloadType(1)| Flags (Mark)  |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |                       Sequence Number                         |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |                      Timestamp (32-bit ms)                    |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |          Fragment Index       |        Fragment Total         |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 */
data class PacketHeader(
    val magic: Byte,
    val version: Byte,
    val payloadType: Byte,
    val flags: Byte,
    val sequenceNumber: Long,
    val timestamp: Long,
    val fragmentIndex: Int,
    val fragmentTotal: Int
) {
    val isMarker: Boolean
        get() = (flags.toInt() and FLAG_MARKER) != 0

    val isKeyframe: Boolean
        get() = (flags.toInt() and FLAG_KEYFRAME) != 0

    companion object {
        const val MAGIC_BYTE: Byte = 0xBD.toByte()
        const val VERSION_BYTE: Byte = 0x01.toByte()
        const val HEADER_SIZE: Int = 16

        const val PAYLOAD_TYPE_H264: Byte = 0x01.toByte()
        const val PAYLOAD_TYPE_JPEG: Byte = 0x02.toByte()
        const val PAYLOAD_TYPE_RAW: Byte = 0x03.toByte()

        const val FLAG_MARKER: Int = 0x01
        const val FLAG_KEYFRAME: Int = 0x02

        fun parse(data: ByteArray, offset: Int = 0, length: Int = data.size): PacketHeader? {
            if (length < HEADER_SIZE) return null
            val magic = data[offset]
            if (magic != MAGIC_BYTE) return null
            val version = data[offset + 1]
            if (version != VERSION_BYTE) return null

            val payloadType = data[offset + 2]
            val flags = data[offset + 3]

            val bb = ByteBuffer.wrap(data, offset, length).order(ByteOrder.BIG_ENDIAN)
            val seq = bb.getInt(4).toLong() and 0xFFFFFFFFL
            val ts = bb.getInt(8).toLong() and 0xFFFFFFFFL
            val fragIdx = bb.getShort(12).toInt() and 0xFFFF
            val fragTotal = bb.getShort(14).toInt() and 0xFFFF

            return PacketHeader(
                magic = magic,
                version = version,
                payloadType = payloadType,
                flags = flags,
                sequenceNumber = seq,
                timestamp = ts,
                fragmentIndex = fragIdx,
                fragmentTotal = fragTotal
            )
        }
    }
}

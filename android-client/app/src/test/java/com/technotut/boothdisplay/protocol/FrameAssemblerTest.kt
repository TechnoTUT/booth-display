package com.technotut.boothdisplay.protocol

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class FrameAssemblerTest {
    private class Packet(val bytes: ByteArray)

    private lateinit var pool: FramePool
    private lateinit var assembler: FrameAssembler
    private val frames = mutableListOf<ByteArray>()
    private val keyframes = mutableListOf<Boolean>()
    private var losses = 0
    private val header = PacketHeader()

    @Before
    fun setUp() {
        pool = FramePool(4, 64 * 1024)
        assembler = FrameAssembler(pool, { f ->
            frames.add(f.data.copyOf(f.size))
            keyframes.add(f.isKeyframe)
            pool.release(f)
        }, { losses++ })
    }

    /** Splits [au] like the controller does, numbering packets from [seq]. */
    private fun packetize(au: ByteArray, seq: Long, keyframe: Boolean = false, ts: Long = 0): List<Packet> {
        val total = (au.size + PacketHeader.MAX_PAYLOAD - 1) / PacketHeader.MAX_PAYLOAD
        return (0 until total).map { i ->
            val from = i * PacketHeader.MAX_PAYLOAD
            val len = minOf(PacketHeader.MAX_PAYLOAD, au.size - from)
            val p = ByteArray(PacketHeader.HEADER_SIZE + len)
            PacketHeader.writeControl(p, PacketHeader.PAYLOAD_TYPE_H264, (seq + i) and 0xFFFFFFFFL, ts)
            var flags = if (keyframe) PacketHeader.FLAG_KEYFRAME else 0
            if (i == total - 1) flags = flags or PacketHeader.FLAG_MARKER
            p[3] = flags.toByte()
            p[12] = (i shr 8).toByte(); p[13] = i.toByte()
            p[14] = (total shr 8).toByte(); p[15] = total.toByte()
            System.arraycopy(au, from, p, PacketHeader.HEADER_SIZE, len)
            Packet(p)
        }
    }

    private fun feed(p: Packet) {
        assertEquals(PacketHeader.Result.OK, header.parse(p.bytes, 0, p.bytes.size))
        assembler.onPacket(header, p.bytes, PacketHeader.HEADER_SIZE, p.bytes.size - PacketHeader.HEADER_SIZE, 0L)
    }

    private fun au(size: Int, seed: Int) = ByteArray(size) { (it * 7 + seed).toByte() }

    @Test
    fun reassemblesFramesInOrder() {
        val a = au(100, 1)
        val b = au(PacketHeader.MAX_PAYLOAD * 3 + 5, 2)
        (packetize(a, 10, keyframe = true) + packetize(b, 11)).forEach(::feed)
        assertEquals(2, frames.size)
        assertArrayEquals(a, frames[0])
        assertArrayEquals(b, frames[1])
        assertEquals(listOf(true, false), keyframes)
        assertEquals(0, losses)
        assertEquals(4, pool.available)
    }

    @Test
    fun reassemblesFragmentsOutOfOrder() {
        val a = au(PacketHeader.MAX_PAYLOAD * 4, 3)
        packetize(a, 0).reversed().forEach(::feed)
        assertArrayEquals(a, frames.single())
        assertEquals(0, losses)
    }

    @Test
    fun reportsAFrameAbandonedIncomplete() {
        val a = packetize(au(PacketHeader.MAX_PAYLOAD * 3, 4), 0)
        val b = au(50, 5)
        a.filterIndexed { i, _ -> i != 1 }.forEach(::feed)
        packetize(b, 3, keyframe = true).forEach(::feed)
        assertEquals(1, losses)
        assertArrayEquals(b, frames.single())
        assertEquals(4, pool.available)
    }

    @Test
    fun reportsAWholeFrameMissing() {
        packetize(au(10, 6), 0).forEach(::feed)
        // Frame at seq 1 (2 packets) never arrives; the next starts at 3.
        packetize(au(10, 7), 3).forEach(::feed)
        assertEquals(2, frames.size)
        assertEquals(1, losses)
    }

    @Test
    fun ignoresDuplicatesAndLateFragments() {
        val a = packetize(au(PacketHeader.MAX_PAYLOAD * 2, 8), 0)
        feed(a[0]); feed(a[0]); feed(a[1])
        feed(a[1]) // late duplicate of a finished frame
        packetize(au(10, 9), 2).forEach(::feed)
        assertEquals(2, frames.size)
        assertEquals(0, losses)
    }

    @Test
    fun handlesSequenceWraparound() {
        val a = au(PacketHeader.MAX_PAYLOAD * 3, 10)
        val first = packetize(au(10, 11), 0xFFFFFFFEL)
        first.forEach(::feed)
        packetize(a, 0xFFFFFFFFL).forEach(::feed) // seq FFFFFFFF, 0, 1
        packetize(au(10, 12), 2).forEach(::feed)
        assertEquals(3, frames.size)
        assertArrayEquals(a, frames[1])
        assertEquals(0, losses)
    }

    @Test
    fun rejectsShortNonLastFragments() {
        val bad = packetize(au(PacketHeader.MAX_PAYLOAD * 2, 13), 0)
        val truncated = Packet(bad[0].bytes.copyOf(bad[0].bytes.size - 1))
        feed(truncated)
        feed(bad[1])
        assertTrue(frames.isEmpty())
        assertEquals(1, losses)
        assertEquals(4, pool.available)
    }

    @Test
    fun reportsLossWhenNoBufferIsFree() {
        val held = List(4) { pool.acquire()!! }
        packetize(au(10, 14), 0).forEach(::feed)
        assertTrue(frames.isEmpty())
        assertEquals(1, losses)
        held.forEach(pool::release)
        packetize(au(10, 15), 1).forEach(::feed)
        assertEquals(1, frames.size)
    }

    @Test
    fun resetReturnsThePartialFrame() {
        feed(packetize(au(PacketHeader.MAX_PAYLOAD * 2, 16), 0)[0])
        assertEquals(3, pool.available)
        assembler.reset()
        assertEquals(4, pool.available)
        assertFalse(frames.isNotEmpty())
    }
}

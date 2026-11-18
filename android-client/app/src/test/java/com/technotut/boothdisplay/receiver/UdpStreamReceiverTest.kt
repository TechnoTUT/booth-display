package com.technotut.boothdisplay.receiver

import com.technotut.boothdisplay.protocol.ClockSync
import com.technotut.boothdisplay.protocol.FramePool
import com.technotut.boothdisplay.protocol.PacketHeader
import com.technotut.boothdisplay.protocol.StreamStats
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.InetSocketAddress
import java.util.Collections

/** Drives the receiver from a fake controller socket on localhost. */
class UdpStreamReceiverTest {
    private val pool = FramePool(8, 64 * 1024)
    private val stats = StreamStats()
    private val clock = ClockSync()
    private val submitted = Collections.synchronizedList(mutableListOf<Byte>())
    private lateinit var controller: DatagramSocket
    private lateinit var receiver: UdpStreamReceiver
    private var displayPort = 0
    private var seq = 0L

    @Before
    fun setUp() {
        displayPort = DatagramSocket(0, InetAddress.getLoopbackAddress()).use { it.localPort }
        controller = DatagramSocket(InetSocketAddress(InetAddress.getLoopbackAddress(), 0))
        controller.soTimeout = 2000
        receiver = UdpStreamReceiver(displayPort, pool, stats, clock) { frame ->
            submitted.add(frame.data[0]) // first byte tags the frame
            pool.release(frame)
            true
        }
        receiver.start()
        Thread.sleep(200) // let it bind
    }

    @After
    fun tearDown() {
        receiver.stop()
        controller.close()
    }

    /** Sends a frame tagged [tag] in [fragments] packets, leaving out the indices in [skip]. */
    private fun sendFrame(tag: Byte, keyframe: Boolean, fragments: Int = 1, skip: Set<Int> = emptySet()) {
        for (i in 0 until fragments) {
            val len = if (i < fragments - 1) PacketHeader.MAX_PAYLOAD else 10
            val p = ByteArray(PacketHeader.HEADER_SIZE + len)
            PacketHeader.writeControl(p, PacketHeader.PAYLOAD_TYPE_H264, seq + i, 0)
            p[3] = ((if (keyframe) PacketHeader.FLAG_KEYFRAME else 0) or
                (if (i == fragments - 1) PacketHeader.FLAG_MARKER else 0)).toByte()
            p[13] = i.toByte()
            p[15] = fragments.toByte()
            p[PacketHeader.HEADER_SIZE] = tag
            if (i !in skip) {
                controller.send(DatagramPacket(p, p.size, InetAddress.getLoopbackAddress(), displayPort))
            }
        }
        seq += fragments
    }

    /** Reads control messages until one of [type] arrives, answering PINGs. */
    private fun awaitControl(type: Int): Long {
        val buf = ByteArray(64)
        val h = PacketHeader()
        while (true) {
            val p = DatagramPacket(buf, buf.size)
            controller.receive(p) // throws on timeout
            assertEquals(PacketHeader.Result.OK, h.parse(buf, 0, p.length))
            if (h.payloadType == PacketHeader.PAYLOAD_TYPE_PING) {
                val pong = ByteArray(PacketHeader.PONG_SIZE)
                PacketHeader.writeControl(pong, PacketHeader.PAYLOAD_TYPE_PONG, h.sequenceNumber, 777)
                System.arraycopy(buf, 8, pong, PacketHeader.HEADER_SIZE, 4) // echo t0
                controller.send(DatagramPacket(pong, pong.size, p.socketAddress))
            }
            if (h.payloadType == type) return h.sequenceNumber
        }
    }

    private fun waitFor(what: String, cond: () -> Boolean) {
        val deadline = System.currentTimeMillis() + 2000
        while (!cond()) {
            assertTrue("timed out waiting for $what", System.currentTimeMillis() < deadline)
            Thread.sleep(5)
        }
    }

    @Test
    fun asksForKeyframesAtStartAndAfterALoss() {
        // A P-frame first: skipped, and the receiver asks for a keyframe and pings.
        sendFrame(1, keyframe = false)
        awaitControl(PacketHeader.PAYLOAD_TYPE_KEYFRAME_REQUEST)
        awaitControl(PacketHeader.PAYLOAD_TYPE_PING)
        waitFor("clock sync") { clock.rttUs >= 0 }

        sendFrame(2, keyframe = true)
        sendFrame(3, keyframe = false, fragments = 2)
        waitFor("two frames") { submitted.size == 2 }

        // Fragment 1 of frame 4 is lost: frame 5 reveals it, is skipped, and a request goes out at once.
        sendFrame(4, keyframe = false, fragments = 2, skip = setOf(1))
        sendFrame(5, keyframe = false)
        val lastSeq = awaitControl(PacketHeader.PAYLOAD_TYPE_KEYFRAME_REQUEST)
        assertEquals("request carries the last sequence number received", seq - 1, lastSeq)

        sendFrame(6, keyframe = true)
        waitFor("recovery keyframe") { submitted.size == 3 }
        assertEquals(listOf<Byte>(2, 3, 6), submitted.toList())

        val snap = stats.snapshot(System.nanoTime(), clock.rttUs)
        assertEquals(1L, snap.lostTotal)
        assertEquals(2L, snap.skippedTotal)
        assertTrue(snap.keyframeRequestsTotal >= 2)
        waitFor("pool refilled") { pool.available == 8 }
    }

    @Test
    fun startsOverWhenTheControllerRestarts() {
        sendFrame(1, keyframe = true)
        sendFrame(2, keyframe = false)
        awaitControl(PacketHeader.PAYLOAD_TYPE_PING)
        waitFor("clock sync") { clock.rttUs >= 0 }
        waitFor("first run") { submitted.size == 2 }

        // The restarted controller sends from a new port, numbering from 0
        // again, and a P-frame comes first.
        controller.close()
        controller = DatagramSocket(InetSocketAddress(InetAddress.getLoopbackAddress(), 0))
        controller.soTimeout = 2000
        seq = 0
        sendFrame(3, keyframe = false)
        awaitControl(PacketHeader.PAYLOAD_TYPE_KEYFRAME_REQUEST)
        sendFrame(4, keyframe = true)
        sendFrame(5, keyframe = false)
        waitFor("second run") { submitted.size == 4 }
        assertEquals(listOf<Byte>(1, 2, 4, 5), submitted.toList())
        assertEquals(1L, receiver.streamRestarts)
    }
}

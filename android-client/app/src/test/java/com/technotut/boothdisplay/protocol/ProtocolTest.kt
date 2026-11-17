package com.technotut.boothdisplay.protocol

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class PacketHeaderTest {
    @Test
    fun parsesAllFields() {
        // Bytes as the controller writes them (core/src/protocol.rs).
        val p = byteArrayOf(
            0xBD.toByte(), 0x02, 0x01, 0x03,
            0xDE.toByte(), 0xAD.toByte(), 0xBE.toByte(), 0xEF.toByte(),
            0x00, 0x01, 0xE2.toByte(), 0x40,
            0x00, 0x03, 0x00, 0x07,
        )
        val h = PacketHeader()
        assertEquals(PacketHeader.Result.OK, h.parse(p, 0, p.size))
        assertEquals(PacketHeader.PAYLOAD_TYPE_H264, h.payloadType)
        assertEquals(true, h.isMarker)
        assertEquals(true, h.isKeyframe)
        assertEquals(0xDEADBEEFL, h.sequenceNumber)
        assertEquals(123456L, h.timestamp)
        assertEquals(3, h.fragmentIndex)
        assertEquals(7, h.fragmentTotal)
    }

    @Test
    fun rejectsOtherVersionsAndForeignPackets() {
        val h = PacketHeader()
        val v1 = ByteArray(16).also { it[0] = 0xBD.toByte(); it[1] = 0x01 }
        assertEquals(PacketHeader.Result.WRONG_VERSION, h.parse(v1, 0, v1.size))
        assertEquals(PacketHeader.Result.NOT_OURS, h.parse(ByteArray(16), 0, 16))
        assertEquals(PacketHeader.Result.NOT_OURS, h.parse(v1, 0, 15))
    }

    @Test
    fun writesControlMessages() {
        val out = ByteArray(16)
        PacketHeader.writeControl(out, PacketHeader.PAYLOAD_TYPE_PING, 0x01020304L, 0xFFFFFFFFL)
        assertArrayEquals(
            byteArrayOf(0xBD.toByte(), 0x02, 0x11, 0, 1, 2, 3, 4, -1, -1, -1, -1, 0, 0, 0, 1),
            out,
        )
    }
}

class ClockSyncTest {
    @Test
    fun estimatesOffsetAndLatency() {
        val c = ClockSync()
        assertNull(c.elapsedSince(0, 0))
        // Display sends at 1_000, controller answers at its 50_400, display gets it at 1_800.
        c.onPong(t0 = 1_000, t1 = 50_400, t2 = 1_800)
        assertEquals(800L, c.rttUs)
        // Controller ~ display + 49_000.
        assertEquals(49_000L, c.offsetUs)
        // A frame stamped 60_000 on the controller, decoded at display 11_030.
        assertEquals(30L, c.elapsedSince(60_000, 11_030))
    }

    @Test
    fun keepsTheShortestRoundTrip() {
        val c = ClockSync()
        c.onPong(t0 = 0, t1 = 10_000, t2 = 400) // rtt 400
        c.onPong(t0 = 1_000_000, t1 = 1_020_000, t2 = 1_010_000) // rtt 10_000, skewed
        assertEquals(400L, c.rttUs)
        assertEquals(10_000L - 400 + 200, c.offsetUs)
    }

    @Test
    fun survives32BitWraparound() {
        val c = ClockSync()
        // The PING timestamp wrapped: t0 is the low 32 bits of display time 2^32 - 100.
        val display = (1L shl 32) + 300
        c.onPong(t0 = 0xFFFFFF9CL, t1 = 5_000, t2 = display)
        assertEquals(400L, c.rttUs)
        // Controller time 5_000 matched display time display - 200; a frame
        // stamped just before the controller clock wrapped is still measured right.
        assertEquals(10_200L, c.elapsedSince((5_000L - 10_000) and 0xFFFFFFFFL, display))
    }

    @Test
    fun ignoresImplausibleRoundTrips() {
        val c = ClockSync()
        c.onPong(t0 = 10, t1 = 0, t2 = 5) // negative
        c.onPong(t0 = 0, t1 = 0, t2 = 2_000_000) // 2 s
        assertEquals(-1L, c.rttUs)
    }
}

class StreamStatsTest {
    @Test
    fun summarizesAndCountsStalls() {
        val s = StreamStats()
        s.snapshot(0, -1)
        val ms = 1_000_000L
        // 10 frames 33 ms apart, then one 100 ms gap.
        var t = 0L
        for (i in 0 until 10) {
            t += 33 * ms
            s.frameDecoded(t, t - 10 * ms, t - 8 * ms, (20 + i) * 1000L)
        }
        t += 100 * ms
        s.frameDecoded(t, t - 10 * ms, t - 8 * ms, 40_000L)
        s.loss()
        val snap = s.snapshot(t, 500)
        assertEquals(1, snap.stalls)
        assertEquals(1L, snap.lostTotal)
        assertEquals(2f, snap.wait!![0], 0.01f)
        assertEquals(8f, snap.decode!![0], 0.01f)
        assertEquals(25f, snap.e2e!![0], 0.01f) // median of 20..29 and 40
        assertEquals(40f, snap.e2e!![2], 0.01f)
        assertEquals(11 * 1e9f / t, snap.fps, 0.01f)
        assertEquals(0.5f, snap.rttMs!!, 0.001f)

        // The next window starts empty; totals carry on.
        val next = s.snapshot(t + 1_000 * ms, -1)
        assertNull(next.e2e)
        assertEquals(0, next.stalls)
        assertEquals(1L, next.lostTotal)
    }
}

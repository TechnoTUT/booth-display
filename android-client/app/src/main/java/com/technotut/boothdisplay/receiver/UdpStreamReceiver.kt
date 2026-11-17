package com.technotut.boothdisplay.receiver

import android.os.Process
import android.util.Log
import com.technotut.boothdisplay.protocol.ClockSync
import com.technotut.boothdisplay.protocol.Frame
import com.technotut.boothdisplay.protocol.FrameAssembler
import com.technotut.boothdisplay.protocol.FramePool
import com.technotut.boothdisplay.protocol.PacketHeader
import com.technotut.boothdisplay.protocol.StreamStats
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.SocketTimeoutException
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Receives the stream (protocol version 2) on a dedicated thread and keeps
 * the picture consistent:
 *
 * - Reassembles access units and hands them to [submit] (the decoder). It
 *   returns false when the decoder fell behind and dropped frames.
 * - After a loss (or such a drop) it skips frames until a keyframe and sends
 *   KEYFRAME_REQUEST to the controller, repeated every 250 ms while waiting.
 *   It also waits for a keyframe at start, so the first picture comes as soon
 *   as the controller answers instead of at the next regular IDR.
 * - Sends a PING once a second and feeds the PONGs to [clock].
 *
 * Control messages go from the receive socket to the address the video comes
 * from, the only address the controller accepts them from.
 */
class UdpStreamReceiver(
    private val port: Int,
    private val pool: FramePool,
    private val stats: StreamStats,
    private val clock: ClockSync,
    private val submit: (Frame) -> Boolean,
) {
    companion object {
        private const val TAG = "UdpStreamReceiver"
        private const val BUFFER_SIZE = 2048 // Standard MTU is <= 1500
        private const val REQUESTED_RCVBUF = 2 * 1024 * 1024
        private const val POLL_MS = 50
        private const val KEYFRAME_REQUEST_INTERVAL_NS = 250_000_000L
        private const val PING_INTERVAL_NS = 1_000_000_000L
    }

    private val isRunning = AtomicBoolean(false)
    private var socket: DatagramSocket? = null
    private var workerThread: Thread? = null

    @Volatile
    var packetCount: Long = 0L
        private set

    @Volatile
    var lastError: String? = null
        private set

    /** Receive buffer the kernel actually granted (capped by net.core.rmem_max), or 0 before bind. */
    @Volatile
    var receiveBufferBytes: Int = 0
        private set

    /** Packets of another protocol version (the controller and this app do not match). */
    @Volatile
    var wrongVersionPackets: Long = 0L
        private set

    /** System.nanoTime() of the last complete frame, 0 before the first. */
    @Volatile
    var lastFrameNs: Long = 0L
        private set

    // Receive thread only.
    private val header = PacketHeader()
    private var waitingForKeyframe = true
    private var lastKeyframeRequestNs = 0L
    private var lastPingNs = 0L
    private var lastVideoSeq = 0L
    private var pingSeq = 0L
    private var controllerAddr: InetAddress? = null
    private var controllerPort = 0
    private val controlBuf = ByteArray(PacketHeader.HEADER_SIZE)
    private val controlPacket = DatagramPacket(controlBuf, controlBuf.size)

    private val assembler = FrameAssembler(pool, ::onFrame, ::onLoss)

    fun start() {
        if (isRunning.getAndSet(true)) return
        workerThread = Thread({ run() }, "UdpReceiverThread").apply {
            priority = Thread.MAX_PRIORITY
            start()
        }
    }

    private fun run() {
        try {
            Process.setThreadPriority(Process.THREAD_PRIORITY_URGENT_AUDIO)
        } catch (e: Exception) {
            Log.w(TAG, "Failed to set URGENT_AUDIO priority: ${e.message}")
        }
        Log.i(TAG, "Starting UDP Stream Receiver on port $port...")
        val receiveBuffer = ByteArray(BUFFER_SIZE)
        val packet = DatagramPacket(receiveBuffer, receiveBuffer.size)
        try {
            val sock = bind()
            socket = sock
            lastError = null
            while (isRunning.get()) {
                try {
                    // Reset the length before every receive, or later packets get truncated.
                    packet.length = receiveBuffer.size
                    sock.receive(packet)
                    packetCount++
                    onPacket(packet, receiveBuffer)
                } catch (e: SocketTimeoutException) {
                    // Periodic work below still runs without packets.
                } catch (e: Exception) {
                    if (!isRunning.get()) break
                    Log.w(TAG, "Socket receive error: ${e.message}")
                }
                periodic(sock, System.nanoTime())
            }
        } catch (e: Exception) {
            lastError = "Socket err: ${e.message}"
            Log.e(TAG, "Fatal socket initialization error: ${e.message}", e)
        } finally {
            socket?.close()
            socket = null
            assembler.reset()
        }
    }

    private fun bind(): DatagramSocket {
        // Bind to the IPv4 wildcard explicitly to avoid IPv6 dual-stack issues.
        val sock = try {
            DatagramSocket(null).apply {
                reuseAddress = true
                bind(InetSocketAddress("0.0.0.0", port))
            }
        } catch (e: Exception) {
            Log.w(TAG, "Failed binding to 0.0.0.0:$port, fallback to standard bind: ${e.message}")
            DatagramSocket(port).apply { reuseAddress = true }
        }
        // Set after bind (some Android versions throw otherwise).
        try {
            sock.receiveBufferSize = REQUESTED_RCVBUF
        } catch (e: Exception) {
            Log.w(TAG, "Could not set receiveBufferSize: ${e.message}")
        }
        // The kernel silently caps the request at net.core.rmem_max (and Linux
        // reports double the usable size), so log what we really got.
        receiveBufferBytes = try { sock.receiveBufferSize } catch (e: Exception) { 0 }
        if (receiveBufferBytes < REQUESTED_RCVBUF) {
            Log.w(TAG, "Receive buffer capped: requested $REQUESTED_RCVBUF, got $receiveBufferBytes bytes")
        } else {
            Log.i(TAG, "Receive buffer: $receiveBufferBytes bytes")
        }
        sock.soTimeout = POLL_MS
        return sock
    }

    private fun onPacket(packet: DatagramPacket, buf: ByteArray) {
        val length = packet.length
        when (header.parse(buf, 0, length)) {
            PacketHeader.Result.NOT_OURS -> return
            PacketHeader.Result.WRONG_VERSION -> {
                wrongVersionPackets++
                return
            }
            PacketHeader.Result.OK -> {}
        }
        val now = System.nanoTime()
        when (header.payloadType) {
            PacketHeader.PAYLOAD_TYPE_H264 -> {
                rememberController(packet)
                lastVideoSeq = header.sequenceNumber
                assembler.onPacket(header, buf, PacketHeader.HEADER_SIZE, length - PacketHeader.HEADER_SIZE, now)
            }
            PacketHeader.PAYLOAD_TYPE_PONG -> if (length >= PacketHeader.PONG_SIZE) {
                clock.onPong(PacketHeader.u32(buf, PacketHeader.HEADER_SIZE), header.timestamp, now / 1000L)
            }
        }
    }

    /** Control messages go back to wherever the video comes from. */
    private fun rememberController(packet: DatagramPacket) {
        val addr = packet.address
        if (addr != controllerAddr || packet.port != controllerPort) {
            controllerAddr = addr
            controllerPort = packet.port
            controlPacket.address = addr
            controlPacket.port = packet.port
            Log.i(TAG, "Controller at ${addr.hostAddress}:${packet.port}")
        }
    }

    private fun onFrame(frame: Frame) {
        lastFrameNs = frame.completeNs
        stats.frameReceived(frame)
        if (waitingForKeyframe) {
            if (!frame.isKeyframe) {
                stats.skipped()
                pool.release(frame)
                return
            }
            waitingForKeyframe = false
        }
        if (!submit(frame)) waitForKeyframe(System.nanoTime())
    }

    private fun onLoss() {
        stats.loss()
        waitForKeyframe(System.nanoTime())
    }

    private fun waitForKeyframe(now: Long) {
        if (!waitingForKeyframe) {
            waitingForKeyframe = true
            lastKeyframeRequestNs = 0L // ask right away
        }
        socket?.let { periodic(it, now) }
    }

    private fun periodic(sock: DatagramSocket, now: Long) {
        if (controllerAddr == null) return
        if (waitingForKeyframe && now - lastKeyframeRequestNs >= KEYFRAME_REQUEST_INTERVAL_NS) {
            lastKeyframeRequestNs = now
            stats.keyframeRequested()
            sendControl(sock, PacketHeader.PAYLOAD_TYPE_KEYFRAME_REQUEST, lastVideoSeq, now)
        }
        if (now - lastPingNs >= PING_INTERVAL_NS) {
            lastPingNs = now
            pingSeq = (pingSeq + 1) and 0xFFFFFFFFL
            sendControl(sock, PacketHeader.PAYLOAD_TYPE_PING, pingSeq, now)
        }
    }

    private fun sendControl(sock: DatagramSocket, payloadType: Int, sequence: Long, now: Long) {
        PacketHeader.writeControl(controlBuf, payloadType, sequence, (now / 1000L) and 0xFFFFFFFFL)
        try {
            sock.send(controlPacket)
        } catch (e: Exception) {
            Log.w(TAG, "Control message send failed: ${e.message}")
        }
    }

    fun stop() {
        isRunning.set(false)
        try {
            socket?.close()
        } catch (e: Exception) {
            // ignore
        }
        workerThread?.interrupt()
        workerThread = null
    }
}

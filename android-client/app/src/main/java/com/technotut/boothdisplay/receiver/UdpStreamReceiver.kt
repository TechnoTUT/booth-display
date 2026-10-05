package com.technotut.boothdisplay.receiver

import android.util.Log
import com.technotut.boothdisplay.protocol.FrameAssembler
import com.technotut.boothdisplay.protocol.PacketHeader
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetSocketAddress
import java.util.concurrent.atomic.AtomicBoolean

/**
 * High-performance UDP socket listener on dedicated background thread.
 */
class UdpStreamReceiver(
    private val port: Int = 8554,
    private val onFrameReceived: (payload: ByteArray, isKeyframe: Boolean, timestamp: Long) -> Unit
) {
    companion object {
        private const val TAG = "UdpStreamReceiver"
        private const val BUFFER_SIZE = 2048 // Standard MTU is <= 1500
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

    private val assembler = FrameAssembler { frame, isKeyframe, timestamp ->
        onFrameReceived(frame, isKeyframe, timestamp)
    }

    fun start() {
        if (isRunning.getAndSet(true)) return

        workerThread = Thread({
            Log.i(TAG, "Starting UDP Stream Receiver on port $port...")
            val receiveBuffer = ByteArray(BUFFER_SIZE)
            val packet = DatagramPacket(receiveBuffer, receiveBuffer.size)

            try {
                // Explicitly bind to IPv4 wildcard 0.0.0.0 to prevent IPv6 wildcard dual-stack issues
                val sock = try {
                    DatagramSocket(null).apply {
                        reuseAddress = true
                        bind(InetSocketAddress("0.0.0.0", port))
                    }
                } catch (e: Exception) {
                    Log.w(TAG, "Failed binding to 0.0.0.0:$port, fallback to standard bind: ${e.message}")
                    DatagramSocket(port).apply {
                        reuseAddress = true
                    }
                }

                // Set 2MB receive buffer after bind (prevent SocketException on some Android OS)
                try {
                    sock.receiveBufferSize = 2 * 1024 * 1024
                } catch (e: Exception) {
                    Log.w(TAG, "Could not set receiveBufferSize: ${e.message}")
                }

                socket = sock
                lastError = null

                while (isRunning.get()) {
                    try {
                        // Crucial: reset buffer length before every receive, otherwise JVM truncates future packets
                        packet.length = receiveBuffer.size

                        sock.receive(packet)
                        packetCount++
                        val length = packet.length
                        if (length > PacketHeader.HEADER_SIZE) {
                            val header = PacketHeader.parse(receiveBuffer, 0, length)
                            if (header != null && header.payloadType == PacketHeader.PAYLOAD_TYPE_H264) {
                                assembler.onPacket(
                                    header = header,
                                    payload = receiveBuffer,
                                    offset = PacketHeader.HEADER_SIZE,
                                    length = length - PacketHeader.HEADER_SIZE
                                )
                            }
                        }
                    } catch (e: Exception) {
                        if (!isRunning.get()) break
                        Log.w(TAG, "Socket receive error: ${e.message}")
                    }
                }
            } catch (e: Exception) {
                lastError = "Socket err: ${e.message}"
                Log.e(TAG, "Fatal socket initialization error: ${e.message}", e)
            } finally {
                socket?.close()
                socket = null
            }
        }, "UdpReceiverThread").apply {
            priority = Thread.MAX_PRIORITY // Real-time network thread
            start()
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

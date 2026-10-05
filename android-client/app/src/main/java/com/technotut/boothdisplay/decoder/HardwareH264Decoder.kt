package com.technotut.boothdisplay.decoder

import android.media.MediaCodec
import android.media.MediaFormat
import android.os.Build
import android.util.Log
import android.view.Surface
import java.nio.ByteBuffer

/**
 * Ultra-low latency H.264 hardware decoder using Android MediaCodec.
 * Directly renders decoded frames onto the target Surface (SurfaceView)
 * without intermediate Bitmaps, preventing GC pauses and OOM.
 */
class HardwareH264Decoder(
    private val width: Int = 1920,
    private val height: Int = 540
) {
    companion object {
        private const val TAG = "HardwareH264Decoder"
        private const val MIME_TYPE = MediaFormat.MIMETYPE_VIDEO_AVC
        private const val TIMEOUT_US = 10000L

        private const val NAL_TYPE_NON_IDR = 1
        private const val NAL_TYPE_IDR = 5
        private const val NAL_TYPE_SEI = 6
        private const val NAL_TYPE_SPS = 7
        private const val NAL_TYPE_PPS = 8
    }

    private val lock = Any()
    private var codec: MediaCodec? = null
    @Volatile
    private var isConfigured = false
    private var drainThread: Thread? = null

    @Volatile
    var renderedFrameCount: Long = 0L
        private set

    @Volatile
    var lastError: String? = null
        private set

    @Volatile
    var lastNalType: String = "None"
        private set

    // Parameter sets cached for Access Unit reassembly
    private var spsBuffer: ByteArray? = null
    private var ppsBuffer: ByteArray? = null
    private var hasReceivedKeyframe = false

    fun init(surface: Surface) {
        synchronized(lock) {
            stop()

            try {
                val format = MediaFormat.createVideoFormat(MIME_TYPE, width, height).apply {
                    // Crucial: Allocate sufficient input buffer to prevent BufferOverflowException
                    setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, 1024 * 1024)

                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                        try {
                            setInteger(MediaFormat.KEY_LOW_LATENCY, 1)
                        } catch (e: Exception) {
                            Log.w(TAG, "KEY_LOW_LATENCY not supported: ${e.message}")
                        }
                    }
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
                        try {
                            setInteger(MediaFormat.KEY_PRIORITY, 0)
                        } catch (e: Exception) {
                            Log.w(TAG, "KEY_PRIORITY not supported: ${e.message}")
                        }
                    }
                }

                val decoder = MediaCodec.createDecoderByType(MIME_TYPE)
                try {
                    decoder.configure(format, surface, null, 0)
                } catch (e: Exception) {
                    // Height 540 is not a multiple of 16 (16 * 34 = 544). Try 16-aligned fallback.
                    Log.w(TAG, "Configure with ${width}x${height} failed (${e.message}), trying 16-aligned height 544...")
                    val alignedFormat = MediaFormat.createVideoFormat(MIME_TYPE, width, 544).apply {
                        setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, 1024 * 1024)
                    }
                    decoder.configure(alignedFormat, surface, null, 0)
                }
                decoder.start()

                codec = decoder
                isConfigured = true
                hasReceivedKeyframe = false
                renderedFrameCount = 0L
                lastError = null

                // Start dedicated background output drain thread for immediate zero-latency rendering
                startDrainThread(decoder)

                Log.i(TAG, "Hardware H.264 decoder successfully started ($width x $height)")
            } catch (e: Exception) {
                lastError = "Codec init fail: ${e.message}"
                Log.e(TAG, "Failed to start MediaCodec: ${e.message}", e)
                stop()
            }
        }
    }

    private fun startDrainThread(decoder: MediaCodec) {
        val bufferInfo = MediaCodec.BufferInfo()
        drainThread = Thread({
            Log.i(TAG, "MediaCodec drain thread started")
            while (isConfigured) {
                try {
                    val outIndex = decoder.dequeueOutputBuffer(bufferInfo, 10000L)
                    when {
                        outIndex >= 0 -> {
                            // Render frame directly to SurfaceView
                            try {
                                decoder.releaseOutputBuffer(outIndex, true)
                                renderedFrameCount++
                            } catch (e: Exception) {
                                if (isConfigured) lastError = "Release err: ${e.message}"
                            }
                        }
                        outIndex == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED -> {
                            Log.i(TAG, "MediaCodec output format changed: ${decoder.outputFormat}")
                        }
                        outIndex == MediaCodec.INFO_OUTPUT_BUFFERS_CHANGED -> {
                            Log.i(TAG, "MediaCodec output buffers changed")
                        }
                        outIndex == MediaCodec.INFO_TRY_AGAIN_LATER -> {
                            // Timeout, continue polling
                        }
                    }
                } catch (e: Exception) {
                    if (!isConfigured) break
                    lastError = "Drain err: ${e.message}"
                    Log.w(TAG, "Drain output loop error: ${e.message}")
                }
            }
            Log.i(TAG, "MediaCodec drain thread stopped")
        }, "MediaCodecDrainThread").apply {
            priority = Thread.MAX_PRIORITY
            start()
        }
    }

    /**
     * Finds the H.264 NAL unit type from Annex-B formatted data.
     * Returns -1 if no start code is found.
     */
    private fun findNalType(data: ByteArray): Int {
        var offset = -1
        if (data.size >= 4 && data[0] == 0.toByte() && data[1] == 0.toByte() && data[2] == 0.toByte() && data[3] == 1.toByte()) {
            offset = 4
        } else if (data.size >= 3 && data[0] == 0.toByte() && data[1] == 0.toByte() && data[2] == 1.toByte()) {
            offset = 3
        }

        if (offset in 0 until data.size) {
            return data[offset].toInt() and 0x1F
        }
        return -1
    }

    fun decodeFrame(data: ByteArray, isKeyframe: Boolean, timestampMs: Long) {
        synchronized(lock) {
            val decoder = codec ?: return
            if (!isConfigured) return

            val nalType = findNalType(data)
            val ptsUs = System.nanoTime() / 1000L // Strictly monotonic presentation timestamp

            when (nalType) {
                NAL_TYPE_SPS -> {
                    lastNalType = "SPS (${data.size}B)"
                    spsBuffer = data.copyOf()
                    // Submit SPS as codec configuration data to MediaCodec
                    queueInput(decoder, data, 0L, MediaCodec.BUFFER_FLAG_CODEC_CONFIG)
                    return
                }

                NAL_TYPE_PPS -> {
                    lastNalType = "PPS (${data.size}B)"
                    ppsBuffer = data.copyOf()
                    // Submit PPS as codec configuration data to MediaCodec
                    queueInput(decoder, data, 0L, MediaCodec.BUFFER_FLAG_CODEC_CONFIG)
                    return
                }

                NAL_TYPE_SEI -> {
                    lastNalType = "SEI (${data.size}B)"
                    return
                }

                NAL_TYPE_IDR -> {
                    lastNalType = "IDR (${data.size}B)"
                    hasReceivedKeyframe = true
                    queueInput(decoder, data, ptsUs, MediaCodec.BUFFER_FLAG_KEY_FRAME)
                    return
                }

                NAL_TYPE_NON_IDR -> {
                    lastNalType = "P (${data.size}B)"
                    if (!hasReceivedKeyframe) return
                    queueInput(decoder, data, ptsUs, 0)
                    return
                }

                else -> {
                    lastNalType = "Other (${data.size}B)"
                    val flags = if (isKeyframe) MediaCodec.BUFFER_FLAG_KEY_FRAME else 0
                    if (isKeyframe) {
                        hasReceivedKeyframe = true
                    } else if (!hasReceivedKeyframe) {
                        return
                    }
                    queueInput(decoder, data, ptsUs, flags)
                }
            }
        }
    }

    private fun queueInput(decoder: MediaCodec, data: ByteArray, presentationTimeUs: Long, flags: Int) {
        try {
            val inIndex = decoder.dequeueInputBuffer(TIMEOUT_US)
            if (inIndex >= 0) {
                val inputBuffer: ByteBuffer? = decoder.getInputBuffer(inIndex)
                inputBuffer?.clear()
                if (inputBuffer != null && data.size > inputBuffer.capacity()) {
                    lastError = "Frame too big: ${data.size} > ${inputBuffer.capacity()}"
                    Log.e(TAG, lastError!!)
                    return
                }
                inputBuffer?.put(data)
                decoder.queueInputBuffer(inIndex, 0, data.size, presentationTimeUs, flags)
                if (lastError == "Input buf full") {
                    lastError = null
                }
            } else {
                lastError = "Input buf full"
                Log.w(TAG, "Input buffer full, dropping frame")
            }
        } catch (e: Exception) {
            lastError = "Queue err: ${e.message}"
            Log.w(TAG, "Queue input error: ${e.message}")
        }
    }

    fun stop() {
        synchronized(lock) {
            isConfigured = false
            hasReceivedKeyframe = false
            spsBuffer = null
            ppsBuffer = null

            drainThread?.interrupt()
            drainThread = null

            try {
                codec?.stop()
                codec?.release()
            } catch (e: Exception) {
                Log.w(TAG, "Error stopping decoder: ${e.message}")
            } finally {
                codec = null
            }
        }
    }
}



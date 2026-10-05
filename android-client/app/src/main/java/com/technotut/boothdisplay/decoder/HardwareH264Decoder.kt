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
        private const val TIMEOUT_US = 5000L

        private const val NAL_TYPE_NON_IDR = 1
        private const val NAL_TYPE_IDR = 5
        private const val NAL_TYPE_SEI = 6
        private const val NAL_TYPE_SPS = 7
        private const val NAL_TYPE_PPS = 8
    }

    private val lock = Any()
    private var codec: MediaCodec? = null
    private var isConfigured = false
    private val bufferInfo = MediaCodec.BufferInfo()

    // Parameter sets cached for Access Unit reassembly and decoder initialization
    private var spsBuffer: ByteArray? = null
    private var ppsBuffer: ByteArray? = null
    private var hasReceivedKeyframe = false

    fun init(surface: Surface) {
        synchronized(lock) {
            stop()

            try {
                val format = MediaFormat.createVideoFormat(MIME_TYPE, width, height)

                // Only set KEY_LOW_LATENCY on Android 11+ (API 30+)
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                    try {
                        format.setInteger(MediaFormat.KEY_LOW_LATENCY, 1)
                    } catch (e: Exception) {
                        Log.w(TAG, "KEY_LOW_LATENCY not supported: ${e.message}")
                    }
                }
                // Only set KEY_PRIORITY on Android 6.0+ (API 23+)
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
                    try {
                        format.setInteger(MediaFormat.KEY_PRIORITY, 0) // Real-time priority
                    } catch (e: Exception) {
                        Log.w(TAG, "KEY_PRIORITY not supported: ${e.message}")
                    }
                }

                val decoder = MediaCodec.createDecoderByType(MIME_TYPE)
                decoder.configure(format, surface, null, 0)
                decoder.start()

                codec = decoder
                isConfigured = true
                hasReceivedKeyframe = false
                Log.i(TAG, "Hardware H.264 decoder successfully started ($width x $height)")
            } catch (e: Exception) {
                Log.e(TAG, "Failed to start MediaCodec: ${e.message}", e)
                stop()
            }
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

    fun decodeFrame(data: ByteArray, isKeyframe: Boolean, presentationTimeUs: Long) {
        synchronized(lock) {
            val decoder = codec ?: return
            if (!isConfigured) return

            val nalType = findNalType(data)

            when (nalType) {
                NAL_TYPE_SPS -> {
                    spsBuffer = data.copyOf()
                    // Queue codec-specific configuration data to MediaCodec
                    queueInput(decoder, data, presentationTimeUs, MediaCodec.BUFFER_FLAG_CODEC_CONFIG)
                    drainOutput(decoder, timeoutUs = 0)
                    return
                }

                NAL_TYPE_PPS -> {
                    ppsBuffer = data.copyOf()
                    // Queue codec-specific configuration data to MediaCodec
                    queueInput(decoder, data, presentationTimeUs, MediaCodec.BUFFER_FLAG_CODEC_CONFIG)
                    drainOutput(decoder, timeoutUs = 0)
                    return
                }

                NAL_TYPE_SEI -> {
                    // SEI contains non-picture metadata; do not feed as video frame
                    return
                }

                NAL_TYPE_IDR -> {
                    // IDR Keyframe: prepend SPS/PPS if available to ensure full Access Unit
                    val sps = spsBuffer
                    val pps = ppsBuffer
                    val payload = if (sps != null && pps != null) {
                        val combined = ByteArray(sps.size + pps.size + data.size)
                        System.arraycopy(sps, 0, combined, 0, sps.size)
                        System.arraycopy(pps, 0, combined, sps.size, pps.size)
                        System.arraycopy(data, 0, combined, sps.size + pps.size, data.size)
                        combined
                    } else {
                        data
                    }
                    hasReceivedKeyframe = true
                    queueInput(decoder, payload, presentationTimeUs, MediaCodec.BUFFER_FLAG_KEY_FRAME)
                    drainOutput(decoder, timeoutUs = 2000L)
                    return
                }

                NAL_TYPE_NON_IDR -> {
                    // Drop P-frames until the first keyframe is decoded to prevent corrupt artifacts
                    if (!hasReceivedKeyframe) return
                    queueInput(decoder, data, presentationTimeUs, 0)
                    drainOutput(decoder, timeoutUs = 2000L)
                    return
                }

                else -> {
                    // Fallback for non-standard / multi-NAL buffers
                    val flags = if (isKeyframe) MediaCodec.BUFFER_FLAG_KEY_FRAME else 0
                    if (isKeyframe) {
                        hasReceivedKeyframe = true
                    } else if (!hasReceivedKeyframe) {
                        return
                    }
                    queueInput(decoder, data, presentationTimeUs, flags)
                    drainOutput(decoder, timeoutUs = 2000L)
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
                inputBuffer?.put(data)
                decoder.queueInputBuffer(inIndex, 0, data.size, presentationTimeUs, flags)
            } else {
                Log.w(TAG, "Input buffer full, dropping frame")
            }
        } catch (e: Exception) {
            Log.w(TAG, "Queue input error: ${e.message}")
        }
    }

    private fun drainOutput(decoder: MediaCodec, timeoutUs: Long) {
        try {
            while (true) {
                val outIndex = decoder.dequeueOutputBuffer(bufferInfo, timeoutUs)
                when {
                    outIndex >= 0 -> {
                        // Direct hardware render to SurfaceView
                        decoder.releaseOutputBuffer(outIndex, true)
                    }
                    outIndex == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED -> {
                        Log.i(TAG, "Output format changed: ${decoder.outputFormat}")
                    }
                    outIndex == MediaCodec.INFO_OUTPUT_BUFFERS_CHANGED -> {
                        Log.i(TAG, "Output buffers changed")
                    }
                    outIndex == MediaCodec.INFO_TRY_AGAIN_LATER -> {
                        return
                    }
                    else -> {
                        return
                    }
                }
            }
        } catch (e: Exception) {
            Log.w(TAG, "Drain output error: ${e.message}")
        }
    }

    fun stop() {
        synchronized(lock) {
            isConfigured = false
            hasReceivedKeyframe = false
            spsBuffer = null
            ppsBuffer = null
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


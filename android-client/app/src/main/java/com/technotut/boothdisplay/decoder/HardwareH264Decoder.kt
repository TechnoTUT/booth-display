package com.technotut.boothdisplay.decoder

import android.media.MediaCodec
import android.media.MediaFormat
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
    }

    private var codec: MediaCodec? = null
    private var isConfigured = false
    private val bufferInfo = MediaCodec.BufferInfo()

    fun init(surface: Surface) {
        stop()

        try {
            val format = MediaFormat.createVideoFormat(MIME_TYPE, width, height).apply {
                // Low latency decoder flags
                setInteger(MediaFormat.KEY_LOW_LATENCY, 1)
                setInteger(MediaFormat.KEY_PRIORITY, 0) // Real-time priority
            }

            val decoder = MediaCodec.createDecoderByType(MIME_TYPE)
            decoder.configure(format, surface, null, 0)
            decoder.start()

            codec = decoder
            isConfigured = true
            Log.i(TAG, "Hardware H.264 decoder successfully started ($width x $height)")
        } catch (e: Exception) {
            Log.e(TAG, "Failed to start MediaCodec: ${e.message}", e)
            stop()
        }
    }

    fun decodeFrame(data: ByteArray, isKeyframe: Boolean, presentationTimeUs: Long) {
        val decoder = codec ?: return
        if (!isConfigured) return

        try {
            // 1. Submit input frame data to MediaCodec
            val inIndex = decoder.dequeueInputBuffer(TIMEOUT_US)
            if (inIndex >= 0) {
                val inputBuffer: ByteBuffer? = decoder.getInputBuffer(inIndex)
                inputBuffer?.clear()
                inputBuffer?.put(data)

                val flags = if (isKeyframe) MediaCodec.BUFFER_FLAG_KEY_FRAME else 0
                decoder.queueInputBuffer(inIndex, 0, data.size, presentationTimeUs, flags)
            }

            // 2. Render decoded frames immediately to Surface
            var outIndex = decoder.dequeueOutputBuffer(bufferInfo, 0)
            while (outIndex >= 0) {
                // true = render output directly to surface at hardware level
                decoder.releaseOutputBuffer(outIndex, true)
                outIndex = decoder.dequeueOutputBuffer(bufferInfo, 0)
            }
        } catch (e: Exception) {
            Log.w(TAG, "Decode error: ${e.message}")
        }
    }

    fun stop() {
        isConfigured = false
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

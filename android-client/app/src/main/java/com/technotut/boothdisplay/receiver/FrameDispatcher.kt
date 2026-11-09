package com.technotut.boothdisplay.receiver

import android.os.Process
import android.util.Log
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.TimeUnit

/**
 * Hands assembled frames from the UDP receive thread to a dedicated decode thread.
 *
 * Decoding (NAL scanning, waiting for a MediaCodec input buffer) used to run on the
 * receive thread. While it blocked, packets piled up in the kernel socket buffer
 * and overflowed it on any decoder hiccup. Now the receive thread only enqueues.
 *
 * If the decoder falls a whole queue behind, the backlog is discarded and P-frames
 * are skipped until the next keyframe, so the decoder never references a frame it
 * did not get and the picture does not lag further and further behind.
 */
class FrameDispatcher(
    private val onFrame: (payload: ByteArray, isKeyframe: Boolean, timestamp: Long) -> Unit
) {
    companion object {
        private const val TAG = "FrameDispatcher"
        private const val QUEUE_CAPACITY = 30 // ~1 s at 30 fps
    }

    private class Frame(val payload: ByteArray, val isKeyframe: Boolean, val timestamp: Long)

    private val queue = ArrayBlockingQueue<Frame>(QUEUE_CAPACITY)

    @Volatile
    private var running = false
    private var thread: Thread? = null

    // Only touched by offer() (the receive thread)
    private var needKeyframe = false

    @Volatile
    var droppedFrames: Long = 0L
        private set

    fun start() {
        if (running) return
        running = true
        thread = Thread({
            try {
                Process.setThreadPriority(Process.THREAD_PRIORITY_URGENT_DISPLAY)
            } catch (e: Exception) {
                Log.w(TAG, "Failed to set URGENT_DISPLAY priority: ${e.message}")
            }
            while (running) {
                val frame = try {
                    queue.poll(100, TimeUnit.MILLISECONDS)
                } catch (e: InterruptedException) {
                    break
                } ?: continue
                try {
                    onFrame(frame.payload, frame.isKeyframe, frame.timestamp)
                } catch (e: Exception) {
                    Log.w(TAG, "Frame handler error: ${e.message}")
                }
            }
        }, "FrameDispatchThread").apply {
            priority = Thread.MAX_PRIORITY
            start()
        }
    }

    /** Called from the receive thread only. Never blocks. */
    fun offer(payload: ByteArray, isKeyframe: Boolean, timestamp: Long) {
        if (needKeyframe && !isKeyframe) {
            droppedFrames++
            return
        }
        val frame = Frame(payload, isKeyframe, timestamp)
        if (queue.offer(frame)) {
            needKeyframe = false
            return
        }

        // Decoder is a full queue behind: discard the backlog.
        val discarded = queue.size
        queue.clear()
        droppedFrames += discarded
        if (isKeyframe) {
            queue.offer(frame)
            needKeyframe = false
        } else {
            droppedFrames++
            needKeyframe = true
        }
        Log.w(TAG, "Decoder fell behind, discarded $discarded queued frames")
    }

    fun stop() {
        running = false
        thread?.interrupt()
        thread = null
        queue.clear()
    }
}

package com.technotut.boothdisplay

import android.app.Activity
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.view.View
import android.view.WindowManager
import android.widget.TextView
import com.technotut.boothdisplay.decoder.HardwareH264Decoder
import com.technotut.boothdisplay.protocol.ClockSync
import com.technotut.boothdisplay.protocol.FramePool
import com.technotut.boothdisplay.protocol.StreamStats
import com.technotut.boothdisplay.receiver.UdpStreamReceiver

class MainActivity : Activity(), SurfaceHolder.Callback {

    companion object {
        /** logcat tag of the per-second figures: adb logcat -s BoothStats */
        private const val STATS_TAG = "BoothStats"
        private const val STREAM_TIMEOUT_NS = 5_000_000_000L

        /** Frames being assembled, waiting for the decoder and being copied in. */
        private const val FRAME_POOL_SIZE = 8
        private const val FRAME_CAPACITY = 1024 * 1024
    }

    private lateinit var surfaceView: SurfaceView
    private lateinit var osdOverlay: View
    private lateinit var textStatus: TextView
    private lateinit var textMetrics: TextView
    private lateinit var textDiagnostics: TextView
    private lateinit var layoutConnectionBadge: View
    private lateinit var dotStatus: View
    private lateinit var textConnectionStatus: TextView

    private val pool = FramePool(FRAME_POOL_SIZE, FRAME_CAPACITY)
    private val stats = StreamStats()
    private val clock = ClockSync()
    private val decoder = HardwareH264Decoder(width = 1920, height = 540, pool = pool, stats = stats, clock = clock)
    private var receiver: UdpStreamReceiver? = null

    private val mainHandler = Handler(Looper.getMainLooper())
    private var isStreamingActive = false

    /** Once a second: logs the figures and refreshes the OSD. */
    private val statsTicker = object : Runnable {
        override fun run() {
            updateStats()
            mainHandler.postDelayed(this, 1000)
        }
    }

    // Configurable listen port (default: 8554)
    private var listenPort = 8554

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // 1. Keep Screen Awake & Immersive Fullscreen
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        setContentView(R.layout.activity_main)

        surfaceView = findViewById(R.id.surfaceView)
        surfaceView.setZOrderMediaOverlay(true)
        surfaceView.holder.setFormat(android.graphics.PixelFormat.OPAQUE)

        osdOverlay = findViewById(R.id.osdOverlay)
        textStatus = findViewById(R.id.textStatus)
        textMetrics = findViewById(R.id.textMetrics)
        textDiagnostics = findViewById(R.id.textDiagnostics)
        layoutConnectionBadge = findViewById(R.id.layoutConnectionBadge)
        dotStatus = findViewById(R.id.dotStatus)
        textConnectionStatus = findViewById(R.id.textConnectionStatus)

        updateConnectionStatus(false)

        // Parse optional intent extras: e.g. adb shell am start --ei port 8555
        parseIntentExtras(intent)

        surfaceView.holder.addCallback(this)

        // Hide navigation/status bar
        applyImmersiveMode()

        // Toggle OSD visibility on user screen tap (shows for 5s then hides if streaming)
        surfaceView.setOnClickListener {
            val isVisible = osdOverlay.visibility == View.VISIBLE
            if (isVisible) {
                osdOverlay.visibility = View.GONE
            } else {
                osdOverlay.visibility = View.VISIBLE
                if (isStreamingActive) {
                    mainHandler.postDelayed({
                        if (isStreamingActive) {
                            osdOverlay.visibility = View.GONE
                        }
                    }, 5000)
                }
            }
            applyImmersiveMode()
        }
    }

    override fun onResume() {
        super.onResume()
        applyImmersiveMode()
    }

    override fun onNewIntent(intent: android.content.Intent?) {
        super.onNewIntent(intent)
        setIntent(intent)
        val oldPort = listenPort
        parseIntentExtras(intent)
        if (oldPort != listenPort) {
            startReceiver()
        }
    }

    private fun parseIntentExtras(intent: android.content.Intent?) {
        if (intent == null) return
        val portExtra = intent.getIntExtra("port", -1)
        if (portExtra in 1..65535) {
            listenPort = portExtra
        }
        textStatus.text = "UDP :$listenPort"
    }

    private fun updateConnectionStatus(isOnline: Boolean) {
        if (isOnline) {
            layoutConnectionBadge.setBackgroundResource(R.drawable.bg_badge_online)
            dotStatus.setBackgroundResource(R.drawable.dot_online)
            textConnectionStatus.text = "Online"
            textConnectionStatus.setTextColor(android.graphics.Color.parseColor("#059669"))
        } else {
            layoutConnectionBadge.setBackgroundResource(R.drawable.bg_badge_offline)
            dotStatus.setBackgroundResource(R.drawable.dot_offline)
            textConnectionStatus.text = "Offline"
            textConnectionStatus.setTextColor(android.graphics.Color.parseColor("#E11D48"))
        }
    }

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (hasFocus) {
            applyImmersiveMode()
        }
    }

    private fun applyImmersiveMode() {
        window.decorView.systemUiVisibility = (
            View.SYSTEM_UI_FLAG_LAYOUT_STABLE
            or View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION
            or View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN
            or View.SYSTEM_UI_FLAG_HIDE_NAVIGATION
            or View.SYSTEM_UI_FLAG_FULLSCREEN
            or View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY
        )
    }

    // --- SurfaceHolder.Callback ---

    override fun surfaceCreated(holder: SurfaceHolder) {
        // Initialize MediaCodec with direct Hardware Surface
        decoder.init(holder.surface)

        // Start UDP Receiver
        startReceiver()
    }

    override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) {}

    override fun surfaceDestroyed(holder: SurfaceHolder) {
        stopReceiver()
        decoder.stop()
    }

    private fun startReceiver() {
        stopReceiver()
        receiver = UdpStreamReceiver(listenPort, pool, stats, clock, decoder::submit).apply { start() }
        stats.snapshot(System.nanoTime(), clock.rttUs) // start a fresh window
        mainHandler.postDelayed(statsTicker, 1000)
    }

    private fun updateStats() {
        val r = receiver ?: return
        val now = System.nanoTime()
        val snap = stats.snapshot(now, clock.rttUs)
        val streaming = r.lastFrameNs != 0L && now - r.lastFrameNs < STREAM_TIMEOUT_NS
        if (streaming) Log.i(STATS_TAG, snap.logLine())

        textStatus.text = "UDP :$listenPort"
        if (streaming != isStreamingActive) {
            isStreamingActive = streaming
            updateConnectionStatus(streaming)
        }
        val err = decoder.lastError ?: r.lastError
        val red = android.graphics.Color.parseColor("#EF4444")
        val green = android.graphics.Color.parseColor("#10B981")
        val amber = android.graphics.Color.parseColor("#F59E0B")
        val grey = android.graphics.Color.parseColor("#64748B")
        when {
            r.wrongVersionPackets > 0 && !streaming -> {
                textMetrics.text = "Protocol mismatch"
                textMetrics.setTextColor(red)
                textDiagnostics.text = "Packets of another protocol version: ${r.wrongVersionPackets} (update the controller or this app)"
                textDiagnostics.setTextColor(red)
            }
            err != null -> {
                textMetrics.text = "Decoder Error"
                textMetrics.setTextColor(red)
                textDiagnostics.text = "$err | Rx: ${r.packetCount}"
                textDiagnostics.setTextColor(red)
            }
            !streaming -> {
                textMetrics.text = "Waiting for stream..."
                textMetrics.setTextColor(grey)
                textDiagnostics.text = "Rx: ${r.packetCount} pkts | RcvBuf: ${r.receiveBufferBytes / 1024} KB | Ready"
                textDiagnostics.setTextColor(grey)
            }
            decoder.renderedFrameCount == 0L -> {
                textMetrics.text = String.format("%.1f FPS (Waiting IDR)", snap.fps)
                textMetrics.setTextColor(amber)
                textDiagnostics.text = snap.logLine()
                textDiagnostics.setTextColor(amber)
            }
            else -> {
                val e2e = snap.e2e?.let { String.format(" | %.0f ms", it[0]) } ?: ""
                textMetrics.text = String.format("%.1f FPS%s", snap.fps, e2e)
                textMetrics.setTextColor(green)
                textDiagnostics.text = "${snap.logLine()} | ${decoder.description}"
                textDiagnostics.setTextColor(green)
            }
        }
    }

    private fun stopReceiver() {
        mainHandler.removeCallbacks(statsTicker)
        receiver?.stop()
        receiver = null
    }

    override fun onDestroy() {
        super.onDestroy()
        stopReceiver()
        decoder.stop()
    }
}

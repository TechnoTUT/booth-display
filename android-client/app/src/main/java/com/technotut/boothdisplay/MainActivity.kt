package com.technotut.boothdisplay

import android.app.Activity
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.view.View
import android.view.WindowManager
import android.widget.TextView
import com.technotut.boothdisplay.decoder.HardwareH264Decoder
import com.technotut.boothdisplay.receiver.UdpStreamReceiver

class MainActivity : Activity(), SurfaceHolder.Callback {

    private lateinit var surfaceView: SurfaceView
    private lateinit var osdOverlay: View
    private lateinit var textStatus: TextView
    private lateinit var textMetrics: TextView
    private lateinit var textDiagnostics: TextView

    private val decoder = HardwareH264Decoder(width = 1920, height = 540)
    private var receiver: UdpStreamReceiver? = null

    private val mainHandler = Handler(Looper.getMainLooper())
    private var frameCount = 0
    private var totalFrames = 0L
    private var lastFpsCalculationTime = System.currentTimeMillis()
    private var isStreamingActive = false

    // 5-second stream timeout runnable to restore OSD standby view
    private val streamTimeoutRunnable = Runnable {
        isStreamingActive = false
        textStatus.text = "UDP :$listenPort"
        textMetrics.text = "Waiting for stream..."
        textMetrics.setTextColor(android.graphics.Color.parseColor("#94A3B8"))
        val err = receiver?.lastError
        if (err != null) {
            textDiagnostics.text = err
            textDiagnostics.setTextColor(android.graphics.Color.parseColor("#EF4444"))
        } else {
            textDiagnostics.text = "Rx: ${receiver?.packetCount ?: 0} pkts | Waiting..."
            textDiagnostics.setTextColor(android.graphics.Color.parseColor("#64748B"))
        }
        osdOverlay.visibility = View.VISIBLE
    }

    // Configurable listen port (default: 8554)
    private var listenPort = 8554

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // 1. Keep Screen Awake & Immersive Fullscreen
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        setContentView(R.layout.activity_main)

        surfaceView = findViewById(R.id.surfaceView)
        osdOverlay = findViewById(R.id.osdOverlay)
        textStatus = findViewById(R.id.textStatus)
        textMetrics = findViewById(R.id.textMetrics)
        textDiagnostics = findViewById(R.id.textDiagnostics)

        // Parse optional intent extras: e.g. adb shell am start --ei port 8555 --es display_id DISPLAY-2
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
        val displayExtra = intent.getStringExtra("display_id")
        if (!displayExtra.isNullOrEmpty()) {
            val badge: TextView? = findViewById(R.id.textBadge)
            badge?.text = displayExtra.uppercase()
        }
        textStatus.text = "UDP :$listenPort"
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

        receiver = UdpStreamReceiver(port = listenPort) { frame, isKeyframe, timestamp ->
            decoder.decodeFrame(frame, isKeyframe, timestamp * 1000L)

            frameCount++
            val now = System.currentTimeMillis()

            mainHandler.post {
                // Cancel existing timeout timer & reschedule for 5.0 seconds
                mainHandler.removeCallbacks(streamTimeoutRunnable)
                mainHandler.postDelayed(streamTimeoutRunnable, 5000)

                totalFrames++
                val rxPkts = receiver?.packetCount ?: 0L

                // First frame arrived: keep OSD visible for 4s so user can verify status, then hide
                if (!isStreamingActive) {
                    isStreamingActive = true
                    textDiagnostics.text = "Rx: $rxPkts pkts | Dec: $totalFrames f"
                    mainHandler.postDelayed({
                        if (isStreamingActive) {
                            osdOverlay.visibility = View.GONE
                        }
                    }, 4000)
                }

                if (now - lastFpsCalculationTime >= 1000) {
                    val fps = frameCount.toDouble() * 1000.0 / (now - lastFpsCalculationTime)
                    frameCount = 0
                    lastFpsCalculationTime = now

                    textStatus.text = "UDP :$listenPort"
                    textMetrics.text = String.format("%.1f FPS", fps)
                    textMetrics.setTextColor(android.graphics.Color.parseColor("#10B981"))
                    textDiagnostics.text = "Rx: $rxPkts pkts | Dec: $totalFrames f"
                }
            }
        }.apply {
            start()
        }
    }

    private fun stopReceiver() {
        mainHandler.removeCallbacks(streamTimeoutRunnable)
        receiver?.stop()
        receiver = null
    }

    override fun onDestroy() {
        super.onDestroy()
        stopReceiver()
        decoder.stop()
    }
}

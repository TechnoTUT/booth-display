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

    private val decoder = HardwareH264Decoder(width = 1920, height = 540)
    private var receiver: UdpStreamReceiver? = null

    private val mainHandler = Handler(Looper.getMainLooper())
    private var frameCount = 0
    private var lastFpsCalculationTime = System.currentTimeMillis()

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

        surfaceView.holder.addCallback(this)

        // Hide navigation/status bar
        applyImmersiveMode()

        // Toggle OSD visibility on tap
        surfaceView.setOnClickListener {
            val isVisible = osdOverlay.visibility == View.VISIBLE
            osdOverlay.visibility = if (isVisible) View.GONE else View.VISIBLE
            applyImmersiveMode()
        }

        // Auto-hide OSD after 8 seconds
        mainHandler.postDelayed({
            osdOverlay.visibility = View.GONE
        }, 8000)
    }

    override fun onResume() {
        super.onResume()
        applyImmersiveMode()
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
            if (now - lastFpsCalculationTime >= 1000) {
                val fps = frameCount.toDouble() * 1000.0 / (now - lastFpsCalculationTime)
                frameCount = 0
                lastFpsCalculationTime = now

                mainHandler.post {
                    textStatus.text = "UDP :$listenPort"
                    textMetrics.text = String.format("%.1f FPS", fps)
                }
            }
        }.apply {
            start()
        }
    }

    private fun stopReceiver() {
        receiver?.stop()
        receiver = null
    }

    override fun onDestroy() {
        super.onDestroy()
        stopReceiver()
        decoder.stop()
    }
}

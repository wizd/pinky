package app.pinky.companion

import android.graphics.PixelFormat
import android.media.MediaCodec
import android.media.MediaFormat
import android.net.LocalSocket
import android.os.Handler
import android.os.Looper
import android.text.TextUtils
import android.util.Log
import android.util.TypedValue
import android.view.Gravity
import android.view.MotionEvent
import android.view.Surface
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStreamReader
import java.io.OutputStreamWriter
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger

/**
 * The floating window of a task that runs on the background screen
 * (BackgroundScreen): the live picture of that hidden display, from the
 * core's video command, with Larger/Smaller, Take over or Hand back, and
 * Stop. Tapping the picture while Pinky works takes the task over; once
 * the person has it, their touches on the picture go to the hidden display
 * (the core's touch command), and Hand back gives it back.
 *
 * Drawn by the accessibility service with Pinky's other controls (Overlays),
 * hidden from agents and deaf to the gestures they inject like them.
 */
class AgentWindow(private val service: PinkyAccessibilityService, private val o: Overlays, private val changed: () -> Unit) {

    private val wm = service.getSystemService(WindowManager::class.java)
    private val main = Handler(Looper.getMainLooper())

    private var root: LinearLayout? = null
    private var picture: SurfaceView? = null
    private var takeButton: TextView? = null
    private var sizeButton: TextView? = null
    private var label: TextView? = null
    private var lp: WindowManager.LayoutParams? = null

    private var large = false
    private var largeBefore = false // the size before the person took over
    private var videoW = 0
    private var videoH = 0
    private var yours = false // the person has the task (task.state "paused")
    private var video: Video? = null

    // The task whose display turned out to be the phone's own screen: no
    // window for it.
    private var ownScreenTask: String? = null
    private var task: String? = null

    // The person's touches, on one connection kept open, in order.
    private val touchIo = Executors.newSingleThreadExecutor()
    private var touchConn: Conn? = null // on touchIo only
    private val moves = AtomicInteger()
    private var gestureTakes = false // this gesture took the task over: it goes nowhere else

    /** The window's view while it shows, for OwnUI. */
    val view: View? get() = root

    /**
     * Follows the state: shown while a task runs on the background screen.
     * taskId names the task, paused says the person has it.
     */
    fun update(show: Boolean, taskId: String?, paused: Boolean) {
        if (taskId != task) {
            task = taskId
            ownScreenTask = null
        }
        if (!show || (taskId != null && taskId == ownScreenTask)) {
            hide()
            return
        }
        val w = root ?: create()
        if (paused != yours) {
            yours = paused
            // Taking over means working on the picture: make it large, and
            // as it was again once the person hands the task back.
            if (paused) {
                largeBefore = large
                large = true
            } else {
                large = largeBefore
            }
            layout()
        }
        takeButton?.text = service.getString(if (yours) R.string.phone_bg_hand_back else R.string.phone_bg_take_over)
        label?.text = service.getString(if (yours) R.string.phone_bg_window_yours else R.string.phone_bg_window_working)
        w.visibility = View.VISIBLE
    }

    fun hide() {
        val r = root ?: return
        stopVideo()
        try {
            wm.removeViewImmediate(r)
        } catch (_: Exception) {
        }
        root = null
        picture = null
        takeButton = null
        sizeButton = null
        label = null
        lp = null
        yours = false
        large = false
        largeBefore = false
        changed()
    }

    fun close() {
        hide()
        touchIo.execute {
            touchConn?.close()
            touchConn = null
        }
        touchIo.shutdown()
    }

    private fun create(): LinearLayout {
        val r = LinearLayout(service).apply {
            orientation = LinearLayout.VERTICAL
            background = o.rounded(Overlays.SURFACE, 16f)
            setPadding(o.px(6), o.px(6), o.px(6), o.px(6))
        }
        val text = TextView(service).apply {
            setTextColor(Overlays.MUTED)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 12f)
            maxLines = 2
            ellipsize = TextUtils.TruncateAt.END
            setPadding(o.px(6), 0, o.px(6), o.px(4))
        }
        val sv = SurfaceView(service)
        sv.holder.addCallback(object : SurfaceHolder.Callback {
            override fun surfaceCreated(holder: SurfaceHolder) = startVideo(holder.surface)
            override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) {}
            override fun surfaceDestroyed(holder: SurfaceHolder) = stopVideo()
        })
        sv.setOnTouchListener { v, e -> touch(v, e) }
        val bar = LinearLayout(service).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.END
            setPadding(0, o.px(6), 0, 0)
        }
        val size = o.button(service.getString(R.string.phone_bg_window_larger)) {
            large = !large
            layout()
        }
        val take = o.button(service.getString(R.string.phone_bg_take_over)) { b ->
            b.isEnabled = false
            CoreClient.send(CoreClient.cmd(if (yours) "handback" else "takeover")) { main.post { b.isEnabled = true } }
        }
        val stop = o.button(service.getString(R.string.phone_stop), danger = true) { b ->
            b.isEnabled = false
            CoreClient.send(CoreClient.cmd("stop")) { err -> main.post { if (err != null) b.isEnabled = true } }
        }
        bar.addView(size)
        bar.addView(take)
        bar.addView(stop)
        r.addView(text)
        r.addView(sv)
        r.addView(bar)
        o.sensitive(r)

        val p = WindowManager.LayoutParams(
            ViewGroup.LayoutParams.WRAP_CONTENT,
            ViewGroup.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.TYPE_ACCESSIBILITY_OVERLAY,
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN,
            PixelFormat.TRANSLUCENT,
        )
        // The driver knows Pinky's own windows by this title (Overlays.add).
        p.title = service.packageName
        root = r
        picture = sv
        takeButton = take
        sizeButton = size
        label = text
        lp = p
        layout()
        r.addOnLayoutChangeListener { _, _, _, _, _, _, _, _, _ -> changed() }
        try {
            wm.addView(r, p)
        } catch (e: Exception) {
            Log.e(TAG, "could not show the floating window", e)
        }
        return r
    }

    // layout sizes the picture: small in the top corner, or large in the
    // middle, with the video's proportions.
    private fun layout() {
        val sv = picture ?: return
        val p = lp ?: return
        val dm = service.resources.displayMetrics
        var w = (dm.widthPixels * if (large) 0.86 else 0.34).toInt()
        var h = BackgroundScreen.heightFor(w, videoW, videoH)
        val maxH = (dm.heightPixels * if (large) 0.68 else 0.36).toInt()
        if (h > maxH) {
            w = w * maxH / h
            h = maxH
        }
        sv.layoutParams = LinearLayout.LayoutParams(w, h)
        // The label wraps within the picture's width, and the small
        // window, too narrow for three buttons, leaves out Take over:
        // tapping the picture takes over there.
        label?.layoutParams = LinearLayout.LayoutParams(w, ViewGroup.LayoutParams.WRAP_CONTENT)
        takeButton?.visibility = if (large) View.VISIBLE else View.GONE
        sizeButton?.text = service.getString(if (large) R.string.phone_bg_window_smaller else R.string.phone_bg_window_larger)
        p.gravity = if (large) Gravity.TOP or Gravity.CENTER_HORIZONTAL else Gravity.TOP or Gravity.END
        p.x = if (large) 0 else o.px(8)
        p.y = o.topInset() + o.px(56)
        root?.let { r ->
            if (r.isAttachedToWindow) {
                try {
                    wm.updateViewLayout(r, p)
                } catch (_: Exception) {
                }
            }
        }
    }

    // Touches on the picture.

    private fun touch(v: View, e: MotionEvent): Boolean {
        if (e.actionMasked == MotionEvent.ACTION_DOWN) {
            gestureTakes = !yours
            if (gestureTakes) {
                // The first tap only takes the task over: it does not
                // press what is under the finger.
                CoreClient.send(CoreClient.cmd("takeover"))
                return true
            }
        }
        if (gestureTakes || videoW <= 0) return true
        val action = when (e.actionMasked) {
            MotionEvent.ACTION_DOWN -> "down"
            MotionEvent.ACTION_MOVE -> "move"
            MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> "up" // the finger lifts either way
            else -> return true
        }
        val (x, y) = BackgroundScreen.toVideo(e.x, e.y, v.width, v.height, videoW, videoH)
        sendTouch(action, x, y)
        return true
    }

    private fun sendTouch(action: String, x: Int, y: Int) {
        val move = action == "move"
        // Moves that pile up are dropped; downs and ups never.
        if (move && moves.get() > 1) return
        if (move) moves.incrementAndGet()
        try {
            touchIo.execute {
                try {
                    val c = touchConn ?: Conn(CoreClient.connect()).also { touchConn = it }
                    val r = c.call(CoreClient.cmd("touch", "action" to action, "x" to x, "y" to y))
                    if (!r.optBoolean("ok")) Log.i(TAG, "touch refused: ${r.optString("error")}")
                } catch (e: Exception) {
                    Log.w(TAG, "touch failed", e)
                    touchConn?.close()
                    touchConn = null
                } finally {
                    if (move) moves.decrementAndGet()
                }
            }
        } catch (_: java.util.concurrent.RejectedExecutionException) {
            if (move) moves.decrementAndGet()
        }
    }

    private class Conn(val s: LocalSocket) {
        private val w = OutputStreamWriter(s.outputStream, Charsets.UTF_8)
        private val r = BufferedReader(InputStreamReader(s.inputStream, Charsets.UTF_8))

        fun call(req: JSONObject): JSONObject {
            w.write(req.toString())
            w.write("\n")
            w.flush()
            return JSONObject(r.readLine() ?: throw IllegalStateException("the core closed the connection"))
        }

        fun close() {
            try {
                s.close()
            } catch (_: Exception) {
            }
        }
    }

    // The picture.

    private fun startVideo(surface: Surface) {
        stopVideo()
        video = Video(surface).also { it.start() }
    }

    private fun stopVideo() {
        video?.close()
        video = null
    }

    private fun sized(w: Int, h: Int) {
        if (w == videoW && h == videoH) return
        videoW = w
        videoH = h
        layout()
    }

    private fun ownScreen() {
        ownScreenTask = task
        hide()
    }

    /** Plays the core's video on surface, and starts again while the window shows. */
    private inner class Video(private val surface: Surface) : Thread("PinkyAgentVideo") {

        @Volatile
        private var closed = false

        @Volatile
        private var socket: LocalSocket? = null

        override fun run() {
            while (!closed) {
                try {
                    play()
                } catch (e: Exception) {
                    if (!closed) Log.i(TAG, "video: ${e.message}")
                }
                if (closed) break
                try {
                    sleep(RETRY_MS)
                } catch (_: InterruptedException) {
                }
            }
        }

        private fun play() {
            val s = CoreClient.connect()
            socket = s
            var dec: MediaCodec? = null
            try {
                if (closed) return
                val out = s.outputStream
                out.write((CoreClient.cmd("video").toString() + "\n").toByteArray(Charsets.UTF_8))
                out.flush()
                val input = s.inputStream
                val r = JSONObject(BackgroundScreen.readLine(input))
                if (!r.optBoolean("ok")) {
                    if (r.optString("code") == "own_screen") {
                        closed = true
                        main.post { ownScreen() }
                    }
                    return // no_display: the task has not opened it yet
                }
                val v = r.getJSONObject("video")
                val w = v.getInt("width")
                val h = v.getInt("height")
                main.post { sized(w, h) }
                val mime = if (v.optString("codec") == "h265") MediaFormat.MIMETYPE_VIDEO_HEVC else MediaFormat.MIMETYPE_VIDEO_AVC
                val d = MediaCodec.createDecoderByType(mime)
                dec = d
                d.configure(MediaFormat.createVideoFormat(mime, w, h), surface, null, 0)
                d.start()
                val info = MediaCodec.BufferInfo()
                while (!closed) {
                    val f = BackgroundScreen.readFrame(input)
                    var i = d.dequeueInputBuffer(10_000)
                    while (i < 0 && !closed) {
                        drain(d, info)
                        i = d.dequeueInputBuffer(10_000)
                    }
                    if (i < 0) break
                    val buf = d.getInputBuffer(i) ?: continue
                    buf.clear()
                    if (f.data.size > buf.capacity()) {
                        d.queueInputBuffer(i, 0, 0, f.ptsUs, 0)
                        continue
                    }
                    buf.put(f.data)
                    val flags = when {
                        f.config -> MediaCodec.BUFFER_FLAG_CODEC_CONFIG
                        f.keyFrame -> MediaCodec.BUFFER_FLAG_KEY_FRAME
                        else -> 0
                    }
                    d.queueInputBuffer(i, 0, f.data.size, f.ptsUs, flags)
                    drain(d, info)
                }
            } finally {
                try {
                    dec?.stop()
                } catch (_: Exception) {
                }
                dec?.release()
                try {
                    s.close()
                } catch (_: Exception) {
                }
                socket = null
            }
        }

        private fun drain(d: MediaCodec, info: MediaCodec.BufferInfo) {
            while (true) {
                val o = d.dequeueOutputBuffer(info, 0)
                when {
                    o >= 0 -> d.releaseOutputBuffer(o, true)
                    o == MediaCodec.INFO_TRY_AGAIN_LATER -> return
                    else -> {} // the output's format or buffers changed
                }
            }
        }

        fun close() {
            closed = true
            try {
                socket?.close()
            } catch (_: Exception) {
            }
            interrupt()
        }
    }

    companion object {
        private const val TAG = "PinkyAgentWindow"
        private const val RETRY_MS = 1000L
    }
}

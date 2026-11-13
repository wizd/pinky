package app.pinky.companion

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.graphics.Outline
import android.graphics.PixelFormat
import android.graphics.Rect
import android.graphics.drawable.LayerDrawable
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.GestureDetector
import android.view.Gravity
import android.view.MotionEvent
import android.view.View
import android.view.ViewOutlineProvider
import android.view.WindowManager
import android.view.accessibility.AccessibilityWindowInfo
import android.widget.ImageView
import org.json.JSONObject
import java.io.Closeable
import kotlin.math.abs

/**
 * Pinky's floating button over other apps, after Mika's pill: off until
 * the person turns it on (Settings › This phone, or its Quick Settings
 * tile), and drawn by the accessibility service while Pinky runs tasks on
 * the phone (on-phone mode).
 *
 * - Double-tap asks Pinky about what the phone shows: the ask panel
 *   (AskActivity) opens over the app with a box for the question, and the
 *   task starts once the panel is gone, so that its first screen is the
 *   app's. The screen goes to the person's own model only, read through
 *   the gateway like any read: blocked apps, hidden codes and the rules
 *   apply.
 * - Long-press does the same with Android's own speech recognizer.
 * - A tap opens Pinky; a drag moves the button, which then keeps to the
 *   nearer edge.
 *
 * It hides while a task runs or Pinky on a computer uses the phone (the
 * Working pill shows then), and while Pinky's own screens are in front.
 * Like the pill, the agent can never press it: its rect goes to OwnUI,
 * its window carries the title the local driver knows Pinky's by, and its
 * view is sensitive. Overlays draws the pill and the card; this class is
 * apart from it on purpose.
 */
class QuickEntry(private val service: PinkyAccessibilityService) {

    private val wm = service.getSystemService(WindowManager::class.java)
    private val main = Handler(Looper.getMainLooper())
    private val dp = service.resources.displayMetrics.density
    private val prefs = service.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
    private var sub: Closeable? = null
    private var stopped = false

    private var view: ImageView? = null
    private var on = false // the person turned it on
    private var busy = true // a task runs or a computer uses the phone; true until the core says otherwise
    private var ownScreen = false
    private val checkOwnScreen = Runnable { updateOwnScreen() }

    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            on = QuickAccess.bubbleOn(service)
            update()
        }
    }

    fun start() {
        on = QuickAccess.bubbleOn(service)
        // Only this app sends it (QuickAccess.setBubble); older Androids ignore the flag.
        service.registerReceiver(receiver, IntentFilter(QuickAccess.ACTION_BUBBLE), Context.RECEIVER_NOT_EXPORTED)
        sub = CoreClient.subscribe(service, { st -> main.post { onState(st) } }, { main.post { onState(null) } })
    }

    fun stop() {
        stopped = true
        try {
            service.unregisterReceiver(receiver)
        } catch (_: Exception) {
        }
        sub?.close()
        sub = null
        main.removeCallbacks(checkOwnScreen)
        main.post { hide() }
    }

    /** Windows came, went or changed: the service calls it for their events. */
    fun windowsChanged() {
        if (!on) return
        main.removeCallbacks(checkOwnScreen)
        main.postDelayed(checkOwnScreen, 100)
    }

    private fun onState(st: JSONObject?) {
        if (stopped) return
        // Without the core, or without its phone, nothing could start.
        busy = st == null || st.optJSONObject("device")?.optBoolean("ready") != true ||
            st.optJSONObject("agent")?.optBoolean("running") == true || st.optJSONObject("computer") != null
        update()
    }

    private fun updateOwnScreen() {
        val own = pinkyInFront(service)
        if (own == ownScreen) return
        ownScreen = own
        update()
    }

    private fun update() {
        if (stopped) return
        if (on && view == null) ownScreen = pinkyInFront(service)
        if (on && !busy && !ownScreen) show() else hide()
    }

    // The button.

    private fun show() {
        if (view != null) return
        val size = px(SIZE_DP)
        val v = ImageView(service).apply {
            val bleed = -size / 4
            setImageDrawable(
                LayerDrawable(
                    arrayOf(service.getDrawable(R.drawable.ic_launcher_background), service.getDrawable(R.drawable.ic_launcher_foreground)),
                ).apply {
                    setLayerInset(0, bleed, bleed, bleed, bleed)
                    setLayerInset(1, bleed, bleed, bleed, bleed)
                },
            )
            scaleType = ImageView.ScaleType.FIT_XY
            outlineProvider = object : ViewOutlineProvider() {
                override fun getOutline(view: View, outline: Outline) = outline.setOval(0, 0, view.width, view.height)
            }
            clipToOutline = true
            elevation = px(4).toFloat()
            alpha = IDLE_ALPHA
            contentDescription = service.getString(R.string.phone_bubble)
        }
        Look.sensitive(v)
        val p = WindowManager.LayoutParams(
            size,
            size,
            WindowManager.LayoutParams.TYPE_ACCESSIBILITY_OVERLAY,
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN,
            PixelFormat.TRANSLUCENT,
        )
        p.gravity = Gravity.TOP or Gravity.START
        val metrics = service.resources.displayMetrics
        val right = prefs.getBoolean(KEY_RIGHT, true)
        p.x = if (right) metrics.widthPixels - size - px(EDGE_DP) else px(EDGE_DP)
        p.y = (metrics.heightPixels * prefs.getFloat(KEY_Y, 0.6f)).toInt().coerceIn(0, metrics.heightPixels - size)
        // The driver knows Pinky's own windows by this title (Overlays.add).
        p.title = service.packageName
        v.setOnTouchListener(Touch(v, p))
        v.addOnLayoutChangeListener { _, _, _, _, _, _, _, _, _ -> reportRect() }
        try {
            wm.addView(v, p)
        } catch (e: Exception) {
            Log.e(TAG, "could not show the floating button", e)
            return
        }
        view = v
    }

    private fun hide() {
        val v = view ?: return
        try {
            wm.removeViewImmediate(v)
        } catch (_: Exception) {
        }
        view = null
        service.ownUI.setBubble(emptyList())
    }

    private fun reportRect() {
        val v = view ?: return
        if (!v.isAttachedToWindow || v.width == 0) return
        val loc = IntArray(2)
        v.getLocationOnScreen(loc)
        service.ownUI.setBubble(listOf(Rect(loc[0], loc[1], loc[0] + v.width, loc[1] + v.height)))
    }

    /** Taps, a double-tap, a long-press and drags on the button. */
    private inner class Touch(private val v: View, private val p: WindowManager.LayoutParams) : View.OnTouchListener {
        private var downX = 0f
        private var downY = 0f
        private var startX = 0
        private var startY = 0
        private var dragging = false
        private val slop = px(8)

        private val gestures = GestureDetector(
            service,
            object : GestureDetector.SimpleOnGestureListener() {
                override fun onDown(e: MotionEvent) = true

                override fun onSingleTapConfirmed(e: MotionEvent): Boolean {
                    QuickAccess.open(
                        service,
                        Intent(service, TaskActivity::class.java)
                            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_REORDER_TO_FRONT),
                    )
                    return true
                }

                override fun onDoubleTap(e: MotionEvent): Boolean {
                    QuickAccess.open(service, QuickAccess.askIntent(service, screen = true))
                    return true
                }

                override fun onLongPress(e: MotionEvent) {
                    if (dragging) return
                    v.performHapticFeedback(android.view.HapticFeedbackConstants.LONG_PRESS)
                    QuickAccess.open(service, QuickAccess.askIntent(service, screen = true, voice = true))
                }
            },
        )

        override fun onTouch(view: View, e: MotionEvent): Boolean {
            when (e.actionMasked) {
                MotionEvent.ACTION_DOWN -> {
                    downX = e.rawX
                    downY = e.rawY
                    startX = p.x
                    startY = p.y
                    dragging = false
                    v.alpha = 1f
                }
                MotionEvent.ACTION_MOVE -> {
                    val dx = e.rawX - downX
                    val dy = e.rawY - downY
                    if (!dragging && (abs(dx) > slop || abs(dy) > slop)) {
                        dragging = true
                        // A drag is no tap: the detector forgets this touch.
                        gestures.onTouchEvent(MotionEvent.obtain(e).apply { action = MotionEvent.ACTION_CANCEL })
                    }
                    if (dragging) {
                        p.x = startX + dx.toInt()
                        p.y = startY + dy.toInt()
                        move()
                        return true
                    }
                }
                MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> {
                    v.alpha = IDLE_ALPHA
                    if (dragging) {
                        dragging = false
                        snap()
                        return true
                    }
                }
            }
            if (!dragging) gestures.onTouchEvent(e)
            return true
        }

        // To the nearer edge, where it stays (also when it shows again).
        private fun snap() {
            val metrics = service.resources.displayMetrics
            val right = p.x + v.width / 2 > metrics.widthPixels / 2
            p.x = if (right) metrics.widthPixels - v.width - px(EDGE_DP) else px(EDGE_DP)
            p.y = p.y.coerceIn(0, (metrics.heightPixels - v.height).coerceAtLeast(0))
            move()
            prefs.edit()
                .putBoolean(KEY_RIGHT, right)
                .putFloat(KEY_Y, p.y.toFloat() / metrics.heightPixels.coerceAtLeast(1))
                .apply()
        }

        private fun move() {
            try {
                wm.updateViewLayout(v, p)
            } catch (_: Exception) {
            }
        }
    }

    private fun px(d: Int) = (d * dp).toInt()

    companion object {
        private const val TAG = "PinkyQuick"
        private const val PREFS = "floating_button"
        private const val KEY_RIGHT = "right"
        private const val KEY_Y = "y"
        private const val SIZE_DP = 48
        private const val EDGE_DP = 4
        private const val IDLE_ALPHA = 0.85f

        /**
         * Whether the top application window on the phone's screen is one
         * of Pinky's own (as Overlays and OwnUI tell).
         */
        fun pinkyInFront(service: PinkyAccessibilityService): Boolean {
            val windows = try {
                service.windows
            } catch (_: Exception) {
                return false
            }
            return try {
                val top = windows.filter { it.type == AccessibilityWindowInfo.TYPE_APPLICATION }.maxByOrNull { it.layer }
                top?.root?.packageName?.toString() == service.packageName
            } catch (_: Exception) {
                false
            } finally {
                TreeDumper.recycleWindows(windows)
            }
        }
    }
}

package app.pinky.companion

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.graphics.Bitmap
import android.graphics.Path
import android.graphics.Point
import android.graphics.PointF
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.util.Base64
import android.view.Display
import android.view.accessibility.AccessibilityNodeInfo
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

/**
 * The commands that act on the phone without adb. Each runs on the
 * connection's thread and replies once Android says it is done.
 */
class Actions(private val service: PinkyAccessibilityService) {

    private val main = Handler(Looper.getMainLooper())
    private val shots = Executors.newSingleThreadExecutor()

    /**
     * gesture: strokes, each a list of points with a start and a duration,
     * all of one gesture: a tap is one point for 50 ms, a long press one
     * point for 800 ms, a swipe two points or more. Or segments, one finger
     * that moves through them one after another without lifting (see
     * [segments]). Refused, with nothing done, when a point lands on Pinky's
     * own controls.
     */
    fun gesture(req: JSONObject): String {
        val display = req.optInt("display", Display.DEFAULT_DISPLAY)
        service.displays.check(display)
        if (display != Display.DEFAULT_DISPLAY && Build.VERSION.SDK_INT < Build.VERSION_CODES.R) {
            throw RequestError("unsupported", "gestures on other displays need Android 11")
        }
        req.optJSONArray("segments")?.let { return segments(req, it, display) }
        val strokes = req.getJSONArray("strokes")
        if (strokes.length() == 0 || strokes.length() > GestureDescription.getMaxStrokeCount()) {
            throw RequestError("bad_request", "a gesture has 1 to ${GestureDescription.getMaxStrokeCount()} strokes")
        }
        val b = GestureDescription.Builder()
        if (display != Display.DEFAULT_DISPLAY) b.setDisplayId(display)
        val points = ArrayList<Point>()
        var longest = 0L
        for (i in 0 until strokes.length()) {
            val s = strokes.getJSONObject(i)
            val pts = points(s)
            val path = Path()
            pts.forEachIndexed { j, p -> if (j == 0) path.moveTo(p.x, p.y) else path.lineTo(p.x, p.y) }
            pts.mapTo(points) { Point(it.x.toInt(), it.y.toInt()) }
            val start = s.optLong("start_ms", 0).coerceAtLeast(0)
            val dur = s.optLong("duration_ms", TAP_MS).coerceIn(1, GestureDescription.getMaxGestureDuration())
            longest = maxOf(longest, start + dur)
            b.addStroke(GestureDescription.StrokeDescription(path, start, dur))
        }
        service.ownUI.check(points, display)

        val t0 = SystemClock.uptimeMillis()
        val done = CountDownLatch(1)
        var completed = false
        main.post { dropHeld() } // Android cancels a held finger for a new gesture
        val sent = service.dispatchGesture(b.build(), object : AccessibilityService.GestureResultCallback() {
            override fun onCompleted(gestureDescription: GestureDescription?) {
                completed = true
                done.countDown()
            }

            override fun onCancelled(gestureDescription: GestureDescription?) {
                done.countDown()
            }
        }, main)
        if (!sent) throw RequestError("action_failed", "Android refused the gesture")
        if (!done.await(longest + GESTURE_SLACK_MS, TimeUnit.MILLISECONDS)) {
            throw RequestError("timeout", "the gesture did not finish")
        }
        if (!completed) {
            throw RequestError("cancelled", "the gesture was cancelled, for example by a touch on the screen")
        }
        return took(t0)
    }

    // A finger an earlier segments request left down (hold), on the main
    // thread only.
    private class Held(val display: Int, val stroke: GestureDescription.StrokeDescription, val at: PointF)

    private var held: Held? = null
    private val liftHeld = Runnable { lift() }

    // Counts gestures, so a segments request that waits out a rest knows
    // whether another gesture came meanwhile. Main thread only.
    private var heldGen = 0

    /**
     * Segments: one finger moves through them in order without lifting,
     * each a list of points it moves along at an even speed in duration_ms;
     * a segment of one point rests there. Android plays each segment as a
     * stroke that continues the one before, so a drag can rest on its way
     * and a person's touch can follow their pointer. hold leaves the finger
     * down after the last segment; the next request with continue goes on
     * from where it rests (its first point moves there from the rest). A
     * held finger lifts when another gesture comes or after HOLD_MAX_MS.
     *
     * Android sends nothing for a stroke that continues without moving: it
     * reports one that only puts the finger down as done at once and
     * cancels one that neither puts it down nor moves it. So a rest that
     * the finger does not lift after is not played: the next move starts
     * that much later instead, and a rest at the end of a request that
     * holds the finger is waited out here before the reply.
     */
    private fun segments(req: JSONObject, arr: JSONArray, display: Int): String {
        if (arr.length() == 0 || arr.length() > MAX_SEGMENTS) {
            throw RequestError("bad_request", "segments: 1 to $MAX_SEGMENTS")
        }
        val cont = req.optBoolean("continue")
        val hold = req.optBoolean("hold")
        val segs = (0 until arr.length()).map { i ->
            val s = arr.getJSONObject(i)
            Segment(points(s), s.optLong("duration_ms", TAP_MS).coerceIn(1, GestureDescription.getMaxGestureDuration()))
        }
        val total = segs.sumOf { it.duration }
        if (total > MAX_SEGMENTS_MS) throw RequestError("bad_request", "segments last at most $MAX_SEGMENTS_MS ms")
        service.ownUI.check(segs.flatMap { s -> s.points.map { Point(it.x.toInt(), it.y.toInt()) } }, display)

        val t0 = SystemClock.uptimeMillis()
        val done = CountDownLatch(1)
        val failure = AtomicReference<RequestError?>()
        fun fail(e: RequestError) {
            failure.compareAndSet(null, e)
            done.countDown()
        }
        main.post {
            main.removeCallbacks(liftHeld)
            val gen = ++heldGen
            val h = held
            held = null
            if (cont && (h == null || h.display != display)) {
                fail(RequestError("not_held", "no finger is down on display $display"))
                return@post
            }
            var prev: GestureDescription.StrokeDescription? = if (cont) h!!.stroke else null
            var at: PointF? = if (cont) h!!.at else null
            var rest = 0L // how long the finger still rests at `at` before it moves on
            fun finish() {
                if (gen != heldGen) {
                    fail(RequestError("cancelled", "another gesture came while the finger rested"))
                    return
                }
                if (hold) {
                    held = Held(display, prev!!, at!!)
                    main.postDelayed(liftHeld, HOLD_MAX_MS)
                }
                done.countDown()
            }
            fun play(i: Int) {
                if (i == segs.size) {
                    if (rest > 0) main.postDelayed({ finish() }, rest) else finish()
                    return
                }
                val s = segs[i]
                val from = at ?: s.points[0]
                val still = s.points.all { it.x == from.x && it.y == from.y }
                val more = i < segs.size - 1 || hold
                val p = prev
                if (p != null && still && more) {
                    rest += s.duration
                    play(i + 1)
                    return
                }
                val path = Path()
                path.moveTo(from.x, from.y)
                for (q in if (at == null) s.points.drop(1) else s.points) path.lineTo(q.x, q.y)
                val stroke = try {
                    when {
                        p == null -> GestureDescription.StrokeDescription(path, 0, s.duration, more)
                        still -> p.continueStroke(path, 0, rest + s.duration, false) // rests, then lifts
                        else -> p.continueStroke(path, rest, s.duration, more)
                    }
                } catch (e: IllegalArgumentException) {
                    fail(RequestError("bad_request", e.message ?: "bad segment"))
                    return
                } catch (e: IllegalStateException) {
                    fail(RequestError("bad_request", e.message ?: "bad segment"))
                    return
                }
                // Android reports a first touch that stays and continues as
                // done once the finger is down.
                val restAfter = if (p == null && still && more) s.duration else 0L
                val b = GestureDescription.Builder().addStroke(stroke)
                if (display != Display.DEFAULT_DISPLAY) b.setDisplayId(display)
                val sent = service.dispatchGesture(b.build(), object : AccessibilityService.GestureResultCallback() {
                    override fun onCompleted(gestureDescription: GestureDescription?) {
                        prev = stroke
                        at = s.points.last()
                        rest = restAfter
                        play(i + 1)
                    }

                    override fun onCancelled(gestureDescription: GestureDescription?) {
                        fail(RequestError("cancelled", "the gesture was cancelled, for example by a touch on the screen"))
                    }
                }, main)
                if (!sent) fail(RequestError("action_failed", "Android refused the gesture"))
            }
            play(0)
        }
        if (!done.await(total + GESTURE_SLACK_MS, TimeUnit.MILLISECONDS)) {
            throw RequestError("timeout", "the gesture did not finish")
        }
        failure.get()?.let { throw it }
        return took(t0)
    }

    private class Segment(val points: List<PointF>, val duration: Long)

    /** Lifts a held finger where it rests. Main thread. */
    private fun lift() {
        val h = held ?: return
        held = null
        val path = Path().apply { moveTo(h.at.x, h.at.y) }
        val b = GestureDescription.Builder().addStroke(h.stroke.continueStroke(path, 0, 1, false))
        if (h.display != Display.DEFAULT_DISPLAY) b.setDisplayId(h.display)
        service.dispatchGesture(b.build(), null, null)
    }

    /** Forgets a held finger without lifting it. Main thread. */
    private fun dropHeld() {
        main.removeCallbacks(liftHeld)
        heldGen++
        held = null
    }

    /** A stroke's or segment's points, checked. */
    private fun points(s: JSONObject): List<PointF> {
        val pts = s.getJSONArray("points")
        if (pts.length() == 0) throw RequestError("bad_request", "a stroke needs a point")
        return (0 until pts.length()).map { j ->
            val p = pts.getJSONArray(j)
            val x = p.getDouble(0).toFloat()
            val y = p.getDouble(1).toFloat()
            if (x < 0 || y < 0) throw RequestError("bad_request", "point ($x, $y) is off the screen")
            PointF(x, y)
        }
    }

    private fun took(t0: Long): String {
        val j = Json()
        j.beginObject()
        j.field("took_ms", SystemClock.uptimeMillis() - t0)
        j.endObject()
        return j.toString()
    }

    /**
     * global: back, home, recents, notifications, or enter, which is the
     * keyboard's action key of the focused field (Android 11+). Never power
     * or lock: the prototype has no such action.
     *
     * With a display other than the main one, only back and enter. Enter
     * goes to the field with the input focus on that display. Back is a key
     * Android sends to the display that has the phone's focus, the one
     * touched last, so it is refused (`not_focused`) unless that is the
     * display asked for: a back meant for an agent's display must never
     * reach the person's screen. Runs on the worker then.
     */
    fun global(req: JSONObject): String {
        val name = req.getString("action")
        val display = req.optInt("display", Display.DEFAULT_DISPLAY)
        val t0 = SystemClock.uptimeMillis()
        val ok = if (display != Display.DEFAULT_DISPLAY) {
            when (name) {
                "back" -> {
                    val focused = focusedDisplay()
                    if (focused != display) {
                        throw RequestError(
                            "not_focused",
                            "back goes to the display touched last, which is " +
                                (if (focused == null) "not known" else "display $focused") + ", not display $display",
                        )
                    }
                    service.performGlobalAction(AccessibilityService.GLOBAL_ACTION_BACK)
                }
                "enter" -> imeEnter(display)
                else -> throw RequestError("unsupported", "$name works on the main display only")
            }
        } else {
            when (name) {
                "back" -> service.performGlobalAction(AccessibilityService.GLOBAL_ACTION_BACK)
                "home" -> service.performGlobalAction(AccessibilityService.GLOBAL_ACTION_HOME)
                "recents" -> service.performGlobalAction(AccessibilityService.GLOBAL_ACTION_RECENTS)
                "notifications" -> service.performGlobalAction(AccessibilityService.GLOBAL_ACTION_NOTIFICATIONS)
                "enter" -> imeEnter()
                else -> throw RequestError("bad_request", "unknown global action: $name")
            }
        }
        if (!ok) throw RequestError("action_failed", "Android refused $name")
        return took(t0)
    }

    /**
     * The display with the phone's focus: the display of the window
     * accessibility calls active, which follows the focused window of the
     * display touched last. Null when no window is active. Worker thread.
     */
    private fun focusedDisplay(): Int? {
        val root = service.rootInActiveWindow ?: return null
        val id = root.windowId
        TreeDumper.recycle(root)
        val all = service.windowsOnAllDisplays
        var found: Int? = null
        for (i in 0 until all.size()) {
            if (all.valueAt(i).any { it.id == id }) found = all.keyAt(i)
            TreeDumper.recycleWindows(all.valueAt(i))
        }
        return found
    }

    /** The keyboard's action key in the focused field of another display. Worker thread. */
    private fun imeEnter(display: Int): Boolean {
        val windows = service.displays.windowsOn(display)
        val node = try {
            service.textSetter.focusedOn(windows)
        } finally {
            TreeDumper.recycleWindows(windows)
        }
        try {
            if (node.packageName?.toString() == service.packageName) {
                throw RequestError("own_ui", OwnUI.REFUSAL)
            }
            return node.performAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_IME_ENTER.id)
        } finally {
            TreeDumper.recycle(node)
        }
    }

    private fun imeEnter(): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) {
            throw RequestError("unsupported", "the keyboard's action key needs Android 11")
        }
        val node = service.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
            ?: throw RequestError("not_found", "no field has the keyboard")
        if (node.packageName?.toString() == service.packageName) {
            throw RequestError("own_ui", OwnUI.REFUSAL)
        }
        return node.performAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_IME_ENTER.id)
    }

    /**
     * screenshot: the display as JPEG (default, quality 80) or PNG, in
     * base64. Android 11 and later; Android allows one every 333 ms, so a
     * request that comes sooner waits.
     */
    fun screenshot(req: JSONObject): String {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) {
            throw RequestError("unsupported", "screenshots without adb need Android 11")
        }
        val display = req.optInt("display", Display.DEFAULT_DISPLAY)
        val png = req.optString("format", "jpeg") == "png"
        val quality = req.optInt("quality", 80).coerceIn(1, 100)
        val t0 = SystemClock.uptimeMillis()
        for (attempt in 1..4) {
            val done = CountDownLatch(1)
            var result: AccessibilityService.ScreenshotResult? = null
            var code = 0
            service.takeScreenshot(display, shots, object : AccessibilityService.TakeScreenshotCallback {
                override fun onSuccess(r: AccessibilityService.ScreenshotResult) {
                    result = r
                    done.countDown()
                }

                override fun onFailure(errorCode: Int) {
                    code = errorCode
                    done.countDown()
                }
            })
            if (!done.await(SHOT_TIMEOUT_MS, TimeUnit.MILLISECONDS)) {
                throw RequestError("timeout", "the screenshot did not come")
            }
            val r = result
            if (r != null) {
                val captured = SystemClock.uptimeMillis()
                val hw = Bitmap.wrapHardwareBuffer(r.hardwareBuffer, r.colorSpace)
                    ?: throw RequestError("action_failed", "the screenshot could not be read")
                val bmp = hw.copy(Bitmap.Config.ARGB_8888, false)
                hw.recycle()
                r.hardwareBuffer.close()
                val out = ByteArrayOutputStream(512 shl 10)
                bmp.compress(if (png) Bitmap.CompressFormat.PNG else Bitmap.CompressFormat.JPEG, quality, out)
                val w = bmp.width
                val h = bmp.height
                bmp.recycle()
                val encoded = SystemClock.uptimeMillis()
                val j = Json(out.size() * 4 / 3 + 256)
                j.beginObject()
                j.field("width", w)
                j.field("height", h)
                j.field("format", if (png) "png" else "jpeg")
                j.field("capture_ms", captured - t0)
                j.field("encode_ms", encoded - captured)
                j.field("bytes", out.size())
                j.field("data", Base64.encodeToString(out.toByteArray(), Base64.NO_WRAP))
                j.endObject()
                return j.toString()
            }
            when (code) {
                AccessibilityService.ERROR_TAKE_SCREENSHOT_INTERVAL_TIME_SHORT -> Thread.sleep(SHOT_INTERVAL_MS)
                ERROR_SECURE_WINDOW -> throw RequestError("secure", "an app on the screen forbids screenshots")
                else -> throw RequestError("action_failed", "Android could not take the screenshot (error $code)")
            }
        }
        throw RequestError("busy", "Android kept refusing screenshots as too frequent")
    }

    /** launch: opens an app by package name, as its launcher icon does. */
    fun launch(req: JSONObject): String {
        val pkg = req.getString("package")
        if (pkg == service.packageName) throw RequestError("own_ui", OwnUI.REFUSAL)
        val intent = service.packageManager.getLaunchIntentForPackage(pkg)
            ?: throw RequestError("not_found", "$pkg is not installed or has no launcher icon")
        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_RESET_TASK_IF_NEEDED)
        val t0 = SystemClock.uptimeMillis()
        service.startActivity(intent)
        val j = Json()
        j.beginObject()
        j.field("package", pkg)
        j.field("took_ms", SystemClock.uptimeMillis() - t0)
        j.endObject()
        return j.toString()
    }

    /** apps: the launchable apps, with their names. */
    fun apps(): String {
        val pm = service.packageManager
        val intent = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
        val seen = HashSet<String>()
        val j = Json(16 shl 10)
        j.beginObject()
        j.name("apps").beginArray()
        for (ri in pm.queryIntentActivities(intent, 0)) {
            val ai = ri.activityInfo?.applicationInfo ?: continue
            if (!seen.add(ai.packageName)) continue
            j.beginObject()
            j.field("package", ai.packageName)
            j.field("label", ri.loadLabel(pm)?.toString().orEmpty())
            j.flag("system", ai.flags and ApplicationInfo.FLAG_SYSTEM != 0)
            j.endObject()
        }
        j.endArray()
        j.endObject()
        return j.toString()
    }

    companion object {
        const val TAP_MS = 50L
        private const val GESTURE_SLACK_MS = 5_000L
        private const val MAX_SEGMENTS = 200
        private const val MAX_SEGMENTS_MS = 60_000L
        private const val HOLD_MAX_MS = 30_000L
        private const val SHOT_TIMEOUT_MS = 5_000L
        private const val SHOT_INTERVAL_MS = 340L

        // AccessibilityService.ERROR_TAKE_SCREENSHOT_SECURE_WINDOW, Android 14.
        private const val ERROR_SECURE_WINDOW = 5
    }
}

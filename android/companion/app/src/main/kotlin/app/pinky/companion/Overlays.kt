package app.pinky.companion

import android.content.Intent
import android.graphics.Color
import android.graphics.PixelFormat
import android.graphics.Rect
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.text.TextUtils
import android.util.Log
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.view.accessibility.AccessibilityWindowInfo
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONObject
import java.io.Closeable

/**
 * Pinky's controls over other apps, drawn by the accessibility service so
 * they stay above every app: the Working pill with Stop while a task runs
 * or while Pinky on a computer drives the phone, and the card for an
 * approval or a handoff. They follow the core's state
 * (CoreClient.subscribe) and answer it on the control socket.
 *
 * The agent can never press them: their rects go to OwnUI, which refuses
 * gestures on them; the local driver hides these windows from the agent's
 * screen; and the views are accessibilityDataSensitive, so on Android 15
 * and later they also drop touches an accessibility service injects.
 */
class Overlays(
    private val service: PinkyAccessibilityService,
    // Gets each state too, on the subscription's thread (ScheduleNotices).
    private val onState: (JSONObject) -> Unit = {},
) {

    private val wm = service.getSystemService(WindowManager::class.java)
    private val main = Handler(Looper.getMainLooper())
    private val dp = service.resources.displayMetrics.density
    private var sub: Closeable? = null
    private var stopped = false

    private var pill: Window? = null
    private var card: Window? = null
    private var cardNeed: String? = null
    private var ended: String? = null // the task that ended and whose pill was closed
    private var endTimer: Runnable? = null

    // Pinky's own screen is in front. It says what the pill says, with its
    // own Stop, and the pill would cover its title: the pill hides there.
    private var ownScreen = false
    private val checkOwnScreen = Runnable { updateOwnScreen() }

    // A task on the background screen shows in its floating window.
    private val agentWindow = AgentWindow(service, this) { reportRects() }

    // While the background screen is on, the port Wireless debugging
    // listens on, which the core needs and which changes each time it is
    // turned on (AdbServices). -1 while discovery has said nothing yet.
    private var adb: AdbServices? = null
    private var foundPort = -1
    private var corePort = 0
    private val noPort = Runnable { if (foundPort < 0) tellPort(0) }

    /** What the pill is about, and so which button it has. */
    private enum class Pill {
        TASK, // the task runs: Stop
        ENDED, // the task ended: Close
        COMPUTER, // Pinky on a computer drives the phone: Stop
        COMPUTER_UNSTOPPABLE, // an older desktop Pinky does: no button
    }

    private class Window(val root: View, val text: TextView, val buttons: LinearLayout) {
        var extra: TextView? = null
        var deadline: TextView? = null
        var mode: Pill? = null // what the pill's buttons are for
    }

    fun start() {
        sub = CoreClient.subscribe(service, { st ->
            onState(st)
            main.post { render(st) }
        }, { main.post { render(JSONObject()) } })
    }

    /** Windows came, went or changed: the service calls it for their events. */
    fun windowsChanged() {
        if (pill == null) return
        main.removeCallbacks(checkOwnScreen)
        main.postDelayed(checkOwnScreen, 100)
    }

    private fun updateOwnScreen() {
        val own = ownScreenInFront()
        if (own == ownScreen) return
        ownScreen = own
        pill?.root?.visibility = if (own) View.GONE else View.VISIBLE
        reportRects()
    }

    // Whether the top application window on the phone's screen is one of
    // Pinky's own (OwnUI knows them the same way).
    private fun ownScreenInFront(): Boolean {
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

    fun stop() {
        stopped = true
        main.removeCallbacks(checkOwnScreen)
        sub?.close()
        sub = null
        main.post {
            stopLooking()
            agentWindow.close()
            remove(pill)
            remove(card)
            pill = null
            card = null
            service.ownUI.setOverlays(emptyList())
        }
    }

    private fun render(st: JSONObject) {
        if (stopped) return
        val agent = st.optJSONObject("agent")
        val computer = st.optJSONObject("computer")
        val needs = st.optJSONArray("needs")
        val need = if (needs != null && needs.length() > 0) needs.getJSONObject(0) else null
        when {
            agent?.optBoolean("running") == true -> {
                ended = null
                val step = lastStep(st)
                val line = when {
                    need != null -> service.getString(R.string.phone_pill_waiting)
                    step != null -> service.getString(R.string.phone_pill_working, step)
                    else -> service.getString(R.string.phone_pill_starting)
                }
                showPill(line, Pill.TASK)
            }
            // Pinky on a computer drives the phone (its claim): say so, and
            // offer Stop when it can be stopped from here. A task that ended
            // before it is old news once the computer is done.
            computer != null -> {
                if (agent != null) ended = endKey(agent)
                showPill(
                    service.getString(R.string.phone_pill_computer),
                    if (computer.optBoolean("stoppable")) Pill.COMPUTER else Pill.COMPUTER_UNSTOPPABLE,
                )
            }
            agent == null -> {
                ended = null
                hidePill()
            }
            else -> {
                val key = endKey(agent)
                if (ended != key) {
                    showEnded(agent, key)
                } else if (pill != null && pill?.mode != Pill.ENDED) {
                    hidePill() // the computer's pill, once it is done
                }
            }
        }
        if (need != null) showCard(need) else hideCard()
        val task = st.optJSONObject("task")
        agentWindow.update(
            st.optBoolean("background") && agent?.optBoolean("running") == true,
            task?.optString("id")?.ifEmpty { null } ?: agent?.optString("task"),
            task?.optString("state") == "paused",
        )
        followShell(st.optJSONObject("shell"))
    }

    // Wireless debugging's port, for the background screen.

    private fun followShell(shell: JSONObject?) {
        if (shell?.optBoolean("on") != true) {
            stopLooking()
            return
        }
        corePort = shell.optInt("port")
        if (adb?.running == true) return
        adb?.stop()
        foundPort = -1
        adb = AdbServices(service, AdbServices.CONNECT) { p ->
            foundPort = p
            tellPort(p)
        }.also { it.start() }
        // Not found in a while: Wireless debugging is off, whatever port
        // the core had from before.
        main.removeCallbacks(noPort)
        main.postDelayed(noPort, NO_PORT_MS)
    }

    private fun stopLooking() {
        main.removeCallbacks(noPort)
        adb?.stop()
        adb = null
        foundPort = -1
    }

    private fun tellPort(p: Int) {
        if (p == corePort || adb == null) return
        corePort = p
        CoreClient.send(CoreClient.cmd("shell", "op" to "port", "port" to p)) { err ->
            if (err != null) Log.w(TAG, "could not give the core the port", err)
        }
    }

    private fun endKey(agent: JSONObject) = agent.optString("task") + agent.optLong("seconds")

    // lastStep is the task's last step as the person reads it in Pinky
    // (Words.step): an answered approval's step no longer says "Waiting
    // for you".
    private fun lastStep(st: JSONObject): String? {
        val task = st.optJSONObject("task") ?: return null
        val steps = task.optJSONArray("steps") ?: return null
        val s = steps.optJSONObject(steps.length() - 1) ?: return null
        return Words.step(service.resources, s, true, task.optString("state")).text.ifEmpty { null }
    }

    // The pill: the task runs, or it just ended.

    private fun showPill(text: String, mode: Pill) {
        endTimer?.let { main.removeCallbacks(it) }
        endTimer = null
        val w = pill ?: newPill().also {
            pill = it
            ownScreen = ownScreenInFront()
            if (ownScreen) it.root.visibility = View.GONE
            add(it, Gravity.TOP or Gravity.CENTER_HORIZONTAL, ViewGroup.LayoutParams.WRAP_CONTENT, topInset() + px(8))
        }
        w.text.text = text
        if (w.mode == mode) return
        w.mode = mode
        // A locked phone stops the task, and a person waiting on a card
        // is not touching the screen: keep it on while the task runs.
        w.root.keepScreenOn = mode == Pill.TASK
        w.buttons.removeAllViews()
        when (mode) {
            Pill.TASK -> w.buttons.addView(stopButton(CoreClient.cmd("stop")))
            Pill.COMPUTER -> w.buttons.addView(stopButton(CoreClient.cmd("stop", "computer" to true)))
            Pill.COMPUTER_UNSTOPPABLE -> {}
            Pill.ENDED -> w.buttons.addView(button(service.getString(R.string.phone_close)) { hidePill() })
        }
    }

    private fun stopButton(cmd: JSONObject): TextView = button(service.getString(R.string.phone_stop), danger = true) { b ->
        b.isEnabled = false
        CoreClient.send(cmd) { err -> main.post { if (err != null) b.isEnabled = true } }
    }

    private fun showEnded(agent: JSONObject, key: String) {
        showPill(Words.endLine(service.resources, agent), Pill.ENDED)
        ended = key
        val t = Runnable { hidePill() }
        endTimer = t
        main.postDelayed(t, ENDED_SHOWN_MS)
    }

    private fun hidePill() {
        endTimer?.let { main.removeCallbacks(it) }
        endTimer = null
        remove(pill)
        pill = null
        reportRects()
    }

    private fun newPill(): Window {
        val root = LinearLayout(service).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            background = rounded(SURFACE, 24f)
            setPadding(px(14), px(6), px(6), px(6))
        }
        val dot = TextView(service).apply {
            text = "●"
            setTextColor(ACCENT)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 12f)
        }
        val text = TextView(service).apply {
            setTextColor(Color.WHITE)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            maxLines = 1
            ellipsize = TextUtils.TruncateAt.END
            maxWidth = (service.resources.displayMetrics.widthPixels * 0.55).toInt()
            setPadding(px(8), 0, px(10), 0)
            // The person opens Pinky's screen from the pill.
            setOnClickListener { openPinky() }
        }
        val buttons = LinearLayout(service)
        root.addView(dot)
        // The line gives way, not the button. A window as wide as its content
        // is measured first at a dialog's width (ViewRootImpl), and a weighted
        // child that does not fit there asks for a wider try.
        root.addView(text, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        root.addView(buttons)
        return Window(root, text, buttons)
    }

    // The card: an approval, or a handoff.

    private fun showCard(need: JSONObject, error: String? = null) {
        val id = need.optString("id")
        val w = card ?: newCard().also {
            card = it
            val width = service.resources.displayMetrics.widthPixels - px(24)
            add(it, Gravity.BOTTOM or Gravity.CENTER_HORIZONTAL, width, bottomInset() + px(12))
        }
        w.text.text = Words.shown(need, "title")
        val detail = listOf(Words.shown(need, "detail"), need.optString("app"), Words.shown(need, "rule").let {
            if (it.isEmpty()) "" else service.getString(R.string.phone_rule, it)
        }).filter { it.isNotEmpty() }.joinToString(" · ")
        w.extra?.text = detail
        w.extra?.visibility = if (detail.isEmpty()) View.GONE else View.VISIBLE
        val deadline = need.optLong("deadline_ms")
        if (deadline > 0) {
            val left = ((deadline - System.currentTimeMillis()) / 1000).coerceAtLeast(0)
            w.deadline?.text = service.getString(R.string.phone_deadline, left)
            w.deadline?.visibility = View.VISIBLE
        } else {
            w.deadline?.visibility = View.GONE
        }
        if (error != null) {
            w.deadline?.text = error
            w.deadline?.visibility = View.VISIBLE
        }
        if (cardNeed == id) return
        cardNeed = id
        w.buttons.removeAllViews()
        val decide = { decision: String ->
            { _: TextView ->
                setEnabled(w.buttons, false)
                // Take the card away before the core hears the answer: an
                // allowed step reads the screen again at once, and the card
                // still over its target would make the step stale.
                val shown = ownOverlays()
                hideCard()
                whenGone(shown) {
                    CoreClient.send(CoreClient.cmd("decide", "need" to id, "decision" to decision)) { err ->
                        if (err != null) {
                            Log.w(TAG, "could not answer", err)
                            val why = service.getString(R.string.phone_err_answer)
                            main.post { if (!stopped) showCard(need, why) }
                        }
                    }
                }
                Unit
            }
        }
        if (need.optString("kind") == "approval") {
            w.buttons.addView(button(service.getString(R.string.phone_deny), danger = true, onClick = decide("deny")))
            if (need.optBoolean("allow_task")) {
                w.buttons.addView(button(service.getString(R.string.phone_allow_task), onClick = decide("allow_task")))
            }
            w.buttons.addView(button(service.getString(R.string.phone_allow_once), primary = true, onClick = decide("allow_once")))
        } else {
            // A handoff: the person does the step, then hands back.
            w.buttons.addView(button(service.getString(R.string.phone_decline), danger = true, onClick = decide("deny")))
            w.buttons.addView(button(service.getString(R.string.phone_hand_back), primary = true, onClick = decide("allow_once")))
        }
    }

    // ownOverlays counts Pinky's overlays in the windows the agent's screen
    // is read from.
    private fun ownOverlays(): Int = try {
        service.windows.count {
            it.type == AccessibilityWindowInfo.TYPE_ACCESSIBILITY_OVERLAY && it.title?.toString() == service.packageName
        }
    } catch (e: Exception) {
        0
    }

    // whenGone runs then once fewer than shown of Pinky's overlays are
    // listed, or after half a second.
    private fun whenGone(shown: Int, then: () -> Unit) {
        val until = SystemClock.uptimeMillis() + 500
        val check = object : Runnable {
            override fun run() {
                if (shown == 0 || ownOverlays() < shown || SystemClock.uptimeMillis() >= until) then()
                else main.postDelayed(this, 16)
            }
        }
        check.run()
    }

    private fun hideCard() {
        remove(card)
        card = null
        cardNeed = null
        reportRects()
    }

    private fun newCard(): Window {
        val root = LinearLayout(service).apply {
            orientation = LinearLayout.VERTICAL
            background = rounded(SURFACE, 20f)
            setPadding(px(18), px(16), px(18), px(12))
        }
        val label = TextView(service).apply {
            text = service.getString(R.string.phone_asks)
            setTextColor(MUTED)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 12f)
        }
        val title = TextView(service).apply {
            setTextColor(Color.WHITE)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 17f)
            setTypeface(typeface, Typeface.BOLD)
            setPadding(0, px(4), 0, px(4))
        }
        val extra = TextView(service).apply {
            setTextColor(MUTED)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 13f)
        }
        val deadline = TextView(service).apply {
            setTextColor(MUTED)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 12f)
            setPadding(0, px(6), 0, 0)
        }
        val buttons = LinearLayout(service).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.END
            setPadding(0, px(10), 0, 0)
        }
        root.addView(label)
        root.addView(title)
        root.addView(extra)
        root.addView(deadline)
        root.addView(buttons)
        return Window(root, title, buttons).also {
            it.extra = extra
            it.deadline = deadline
        }
    }

    // Windows.

    private fun add(w: Window, gravity: Int, width: Int, y: Int) {
        sensitive(w.root)
        val lp = WindowManager.LayoutParams(
            width,
            ViewGroup.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.TYPE_ACCESSIBILITY_OVERLAY,
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN,
            PixelFormat.TRANSLUCENT,
        )
        lp.gravity = gravity
        lp.y = y
        // The driver knows Pinky's own windows by this title: their content
        // is accessibilityDataSensitive, so not even this service reads
        // their package (internal/driver/android/local).
        lp.title = service.packageName
        w.root.addOnLayoutChangeListener { _, _, _, _, _, _, _, _, _ -> reportRects() }
        try {
            wm.addView(w.root, lp)
        } catch (e: Exception) {
            Log.e(TAG, "could not show an overlay", e)
        }
    }

    private fun remove(w: Window?) {
        if (w == null) return
        try {
            wm.removeViewImmediate(w.root)
        } catch (_: Exception) {
        }
    }

    /** Tells OwnUI where Pinky's controls are, so gestures on them are refused. */
    private fun reportRects() {
        val rects = ArrayList<Rect>()
        for (v in listOfNotNull(pill?.root, card?.root, agentWindow.view)) {
            if (!v.isAttachedToWindow || v.width == 0 || v.visibility != View.VISIBLE) continue
            val loc = IntArray(2)
            v.getLocationOnScreen(loc)
            rects += Rect(loc[0], loc[1], loc[0] + v.width, loc[1] + v.height)
        }
        if (rects == service.ownUI.overlayRects()) return
        service.ownUI.setOverlays(rects)
        Log.i(TAG, "overlays at $rects")
    }

    private fun openPinky() {
        service.startActivity(
            Intent(service, TaskActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_REORDER_TO_FRONT),
        )
    }

    // Views.

    fun button(
        text: String,
        primary: Boolean = false,
        danger: Boolean = false,
        onClick: (TextView) -> Unit,
    ): TextView = TextView(service).apply {
        this.text = text
        setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
        setTypeface(typeface, Typeface.BOLD)
        gravity = Gravity.CENTER
        minWidth = px(64)
        setPadding(px(14), px(8), px(14), px(8))
        when {
            primary -> {
                setTextColor(Color.BLACK)
                background = rounded(ACCENT, 18f)
            }
            danger -> {
                setTextColor(DANGER)
                background = rounded(BUTTON, 18f)
            }
            else -> {
                setTextColor(Color.WHITE)
                background = rounded(BUTTON, 18f)
            }
        }
        layoutParams = LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.WRAP_CONTENT,
            ViewGroup.LayoutParams.WRAP_CONTENT,
        ).apply { marginStart = px(8) }
        setOnClickListener { onClick(this) }
        sensitive(this)
    }

    private fun setEnabled(group: LinearLayout, on: Boolean) {
        for (i in 0 until group.childCount) {
            group.getChildAt(i).isEnabled = on
            group.getChildAt(i).alpha = if (on) 1f else 0.5f
        }
    }

    /**
     * Hidden from accessibility services that are not accessibility tools
     * (this one included), and deaf to touches they inject on Android 15
     * and later. Touches that come through another app's window are
     * dropped too.
     */
    fun sensitive(v: View) {
        v.filterTouchesWhenObscured = true
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            v.setAccessibilityDataSensitive(View.ACCESSIBILITY_DATA_SENSITIVE_YES)
        }
        if (v is ViewGroup) for (i in 0 until v.childCount) sensitive(v.getChildAt(i))
    }

    fun rounded(color: Int, radiusDp: Float) = GradientDrawable().apply {
        setColor(color)
        cornerRadius = radiusDp * dp
    }

    fun px(d: Int) = (d * dp).toInt()

    fun topInset(): Int = dimen("status_bar_height")

    private fun bottomInset(): Int = dimen("navigation_bar_height")

    private fun dimen(name: String): Int {
        val id = service.resources.getIdentifier(name, "dimen", "android")
        return if (id != 0) service.resources.getDimensionPixelSize(id) else 0
    }

    companion object {
        private const val TAG = "PinkyOverlays"
        private const val ENDED_SHOWN_MS = 8_000L
        private const val NO_PORT_MS = 5_000L
        val SURFACE = Color.rgb(28, 28, 30)
        private val BUTTON = Color.argb(255, 58, 58, 62)
        val MUTED = Color.argb(255, 174, 174, 178)
        val ACCENT = Color.rgb(255, 196, 0)
        private val DANGER = Color.rgb(255, 105, 97)
    }
}

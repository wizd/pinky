package app.pinky.companion

import android.app.Activity
import android.graphics.Typeface
import android.graphics.drawable.Drawable
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONArray
import org.json.JSONObject

/**
 * A task's steps in a group, as the core's gateway writes them and desktop
 * Pinky words them (Words.step): the task on the task screen, and a task
 * of the history (PastTaskActivity). The caller adds group to its screen
 * and shows or hides it.
 */
class StepList(private val activity: Activity, private val look: Look) {

    val group: LinearLayout = look.group()

    private val rows = ArrayList<Row>()
    private val semibold = Typeface.create(Typeface.DEFAULT, 600, false)

    private class Row(val view: View, val mark: TextView, val text: TextView, val sub: TextView, val person: Drawable?)

    /** Shows steps (TaskState.Steps) of a task in taskState (gateway.TaskState), keeping the rows it can. */
    fun show(steps: JSONArray, taskState: String) {
        val n = steps.length()
        if (n < rows.size) {
            group.removeAllViews()
            rows.clear()
        }
        for (i in 0 until n) {
            val s = steps.optJSONObject(i) ?: continue
            val row = rows.getOrNull(i) ?: row().also {
                rows += it
                look.addRow(group, it.view)
            }
            val step = Words.step(activity.resources, s, i == n - 1, taskState)
            row.text.say(step.text)
            row.sub.say(step.sub)
            // What waits for the person is amber, and with the step the
            // agent is on, bold (ui/extras.go).
            row.text.setTextColor(if (step.kind == Words.Kind.WAIT) look.amber else look.ink)
            row.text.typeface = if (step.kind == Words.Kind.WAIT || step.kind == Words.Kind.NOW) semibold else Typeface.DEFAULT
            mark(row, step.kind, s.optString("status"))
        }
    }

    // A step's mark: what waits, the step the agent is on, the person's own
    // steps (a figure, as in desktop Pinky), and how the others ended.
    private fun mark(row: Row, kind: Words.Kind, status: String) {
        val mark = row.mark
        val figure = if (kind == Words.Kind.YOU) row.person else null
        if (mark.compoundDrawablesRelative[0] !== figure) mark.setCompoundDrawablesRelative(figure, null, null, null)
        val (glyph, color) = when {
            kind == Words.Kind.YOU -> "" to look.ink2
            kind == Words.Kind.WAIT -> "…" to look.amber
            kind == Words.Kind.NOW -> "•" to look.accent
            status == "uncertain" -> "✓" to look.amber
            status in NOT_DONE -> "–" to look.ink3
            status in RAN -> "✓" to look.green
            else -> "✕" to look.red
        }
        if (mark.text.toString() != glyph) mark.text = glyph
        mark.setTextColor(color)
    }

    private fun row(): Row {
        val mark = look.text("", Look.BODY, look.ink3, 600)
        val text = look.text("", Look.BODY, look.ink)
        val sub = look.text("", Look.SMALL, look.ink3).apply { setPadding(0, look.dp(2), 0, 0) }
        sub.say("")
        val person = activity.getDrawable(R.drawable.ic_person)?.mutate()?.apply {
            setTint(look.ink2)
            setBounds(0, 0, look.dp(16), look.dp(16))
        }
        val texts = LinearLayout(activity).apply {
            orientation = LinearLayout.VERTICAL
            addView(text)
            addView(sub)
        }
        val v = LinearLayout(activity).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(look.dp(14), look.dp(10), look.dp(16), look.dp(10))
            addView(mark, LinearLayout.LayoutParams(look.dp(22), ViewGroup.LayoutParams.WRAP_CONTENT))
            addView(texts, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        }
        return Row(v, mark, text, sub, person)
    }

    companion object {
        // Steps that ran.
        private val RAN = setOf("settled", "user", "", "running")

        // Steps that did not happen, without anything having gone wrong
        // (ui/extras.go, stepDisplay).
        private val NOT_DONE = setOf(
            "approval_denied", "stopped", "stale", "expired", "user_took_over", "paused",
            "device_locked", "device_offline", "device_needs_setup", "device_paused",
        )
    }
}

/**
 * How a task ended, on a card: its mark and title, why, and what it used.
 * The task screen's has Dismiss (onDismiss); a task of the history's
 * (PastTaskActivity) has none. The caller adds view to its screen and shows
 * or hides it.
 */
class EndCard(private val activity: Activity, private val look: Look, onDismiss: (() -> Unit)? = null) {

    val view: LinearLayout = look.card()

    private val mark = look.text("", Look.BODY, look.green, 600).apply { setPadding(0, 0, look.dp(8), 0) }
    private val title = look.text("", Look.BODY, look.ink, 600)
    private val reason = look.text("", Look.BODY, look.ink2).apply {
        setPadding(0, look.dp(6), 0, 0)
        setTextIsSelectable(true)
    }
    private val usage = look.text("", Look.SMALL, look.ink3).apply { setPadding(0, look.dp(4), 0, 0) }

    init {
        val top = LinearLayout(activity).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            addView(mark)
            addView(title, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        }
        if (onDismiss != null) top.addView(look.button(activity.getString(R.string.phone_dismiss), Look.Kind.PLAIN) { onDismiss() })
        view.addView(top, look.matchWrap())
        view.addView(reason, look.matchWrap())
        view.addView(usage, look.matchWrap())
    }

    /** Shows how the agent ended its task (AgentState), as the task screen says it. */
    fun show(agent: JSONObject) {
        val res = activity.resources
        val (t, why) = Words.end(res, agent)
        show(agent.optString("end"), t, why, Words.usage(res, agent))
    }

    /** Shows an end (a pilot.End, or a past task's outcome) with its title, why it ended and what the task used. */
    fun show(end: String, title: String, why: String, used: String) {
        mark.text = if (end == "done") "✓" else "●"
        mark.setTextColor(color(look, end))
        this.title.say(title)
        reason.say(why)
        usage.say(used)
    }

    companion object {
        /** The color of an end: done, stopped by the person, or neither (a limit, a failure). */
        fun color(look: Look, end: String): Int = when (end) {
            "done" -> look.green
            "stopped" -> look.ink3
            else -> look.amber
        }
    }
}

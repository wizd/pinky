package app.pinky.companion

import android.app.Activity
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.widget.TextView
import org.json.JSONArray
import org.json.JSONObject

/**
 * A task of the history: what the person asked, when, how it ended and its
 * steps, as desktop Pinky shows a task of its Activity (ui/activity.go).
 * It opens from Activity on the task screen and from HistoryActivity, in
 * their process, and is closed to agents as they are (Look).
 */
class PastTaskActivity : Activity() {

    private val main = Handler(Looper.getMainLooper())
    private lateinit var look: Look
    private lateinit var title: TextView
    private lateinit var meta: TextView
    private lateinit var end: EndCard
    private lateinit var note: TextView
    private lateinit var stepsHeading: TextView
    private lateinit var steps: StepList

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        look = Look(this)
        val col = look.column()
        title = look.text("", Look.TITLE2, look.ink, 600).apply {
            setPadding(look.dp(4), look.dp(12), look.dp(4), 0)
            setTextIsSelectable(true)
        }
        meta = look.text("", Look.SMALL, look.ink3).apply { setPadding(look.dp(4), look.dp(4), look.dp(4), 0) }
        end = EndCard(this, look)
        note = look.note()
        stepsHeading = look.section(getString(R.string.phone_task_steps))
        steps = StepList(this, look)
        col.addView(title, look.matchWrap())
        col.addView(meta, look.matchWrap())
        col.addView(end.view, look.below(16))
        col.addView(note, look.matchWrap())
        col.addView(stepsHeading, look.matchWrap())
        col.addView(steps.group, look.matchWrap())
        for (v in listOf(title, meta, note)) v.say("")
        for (v in listOf(end.view, stepsHeading, steps.group)) v.shown(false)
        look.setScreen(col)
        read()
    }

    override fun onDestroy() {
        main.removeCallbacksAndMessages(null)
        super.onDestroy()
    }

    private fun read() {
        val id = intent.getStringExtra(EXTRA_ID).orEmpty()
        CoreClient.background {
            val t = runCatching { PastTasks.task(this, id) }
            main.post {
                if (isDestroyed) return@post
                t.onSuccess { show(it) }.onFailure { failed(it) }
            }
        }
    }

    private fun show(t: JSONObject) {
        val agent = t.optJSONObject("agent")
        // The task as the person wrote it; the gateway's title is one line.
        title.say(agent?.optString("task").orEmpty().ifBlank { t.optString("title") })
        val started = t.optLong("started_ms")
        val ended = t.optLong("ended_ms")
        val at = Words.day(resources, started) + " " + Words.clock(this, started)
        // The end card says how long the agent took; without its record,
        // the gateway's times do.
        meta.say(if (agent == null && ended > started) at + " · " + Words.elapsed(resources, (ended - started) / 1000) else at)
        if (agent != null) {
            end.show(agent)
        } else {
            val outcome = t.optString("outcome")
            end.show(outcome, Words.outcome(resources, outcome), "", "")
        }
        end.view.shown(true)
        val s = t.optJSONArray("steps") ?: JSONArray()
        steps.show(s, t.optString("state"))
        stepsHeading.shown(s.length() > 0)
        steps.group.shown(s.length() > 0)
    }

    private fun failed(e: Throwable) {
        val gone = e is CoreClient.Refused && e.code == PastTasks.GONE
        if (!gone) Log.w(TAG, "could not read a task of Activity", e)
        note.say(getString(if (gone) R.string.phone_history_gone else R.string.phone_history_failed))
    }

    companion object {
        private const val TAG = "PinkyScreens"

        /** The task's ID in the history (PastTask.ID). */
        const val EXTRA_ID = "app.pinky.companion.extra.TASK"
    }
}

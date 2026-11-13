package app.pinky.companion

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.text.TextUtils
import org.json.JSONArray
import org.json.JSONObject

/**
 * The history: the tasks Pinky did on the phone that ended, which the core
 * keeps for 30 days, as desktop Pinky keeps its Activity
 * (internal/standalone/history.go, PastTask). The task screen lists the
 * latest under Activity, HistoryActivity all of them, and PastTaskActivity
 * one with its steps. page and task block; run them off the main thread.
 */
object PastTasks {

    /** control.go's CodeGone: the history no longer has the task. */
    const val GONE = "gone"

    /** Tasks of the history, newest first; more is whether older ones follow. */
    class Page(val tasks: List<JSONObject>, val more: Boolean)

    /** At most limit tasks that started before before (Unix ms; 0 for now), in context's language (CoreClient.speak). */
    fun page(context: Context, before: Long, limit: Int): Page {
        CoreClient.speak(context)
        val r = CoreClient.call(CoreClient.cmd("history", "before" to before, "limit" to limit))
        // An empty history leaves "history" out.
        val a = r.optJSONArray("history") ?: JSONArray()
        return Page(List(a.length()) { a.getJSONObject(it) }, r.optBoolean("more"))
    }

    /** One task, with its steps; throws Refused with GONE when the history no longer has it. */
    fun task(context: Context, id: String): JSONObject {
        CoreClient.speak(context)
        return CoreClient.call(CoreClient.cmd("history", "id" to id)).optJSONArray("history")?.optJSONObject(0)
            ?: throw CoreClient.Refused(GONE, "the core sent no task")
    }

    fun open(activity: Activity, id: String) {
        activity.startActivity(Intent(activity, PastTaskActivity::class.java).putExtra(PastTaskActivity.EXTRA_ID, id))
    }

    /**
     * A task's row: its title; when it started (with its day, withDay) and
     * what it used; and how it ended in a word, in the color of the end
     * card on the task screen.
     */
    fun row(activity: Activity, look: Look, t: JSONObject, withDay: Boolean, onClick: () -> Unit): Look.Row {
        val res = activity.resources
        val agent = t.optJSONObject("agent")
        val started = t.optLong("started_ms")
        val ended = t.optLong("ended_ms")
        val time = Words.clock(activity, started)
        val at = if (withDay) Words.day(res, started, short = true) + " " + time else time
        // Without the agent's record, the gateway's times say how long it took.
        val used = when {
            agent != null -> Words.usage(res, agent)
            ended > started -> Words.elapsed(res, (ended - started) / 1000)
            else -> null
        }
        val outcome = t.optString("outcome")
        val r = look.row(t.optString("title"), Words.outcome(res, outcome), listOfNotNull(at, used).joinToString(" · "), onClick)
        r.title.maxLines = 2
        r.title.ellipsize = TextUtils.TruncateAt.END
        r.value.setTextColor(EndCard.color(look, agent?.optString("end") ?: outcome))
        return r
    }
}

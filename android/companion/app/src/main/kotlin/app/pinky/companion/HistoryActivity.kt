package app.pinky.companion

import android.app.Activity
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONObject

/**
 * Every task of the history, newest first and by day, as desktop Pinky's
 * Activity lists them (ui/activity.go). It opens from Activity on the task
 * screen, in its process, and is closed to agents as it is (Look); a task
 * opens in PastTaskActivity.
 */
class HistoryActivity : Activity() {

    private val main = Handler(Looper.getMainLooper())
    private lateinit var look: Look
    private lateinit var days: LinearLayout
    private lateinit var note: TextView
    private lateinit var moreRow: LinearLayout
    private lateinit var moreButton: TextView

    private val tasks = ArrayList<JSONObject>() // the tasks listed, newest first
    private var more = false // whether older tasks follow
    private var reading = false
    private var failed = false // the last read failed
    private var reads = 0 // counts reads, so that only the latest is shown

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        look = Look(this)
        val col = look.column()
        col.addView(look.header(getString(R.string.phone_activity)), look.matchWrap())
        days = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        col.addView(days, look.matchWrap())
        note = look.note()
        note.say("")
        col.addView(note, look.matchWrap())
        moreButton = look.button(getString(R.string.phone_history_more), Look.Kind.PLAIN) { read(next = true) }
        moreRow = look.buttons(moreButton).apply { gravity = Gravity.CENTER }
        col.addView(moreRow, look.matchWrap())
        look.setScreen(col)
        renderStatus()
        read(next = false)
    }

    override fun onRestart() {
        super.onRestart()
        // Back from a task, or from another app: tasks may have ended since.
        read(next = false)
    }

    override fun onDestroy() {
        main.removeCallbacksAndMessages(null)
        super.onDestroy()
    }

    // Reads the history again, as many tasks as the screen lists, or with
    // next the page after the last one listed.
    private fun read(next: Boolean) {
        if (next && (reading || !more || tasks.isEmpty())) return
        val before = if (next) tasks.last().optLong("started_ms") else 0L
        val limit = if (next) PAGE else tasks.size.coerceIn(PAGE, MAX)
        val n = ++reads
        reading = true
        renderStatus()
        CoreClient.background {
            val page = runCatching { PastTasks.page(this, before, limit) }
            main.post {
                if (n != reads || isDestroyed) return@post
                reading = false
                page.onSuccess {
                    failed = false
                    if (!next) tasks.clear()
                    tasks += it.tasks
                    more = it.more
                    renderList()
                }.onFailure { e ->
                    Log.w(TAG, "could not read Activity", e)
                    failed = true
                }
                renderStatus()
            }
        }
    }

    // The tasks, each day under its heading.
    private fun renderList() {
        days.removeAllViews()
        var day = ""
        var group: LinearLayout? = null
        for (t in tasks) {
            val d = Words.day(resources, t.optLong("started_ms"))
            var g = group
            if (g == null || d != day) {
                g = look.group()
                days.addView(look.section(d), look.matchWrap())
                days.addView(g, look.matchWrap())
                day = d
                group = g
            }
            val id = t.optString("id")
            look.addRow(g, PastTasks.row(this, look, t, withDay = false) { PastTasks.open(this, id) }.view)
        }
    }

    private fun renderStatus() {
        note.say(
            when {
                failed -> getString(R.string.phone_history_failed)
                tasks.isEmpty() && !reading -> getString(R.string.phone_history_empty)
                else -> ""
            },
        )
        moreRow.shown(more && tasks.isNotEmpty())
        look.enable(moreButton, !reading)
    }

    companion object {
        private const val TAG = "PinkyScreens"
        private const val PAGE = 30
        private const val MAX = 100 // the most tasks the core sends at once
    }
}

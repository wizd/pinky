package app.pinky.companion

import android.app.Activity
import android.app.DatePickerDialog
import android.app.Dialog
import android.app.TimePickerDialog
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.format.DateFormat
import android.util.Log
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONArray
import org.json.JSONObject
import java.time.LocalDate

/**
 * A scheduled task, new or saved (EXTRA_ID), as desktop Pinky's schedule
 * dialog has it (ui/schedules.go): the task, when it runs, and when to
 * tell the person; the next three runs, which the core works out
 * (schedule_preview); and for a saved one Run now, Pause and Delete, and
 * its last runs. In the task screen's process, closed to agents as it is
 * (Look). Its times are in the phone's time zone.
 */
class ScheduleEditActivity : Activity() {

    private val main = Handler(Looper.getMainLooper())
    private lateinit var look: Look
    private lateinit var settings: TaskSettings
    private lateinit var secrets: Secrets
    private lateinit var form: LinearLayout
    private lateinit var error: TextView
    private lateinit var nextGroup: LinearLayout
    private lateinit var saveButton: TextView
    private lateinit var runButton: TextView
    private lateinit var pauseButton: TextView
    private lateinit var deleteButton: TextView
    private lateinit var runsHeading: TextView
    private lateinit var runsGroup: LinearLayout
    private var picker: Dialog? = null

    // The schedule as the screen has it.
    private var id = ""
    private var task = ""
    private var repeat = "daily"
    private var date = LocalDate.now().plusDays(1).toString()
    private var times = listOf("08:00")
    private var days = setOf(LocalDate.now().dayOfWeek.value % 7)
    private var every = 4
    private var condition = ""
    private var cooldown = 0
    private var paused = false
    private var zone = "" // a saved schedule's own zone, kept as it is
    private var runs = JSONArray()

    private var busy = false
    private var previews = 0
    private val preview = Runnable { readPreview() }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        look = Look(this)
        settings = TaskSettings(this)
        secrets = Secrets(this)
        id = intent.getStringExtra(EXTRA_ID).orEmpty()
        val col = look.column()
        col.addView(
            look.header(getString(if (id.isEmpty()) R.string.phone_sched_title_new else R.string.phone_sched_title_edit)),
            look.matchWrap(),
        )
        form = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        col.addView(form, look.matchWrap())
        col.addView(look.section(getString(R.string.phone_sched_next)), look.matchWrap())
        nextGroup = look.group()
        col.addView(nextGroup, look.matchWrap())
        error = look.text("", Look.SMALL, look.red).apply { setPadding(look.dp(4), look.dp(8), look.dp(4), 0) }
        error.say("")
        col.addView(error, look.matchWrap())
        saveButton = look.button(getString(R.string.phone_save), Look.Kind.PRIMARY) { save(then = { finish() }) }
        runButton = look.button(getString(R.string.phone_sched_run_now), Look.Kind.SECONDARY) { runNow() }
        col.addView(look.buttons(saveButton, runButton).apply { gravity = Gravity.CENTER }, look.below(12))
        pauseButton = look.button(getString(R.string.phone_sched_pause), Look.Kind.PLAIN) { togglePause() }
        deleteButton = look.button(getString(R.string.phone_sched_delete), Look.Kind.DANGER) { askDelete() }
        col.addView(look.buttons(pauseButton, deleteButton).apply { gravity = Gravity.CENTER }, look.below(4))
        runsHeading = look.section(getString(R.string.phone_sched_runs))
        col.addView(runsHeading, look.matchWrap())
        runsGroup = look.group()
        col.addView(runsGroup, look.matchWrap())
        look.setScreen(col)
        renderForm()
        renderButtons()
        renderRuns()
        if (id.isEmpty()) {
            schedulePreview()
            editTask()
        } else {
            load()
        }
    }

    override fun onDestroy() {
        main.removeCallbacksAndMessages(null)
        picker?.dismiss()
        look.dismissDialog()
        super.onDestroy()
    }

    // The saved schedule.
    private fun load() {
        busy = true
        renderButtons()
        CoreClient.background {
            val r = runCatching { CoreClient.call(Schedules.cmd("schedules")) }
            main.post {
                if (isDestroyed) return@post
                busy = false
                r.onSuccess { reply ->
                    val all = reply.optJSONArray("schedules") ?: JSONArray()
                    val s = (0 until all.length()).map { all.optJSONObject(it) }.firstOrNull { it?.optString("id") == id }
                    if (s == null) {
                        error.say(getString(R.string.phone_sched_err_not_found))
                        id = ""
                    } else {
                        take(s)
                    }
                }.onFailure { e ->
                    Log.w(TAG, "could not read the schedule", e)
                    error.say(getString(R.string.phone_sched_core_down))
                }
                renderForm()
                renderButtons()
                renderRuns()
                schedulePreview()
            }
        }
    }

    private fun take(s: JSONObject) {
        id = s.optString("id")
        task = s.optString("task")
        repeat = s.optString("repeat").ifEmpty { "daily" }
        date = s.optString("date").ifEmpty { date }
        times = strings(s.optJSONArray("times")).ifEmpty { times }
        val d = s.optJSONArray("days")
        if (d != null && d.length() > 0) days = (0 until d.length()).map { d.optInt(it) }.toSet()
        every = s.optInt("every").takeIf { it > 0 } ?: every
        condition = s.optString("condition")
        cooldown = s.optInt("cooldown")
        paused = s.optBoolean("paused")
        zone = s.optString("zone")
        runs = s.optJSONArray("runs") ?: JSONArray()
    }

    // The schedule as the core takes it (schedule.Schedule).
    private fun draft(): JSONObject {
        val j = JSONObject()
            .put("task", task.trim())
            .put("repeat", repeat)
            .put("paused", paused)
        if (id.isNotEmpty()) j.put("id", id)
        if (zone.isNotEmpty()) j.put("zone", zone)
        when (repeat) {
            "once" -> j.put("date", date).put("times", JSONArray(times.take(1)))
            "interval" -> j.put("every", every).put("times", JSONArray(times.take(1)))
            "weekly" -> j.put("days", JSONArray(days.sorted())).put("times", JSONArray(times))
            else -> j.put("times", JSONArray(times))
        }
        if (condition.isNotBlank()) j.put("condition", condition.trim()).put("cooldown", cooldown)
        return j
    }

    // The form.

    private fun renderForm() {
        form.removeAllViews()
        val g = look.group()
        look.addRow(g, look.row(getString(R.string.phone_sched_task), desc = task.ifEmpty { getString(R.string.phone_sched_task_hint) }) { editTask() }.view)
        look.addRow(g, look.row(getString(R.string.phone_sched_repeat), repeatLabel(repeat)) { chooseRepeat() }.view)
        when (repeat) {
            "once" -> {
                look.addRow(g, look.row(getString(R.string.phone_sched_date), ScheduleWords.date(resources, date)) { pickDate() }.view)
                look.addRow(g, timeRow(R.string.phone_sched_time, 0))
            }
            "interval" -> {
                look.addRow(g, look.row(getString(R.string.phone_sched_every), resources.getQuantityString(R.plurals.phone_sched_hours, every, every)) { chooseEvery() }.view)
                look.addRow(g, timeRow(R.string.phone_sched_start_at, 0))
            }
            else -> {
                if (repeat == "weekly") {
                    val names = days.sorted().joinToString(getString(R.string.phone_sched_list_sep)) { ScheduleWords.weekday(resources, it, short = true) }
                    look.addRow(g, look.row(getString(R.string.phone_sched_days), names) { chooseDays() }.view)
                }
                for (i in times.indices) look.addRow(g, timeRow(R.string.phone_sched_time, i))
                if (times.size < ScheduleRules.MAX_TIMES) {
                    look.addRow(g, look.row(getString(R.string.phone_sched_add_time)) { pickTime(-1) }.view)
                }
            }
        }
        form.addView(g, look.below(12))

        form.addView(look.section(getString(R.string.phone_sched_condition)), look.matchWrap())
        val tell = look.group()
        look.addRow(
            tell,
            look.row(
                getString(R.string.phone_sched_condition),
                desc = condition.ifEmpty { getString(R.string.phone_sched_condition_desc) },
            ) { editCondition() }.view,
        )
        if (condition.isNotBlank()) {
            look.addRow(tell, look.row(getString(R.string.phone_sched_cooldown), cooldownLabel(cooldown)) { chooseCooldown() }.view)
        }
        form.addView(tell, look.matchWrap())
    }

    private fun timeRow(title: Int, i: Int) = look.row(getString(title), ScheduleWords.time(this, times.getOrElse(i) { "08:00" })) {
        if (repeat == "once" || repeat == "interval" || times.size <= 1) {
            pickTime(i)
        } else {
            look.menu(ScheduleWords.time(this, times[i]), listOf(getString(R.string.phone_sched_change), getString(R.string.phone_sched_remove))) { which ->
                if (which == 0) pickTime(i) else changed { times = times.filterIndexed { j, _ -> j != i } }
            }
        }
    }.view

    private fun changed(change: () -> Unit) {
        change()
        error.say("")
        renderForm()
        schedulePreview()
    }

    private fun editTask() {
        look.edit(getString(R.string.phone_sched_task), "", task, getString(R.string.phone_sched_task_hint), Look.Field.TEXT) { v ->
            changed { task = v.trim() }
        }
    }

    private fun editCondition() {
        look.edit(
            getString(R.string.phone_sched_condition),
            getString(R.string.phone_sched_condition_desc),
            condition,
            getString(R.string.phone_sched_condition_hint),
            Look.Field.TEXT,
        ) { v -> changed { condition = v.trim() } }
    }

    private fun chooseRepeat() {
        val all = ScheduleRules.REPEATS
        look.choose(getString(R.string.phone_sched_repeat), all.map { repeatLabel(it) }, all.indexOf(repeat)) { i ->
            changed { repeat = all[i] }
        }
    }

    private fun chooseEvery() {
        val all = ScheduleRules.INTERVALS
        look.choose(getString(R.string.phone_sched_every), all.map { resources.getQuantityString(R.plurals.phone_sched_hours, it, it) }, all.indexOf(every)) { i ->
            changed { every = all[i] }
        }
    }

    private fun chooseCooldown() {
        val all = ScheduleRules.COOLDOWNS
        look.choose(getString(R.string.phone_sched_cooldown), all.map { cooldownLabel(it) }, all.indexOf(cooldown)) { i ->
            changed { cooldown = all[i] }
        }
    }

    private fun chooseDays() {
        // Monday first, as most of the world's calendars; 0 is Sunday.
        val order = listOf(1, 2, 3, 4, 5, 6, 0)
        look.chooseMany(
            getString(R.string.phone_sched_days),
            order.map { ScheduleWords.weekday(resources, it, short = false) },
            BooleanArray(order.size) { order[it] in days },
        ) { picked -> changed { days = order.filterIndexed { i, _ -> picked[i] }.toSet() } }
    }

    private fun pickDate() {
        val d = runCatching { LocalDate.parse(date) }.getOrElse { LocalDate.now() }
        picker?.dismiss()
        picker = DatePickerDialog(this, { _, y, m, day -> changed { date = LocalDate.of(y, m + 1, day).toString() } }, d.year, d.monthValue - 1, d.dayOfMonth)
            .also { it.show() }
    }

    // Picks the time at i, or a new one with -1.
    private fun pickTime(i: Int) {
        val (h, m) = ScheduleRules.parse(times.getOrElse(i) { "08:00" }) ?: (8 to 0)
        picker?.dismiss()
        picker = TimePickerDialog(this, { _, hour, minute ->
            val t = ScheduleRules.hhmm(hour, minute)
            changed {
                times = when {
                    i < 0 -> ScheduleRules.addTime(times, t)
                    i < times.size -> ScheduleRules.changeTime(times, i, t)
                    else -> listOf(t)
                }
            }
        }, h, m, DateFormat.is24HourFormat(this)).also { it.show() }
    }

    private fun repeatLabel(r: String) = getString(
        when (r) {
            "once" -> R.string.phone_sched_once
            "weekdays" -> R.string.phone_sched_weekdays
            "weekly" -> R.string.phone_sched_weekly
            "interval" -> R.string.phone_sched_interval
            else -> R.string.phone_sched_daily
        },
    )

    private fun cooldownLabel(m: Int) = getString(
        when (m) {
            60 -> R.string.phone_sched_cooldown_hour
            24 * 60 -> R.string.phone_sched_cooldown_day
            else -> R.string.phone_sched_cooldown_always
        },
    )

    // The next runs, a moment after the last change.

    private fun schedulePreview() {
        main.removeCallbacks(preview)
        main.postDelayed(preview, 250)
    }

    private fun readPreview() {
        val n = ++previews
        val d = draft()
        if (d.optString("task").isEmpty()) d.put("task", "-") // the next runs do not depend on it
        CoreClient.background {
            val r = runCatching { CoreClient.call(Schedules.cmd("schedule_preview", "schedule" to d)) }
            main.post {
                if (n != previews || isDestroyed) return@post
                nextGroup.removeAllViews()
                val lines = r.fold(
                    { reply ->
                        val up = reply.optJSONArray("upcoming") ?: JSONArray()
                        when {
                            paused -> listOf(getString(R.string.phone_sched_paused))
                            up.length() == 0 -> listOf(getString(R.string.phone_sched_no_next))
                            else -> (0 until up.length()).map { ScheduleWords.moment(this, up.optLong(it)) }
                        }
                    },
                    { e -> listOf(why(e)) },
                )
                for (l in lines) look.addRow(nextGroup, look.row(l).view)
            }
        }
    }

    // What the buttons do.

    private fun renderButtons() {
        val saved = id.isNotEmpty()
        look.enable(saveButton, !busy)
        runButton.shown(saved)
        pauseButton.shown(saved)
        deleteButton.shown(saved)
        look.enable(runButton, !busy)
        look.enable(pauseButton, !busy)
        look.enable(deleteButton, !busy)
        pauseButton.text = getString(if (paused) R.string.phone_sched_resume else R.string.phone_sched_pause)
    }

    private fun renderRuns() {
        runsGroup.removeAllViews()
        val saved = id.isNotEmpty()
        runsHeading.shown(saved)
        runsGroup.shown(saved)
        if (!saved) return
        if (runs.length() == 0) {
            look.addRow(runsGroup, look.row(getString(R.string.phone_sched_no_runs)).view)
            return
        }
        for (i in 0 until runs.length()) {
            val r = runs.optJSONObject(i) ?: continue
            var title = ScheduleWords.moment(this, ScheduleWords.ms(r.optString("due")))
            if (r.optBoolean("manual")) title = getString(R.string.phone_sched_run_manual, title)
            val line = ScheduleWords.run(
                resources,
                r.optString("end"),
                r.optString("reason"),
                r.optString("summary"),
                if (r.has("met")) r.optBoolean("met") else null,
                r.optInt("missed"),
            )
            val row = look.row(title, desc = line)
            row.desc.setTextIsSelectable(true)
            look.addRow(runsGroup, row.view)
        }
    }

    // Saves the schedule, then runs then; says why it cannot.
    private fun save(then: () -> Unit) {
        if (busy) return
        busy = true
        error.say("")
        renderButtons()
        val d = draft()
        CoreClient.background {
            val r = runCatching { CoreClient.call(Schedules.cmd("schedule_save", "schedule" to d)) }
            r.onSuccess { Schedules.arm(this, it.optLong("next_ms")) }
            main.post {
                if (isDestroyed) return@post
                busy = false
                r.onSuccess { reply ->
                    reply.optJSONArray("schedules")?.optJSONObject(0)?.let { take(it) }
                    renderButtons()
                    then()
                }.onFailure { e ->
                    error.say(why(e))
                    renderButtons()
                }
            }
        }
    }

    // Run now: saves what the screen has, and runs it with the person
    // there. The agent starts from the app the person was in.
    private fun runNow() {
        save(then = {
            busy = true
            renderButtons()
            val service = settings.service
            val name = settings.keyName(service)
            val optional = settings.keyOptional(service)
            CoreClient.background {
                val why = try {
                    val key = secrets.load(name)
                    when {
                        key != null || optional && !secrets.has(name) -> {
                            CoreClient.call(settings.modelRequest("schedule_run", key.orEmpty(), service).put("id", id).put("zone", Schedules.zone()))
                            null
                        }
                        secrets.has(name) -> getString(R.string.phone_err_key_lost)
                        else -> getString(R.string.phone_no_key)
                    }
                } catch (e: Exception) {
                    why(e)
                }
                main.post {
                    if (isDestroyed) return@post
                    busy = false
                    renderButtons()
                    if (why == null) moveTaskToBack(true) else error.say(why)
                }
            }
        })
    }

    private fun togglePause() {
        if (busy || id.isEmpty()) return
        busy = true
        renderButtons()
        val to = !paused
        CoreClient.background {
            val r = runCatching { CoreClient.call(Schedules.cmd("schedule_pause", "id" to id, "paused" to to)) }
            r.onSuccess { Schedules.arm(this, it.optLong("next_ms")) }
            main.post {
                if (isDestroyed) return@post
                busy = false
                r.onSuccess { paused = to }.onFailure { error.say(why(it)) }
                renderButtons()
                schedulePreview()
            }
        }
    }

    private fun askDelete() {
        look.menu(getString(R.string.phone_sched_delete_q), listOf(getString(R.string.phone_sched_delete))) { delete() }
    }

    private fun delete() {
        if (busy || id.isEmpty()) return
        busy = true
        renderButtons()
        CoreClient.background {
            val r = runCatching { CoreClient.call(Schedules.cmd("schedule_delete", "id" to id)) }
            r.onSuccess { Schedules.arm(this, it.optLong("next_ms")) }
            main.post {
                if (isDestroyed) return@post
                busy = false
                r.onSuccess { finish() }.onFailure {
                    error.say(why(it))
                    renderButtons()
                }
            }
        }
    }

    // Why the core said no, in the person's words.
    private fun why(e: Throwable): String {
        if (e !is CoreClient.Refused) {
            Log.w(TAG, "the schedules failed", e)
            return getString(R.string.phone_sched_core_down)
        }
        return when (e.code) {
            "sched_no_task" -> getString(R.string.phone_sched_err_no_task)
            "sched_long" -> getString(R.string.phone_sched_err_long)
            "sched_no_time" -> getString(R.string.phone_sched_err_no_time)
            "sched_time" -> getString(R.string.phone_sched_err_time)
            "sched_too_many" -> resources.getQuantityString(R.plurals.phone_sched_err_too_many, ScheduleRules.MAX_TIMES, ScheduleRules.MAX_TIMES)
            "sched_date" -> getString(R.string.phone_sched_err_date)
            "sched_past" -> getString(R.string.phone_sched_err_past)
            "sched_no_days" -> getString(R.string.phone_sched_err_no_days)
            "sched_not_found" -> getString(R.string.phone_sched_err_not_found)
            "sched_running", "busy" -> getString(R.string.phone_err_busy)
            "no_device" -> getString(R.string.phone_err_no_device)
            "no_key" -> getString(R.string.phone_no_key)
            "no_model" -> getString(R.string.phone_err_no_model)
            "computer" -> getString(R.string.phone_err_computer)
            else -> {
                Log.w(TAG, "the core refused [${e.code}]: ${e.message}")
                getString(R.string.phone_sched_err_other)
            }
        }
    }

    private fun strings(a: JSONArray?): List<String> = (0 until (a?.length() ?: 0)).map { a!!.optString(it) }

    companion object {
        const val EXTRA_ID = "schedule"
        private const val TAG = "PinkyScreens"
    }
}

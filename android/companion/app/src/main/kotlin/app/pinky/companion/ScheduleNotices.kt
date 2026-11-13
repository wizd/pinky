package app.pinky.companion

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.res.Resources
import android.util.Log
import org.json.JSONObject
import java.time.OffsetDateTime
import java.util.concurrent.atomic.AtomicBoolean

/**
 * The person hears of the scheduled tasks nobody watched: how each went,
 * or why it was skipped (the core's State.Notices, schedules.go), and an
 * approval or a handoff one of them waits for. Notifications need the
 * person's yes on Android 13 and later (POST_NOTIFICATIONS), which
 * SchedulesActivity asks for; without it they are not shown, and each
 * schedule's last runs still say how it went.
 */
object ScheduleNotices {

    private const val TAG = "PinkySchedules"
    private const val RESULTS = "schedules"
    private const val WAITING = "schedule_waiting"
    private const val DOWN_ID = 199_999

    /** The approvals and handoffs notified, which are taken back once they are answered. */
    private val waiting = HashSet<String>()

    /**
     * Follows the core's state in the service, through the overlays'
     * subscription: tells the notices, notifies what an unattended task
     * waits for, and sets the schedules' alarm once the core is up.
     */
    class Watcher(private val context: Context) {
        private val synced = AtomicBoolean(false)

        fun onState(st: JSONObject) {
            if (!synced.getAndSet(true)) {
                CoreClient.background {
                    try {
                        Schedules.sync(context)
                    } catch (e: Exception) {
                        Log.w(TAG, "could not set the schedules' alarm", e)
                        synced.set(false)
                    }
                }
            }
            show(context, st)
        }
    }

    /** Tells the notices of the core's state now, for the alarm (Schedules.due). Off the main thread. */
    fun fromCore(context: Context) {
        try {
            CoreClient.call(CoreClient.cmd("state")).optJSONObject("state")?.let { show(context, it) }
        } catch (e: Exception) {
            Log.w(TAG, "could not read the notices", e)
        }
    }

    @Synchronized
    private fun show(context: Context, st: JSONObject) {
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        val res = context.resources
        val notices = st.optJSONArray("notices")
        if (notices != null && notices.length() > 0) {
            channels(context, nm)
            for (i in 0 until notices.length()) {
                val n = notices.optJSONObject(i) ?: continue
                val id = n.optLong("id")
                val text = ScheduleWords.notice(res, n)
                nm.notify(
                    ScheduleRules.noticeId(id),
                    build(context, RESULTS, res.getString(R.string.phone_sched_notify_title, n.optString("task")), text)
                        .setContentIntent(openSchedule(context, n.optString("schedule")))
                        .setWhen(n.optLong("due_ms").takeIf { it > 0 } ?: System.currentTimeMillis())
                        .setShowWhen(true)
                        .build(),
                )
                // Told, or not shown because the person turned notifications
                // off: the schedule's runs keep it either way.
                CoreClient.send(CoreClient.cmd("notice_seen", "notice" to id))
            }
        }
        // What a task nobody watches waits for. The card over other apps
        // has it too (Overlays): the notification brings the person there.
        val agent = st.optJSONObject("agent")
        val unattended = agent?.optBoolean("running") == true && agent.optBoolean("unattended")
        val now = HashSet<String>()
        val needs = st.optJSONArray("needs")
        if (unattended && needs != null) {
            for (i in 0 until needs.length()) {
                val need = needs.optJSONObject(i) ?: continue
                val id = need.optString("id")
                now += id
                if (id in waiting) continue
                channels(context, nm)
                val what = Words.shown(need, "title")
                nm.notify(
                    ScheduleRules.needId(id),
                    build(
                        context,
                        WAITING,
                        res.getString(R.string.phone_sched_waiting_title),
                        res.getString(R.string.phone_sched_waiting_text, agent?.optString("task").orEmpty(), what),
                    ).setContentIntent(openTask(context)).setCategory(Notification.CATEGORY_REMINDER).build(),
                )
            }
        }
        for (id in waiting - now) nm.cancel(ScheduleRules.needId(id))
        waiting.clear()
        waiting += now
    }

    /** The alarm fired while the core was not there: nothing could run. */
    fun coreDown(context: Context) {
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        channels(context, nm)
        val res = context.resources
        nm.notify(
            DOWN_ID,
            build(context, RESULTS, res.getString(R.string.phone_sched_down_title), res.getString(R.string.phone_sched_down_text))
                .setContentIntent(openTask(context))
                .build(),
        )
    }

    private fun build(context: Context, channel: String, title: String, text: String): Notification.Builder =
        Notification.Builder(context, channel)
            .setSmallIcon(R.drawable.ic_stat_pinky)
            .setContentTitle(title)
            .setContentText(text)
            .setStyle(Notification.BigTextStyle().bigText(text))
            .setAutoCancel(true)

    private fun channels(context: Context, nm: NotificationManager) {
        val res = context.resources
        nm.createNotificationChannel(
            NotificationChannel(RESULTS, res.getString(R.string.phone_sched_channel), NotificationManager.IMPORTANCE_DEFAULT)
                .apply { description = res.getString(R.string.phone_sched_channel_desc) },
        )
        nm.createNotificationChannel(
            NotificationChannel(WAITING, res.getString(R.string.phone_sched_waiting_channel), NotificationManager.IMPORTANCE_HIGH)
                .apply { description = res.getString(R.string.phone_sched_waiting_channel_desc) },
        )
    }

    private fun openSchedule(context: Context, id: String): PendingIntent = PendingIntent.getActivity(
        context,
        id.hashCode(),
        Intent(context, ScheduleEditActivity::class.java)
            .putExtra(ScheduleEditActivity.EXTRA_ID, id)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    private fun openTask(context: Context): PendingIntent = PendingIntent.getActivity(
        context,
        0,
        Intent(context, TaskActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )
}

/**
 * How the schedules read on the phone, as desktop Pinky says them
 * (ui/schedules.go): when a schedule runs, and how a run went.
 */
object ScheduleWords {

    /** What a notice (State.Notices) says. */
    fun notice(res: Resources, n: JSONObject): String = run(
        res,
        n.optString("end"),
        n.optString("reason"),
        n.optString("summary"),
        if (n.has("met")) n.optBoolean("met") else null,
        n.optInt("missed"),
    )

    /** How a run went, in a line: a schedule's run (schedule.Run) or a notice's. */
    fun run(res: Resources, end: String, reason: String, summary: String, met: Boolean?, missed: Int): String {
        val title: String
        var why = summary
        when {
            end == "skipped" -> {
                title = res.getString(R.string.phone_sched_skipped)
                why = skip(res, reason, missed)
            }
            end == "running" -> return res.getString(R.string.phone_sched_running)
            met == true -> title = res.getString(R.string.phone_sched_met)
            met == false -> title = res.getString(R.string.phone_sched_not_met)
            end == "done" -> title = res.getString(R.string.phone_end_done)
            end in setOf("stopped", "steps", "time", "cost") -> title = res.getString(R.string.phone_end_stopped)
            reason == "interrupted" -> {
                title = res.getString(R.string.phone_end_failed)
                why = res.getString(R.string.phone_sched_interrupted)
            }
            else -> title = res.getString(R.string.phone_end_failed)
        }
        return if (why.isEmpty()) title else res.getString(R.string.phone_end_line, title, why)
    }

    /** Why a time was skipped (schedule's Skip constants). */
    fun skip(res: Resources, reason: String, missed: Int): String = when (reason) {
        "locked" -> res.getString(R.string.phone_sched_skip_locked)
        "in_use" -> res.getString(R.string.phone_sched_skip_in_use)
        "busy" -> res.getString(R.string.phone_sched_skip_busy)
        "offline" -> res.getString(R.string.phone_sched_skip_offline)
        "no_key" -> res.getString(R.string.phone_sched_skip_no_key)
        "off" -> res.getString(R.string.phone_sched_skip_off)
        "not_running" -> res.getQuantityString(R.plurals.phone_sched_skip_not_running, missed + 1, missed + 1)
        else -> ""
    }

    /** When a schedule runs: "Every day at 08:00 and 20:00". */
    fun repeat(context: Context, s: JSONObject): String {
        val res = context.resources
        val times = (0 until (s.optJSONArray("times")?.length() ?: 0)).map { s.optJSONArray("times")!!.optString(it) }
        val sep = res.getString(R.string.phone_sched_list_sep)
        val at = times.joinToString(sep) { time(context, it) }
        return when (s.optString("repeat")) {
            "once" -> res.getString(R.string.phone_sched_sum_once, date(res, s.optString("date")), at)
            "weekdays" -> res.getString(R.string.phone_sched_sum_weekdays, at)
            "weekly" -> {
                val days = s.optJSONArray("days")
                val names = (0 until (days?.length() ?: 0)).map { weekday(res, days!!.optInt(it), short = true) }
                res.getString(R.string.phone_sched_sum_weekly, names.joinToString(sep), at)
            }
            "interval" -> {
                val n = s.optInt("every")
                res.getQuantityString(R.plurals.phone_sched_sum_interval, n, n, at)
            }
            else -> res.getString(R.string.phone_sched_sum_daily, at)
        }
    }

    /** A time of day as schedules keep it ("20:30"), on the phone's 12- or 24-hour clock. */
    fun time(context: Context, t: String): String {
        val (h, m) = ScheduleRules.parse(t) ?: return t
        val c = java.util.Calendar.getInstance().apply {
            set(java.util.Calendar.HOUR_OF_DAY, h)
            set(java.util.Calendar.MINUTE, m)
        }
        return android.text.format.DateFormat.getTimeFormat(context).format(c.time)
    }

    /** A date as schedules keep it ("2026-11-20"), as the phone writes dates. */
    fun date(res: Resources, d: String): String = try {
        val ld = java.time.LocalDate.parse(d)
        val locale = res.configuration.locales[0]
        val pattern = android.text.format.DateFormat.getBestDateTimePattern(locale, "yMMMEd")
        java.time.format.DateTimeFormatter.ofPattern(pattern, locale).format(ld)
    } catch (_: Exception) {
        d
    }

    /** A weekday, 0 for Sunday (schedule.Schedule.Days). */
    fun weekday(res: Resources, d: Int, short: Boolean): String {
        val day = java.time.DayOfWeek.of(if (d == 0) 7 else d)
        val style = if (short) java.time.format.TextStyle.SHORT else java.time.format.TextStyle.FULL
        return day.getDisplayName(style, res.configuration.locales[0])
    }

    /** A moment (Unix ms) as a run's row says it: its day and time. */
    fun moment(context: Context, ms: Long): String =
        context.getString(R.string.phone_sched_moment, Words.day(context.resources, ms, short = true), Words.clock(context, ms))

    /** A time of a schedule's JSON (Go's RFC 3339) in Unix ms; 0 when it has none. */
    fun ms(t: String): Long = try {
        if (t.isEmpty() || t.startsWith("0001-")) 0L else OffsetDateTime.parse(t).toInstant().toEpochMilli()
    } catch (_: Exception) {
        0L
    }
}

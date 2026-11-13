package app.pinky.companion

import android.app.AlarmManager
import android.app.KeyguardManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.PowerManager
import android.os.SystemClock
import android.util.Log
import android.view.accessibility.AccessibilityEvent
import org.json.JSONObject
import java.util.TimeZone

/**
 * Scheduled tasks on the phone (docs/notes/schedules.md). The core keeps the
 * schedules and decides what runs (internal/standalone/schedules.go), but a
 * Go timer does not survive Android's Doze. So this app sets an alarm for
 * the next time due, which the core names in each schedule reply
 * (next_ms), and when it fires sends schedule_due with the model's key and
 * settings, the phone's time zone, and what only the app knows: whether
 * the phone is locked, in the person's hands, or on-phone mode is off.
 *
 * The alarm is exact (setExactAndAllowWhileIdle) when Android lets Pinky
 * set exact alarms: "Alarms & reminders", which the person allows in
 * Android's settings (SchedulesActivity opens the page; Pinky never turns
 * it on itself). Without it the alarm is inexact (setAndAllowWhileIdle),
 * and Doze may hold it back for some minutes. WorkManager would be later
 * still: its periodic work runs every 15 minutes at best, and not at a set
 * time. Android clears alarms when the phone restarts, so ScheduleReceiver
 * sets it again then, after an update of the app, when the time or the
 * time zone changes, and when exact alarms are allowed; and the service
 * sets it again once the core is up (ScheduleNotices.Watcher).
 */
object Schedules {

    private const val TAG = "PinkySchedules"

    /** The alarm's action, which only this app sends (its PendingIntent is explicit). */
    const val ACTION_DUE = "app.pinky.companion.action.SCHEDULE_DUE"

    /** The phone's time zone, which every schedule command names: Go on Android has none of its own. */
    fun zone(): String = TimeZone.getDefault().id

    /** A schedule command of the core, with the phone's time zone. */
    fun cmd(name: String, vararg fields: Pair<String, Any>): JSONObject =
        CoreClient.cmd(name, "zone" to zone(), *fields)

    /** Whether Android lets Pinky set exact alarms ("Alarms & reminders"). */
    fun exactAllowed(context: Context): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) return true
        val am = context.getSystemService(AlarmManager::class.java) ?: return false
        return am.canScheduleExactAlarms()
    }

    /** Sets the alarm for nextMs (Unix ms; 0 or less clears it). */
    fun arm(context: Context, nextMs: Long) {
        val app = context.applicationContext
        val am = app.getSystemService(AlarmManager::class.java) ?: return
        val pi = PendingIntent.getBroadcast(
            app,
            0,
            Intent(app, ScheduleReceiver::class.java).setAction(ACTION_DUE),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        when (ScheduleRules.alarm(nextMs, exactAllowed(app))) {
            ScheduleRules.Alarm.NONE -> am.cancel(pi)
            ScheduleRules.Alarm.EXACT -> try {
                am.setExactAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, nextMs, pi)
            } catch (_: SecurityException) {
                // The person took "Alarms & reminders" back just now.
                am.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, nextMs, pi)
            }
            ScheduleRules.Alarm.INEXACT -> am.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, nextMs, pi)
        }
    }

    /** Sets the alarm from the core's schedules. Throws when the core is not there. Off the main thread. */
    fun sync(context: Context) {
        arm(context, CoreClient.call(cmd("schedules")).optLong("next_ms"))
    }

    /**
     * The alarm fired: the core runs or skips what is due, and the alarm is
     * set for what follows. A time skipped is told at once, from the
     * state; a run's end is told by the service's watcher when it ends.
     * Off the main thread.
     */
    fun due(context: Context) {
        val app = context.applicationContext
        val settings = TaskSettings(app)
        val id = settings.service
        // No key: the core skips the time and says so (no_key), unless the
        // service needs none.
        val key = Secrets(app).load(settings.keyName(id)).orEmpty()
        val now = phoneNow(app)
        val req = settings.modelRequest("schedule_due", key, id)
            .put("zone", zone())
            .put("locked", now.locked)
            .put("in_use", now.inUse)
            .put("off", !PinkyAccessibilityService.onPhoneMode(app))
        val r = try {
            CoreClient.call(req)
        } catch (e: Exception) {
            // The core runs in the service's process once the service is on;
            // it sets the alarm again when it starts.
            Log.w(TAG, "a schedule was due, but the core is not there", e)
            ScheduleNotices.coreDown(app)
            return
        }
        arm(app, r.optLong("next_ms"))
        ScheduleNotices.fromCore(app)
    }

    /** What the phone is like right now, for schedule_due. */
    class PhoneNow(val locked: Boolean, val inUse: Boolean)

    fun phoneNow(context: Context): PhoneNow {
        val pm = context.getSystemService(PowerManager::class.java)
        val km = context.getSystemService(KeyguardManager::class.java)
        val interactive = pm?.isInteractive ?: false
        val keyguard = km?.isKeyguardLocked ?: true
        return PhoneNow(
            ScheduleRules.locked(interactive, keyguard),
            ScheduleRules.inUse(interactive, keyguard, UserActivity.idleMs()),
        )
    }
}

/**
 * When the person last touched the phone, as the accessibility service's
 * events tell (ScheduleRules.touches), for whether a schedule finds the
 * phone in use. In the service's process, where the alarm's receiver runs
 * too.
 */
object UserActivity {

    @Volatile
    private var last = 0L // SystemClock.elapsedRealtime, 0 for never

    fun touch() {
        last = SystemClock.elapsedRealtime()
    }

    /** How long ago the person last touched the phone; Long.MAX_VALUE when never since the process started. */
    fun idleMs(): Long = if (last == 0L) Long.MAX_VALUE else SystemClock.elapsedRealtime() - last
}

/** The schedules' rules that need no phone, so that unit tests check them. */
object ScheduleRules {

    /** How recently a touch makes the phone in use. */
    const val IN_USE_MS = 60_000L

    /** The most times a day a schedule names (schedule.MaxTimes). */
    const val MAX_TIMES = 12

    /** The hours "Every few hours" offers (schedule.Intervals). */
    val INTERVALS = listOf(1, 2, 3, 4, 6, 8, 12)

    /** The cooldowns the editor offers, in minutes (ui/schedules.go, schedCooldowns). */
    val COOLDOWNS = listOf(0, 60, 24 * 60)

    /** The repeats the editor offers, in its order (schedule's Once to Interval). */
    val REPEATS = listOf("once", "daily", "weekdays", "weekly", "interval")

    /**
     * Locked: the keyguard shows or the screen is off. Pinky never unlocks
     * the phone or turns its screen on for a scheduled task.
     */
    fun locked(interactive: Boolean, keyguard: Boolean): Boolean = !interactive || keyguard

    /** In use: the screen is on and unlocked, and the person touched it a moment ago. */
    fun inUse(interactive: Boolean, keyguard: Boolean, idleMs: Long): Boolean =
        interactive && !keyguard && idleMs < IN_USE_MS

    /**
     * Whether an accessibility event is the person's doing: a tap, a
     * scroll, typing, or a new screen. Not a notification, and not content
     * that changes by itself (a clock, a video).
     */
    fun touches(eventType: Int): Boolean = when (eventType) {
        AccessibilityEvent.TYPE_VIEW_CLICKED,
        AccessibilityEvent.TYPE_VIEW_LONG_CLICKED,
        AccessibilityEvent.TYPE_VIEW_SCROLLED,
        AccessibilityEvent.TYPE_VIEW_TEXT_CHANGED,
        AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED,
        AccessibilityEvent.TYPE_TOUCH_INTERACTION_START,
        -> true
        else -> false
    }

    enum class Alarm { NONE, EXACT, INEXACT }

    /** Which alarm to set for nextMs. */
    fun alarm(nextMs: Long, exactAllowed: Boolean): Alarm = when {
        nextMs <= 0 -> Alarm.NONE
        exactAllowed -> Alarm.EXACT
        else -> Alarm.INEXACT
    }

    /** A time of day as schedules keep it, "08:05". */
    fun hhmm(hour: Int, minute: Int): String = "%02d:%02d".format(java.util.Locale.ROOT, hour, minute)

    /** The hour and minute of "08:05", or null. */
    fun parse(t: String): Pair<Int, Int>? {
        val m = Regex("""^(\d{1,2}):(\d{2})$""").matchEntire(t.trim()) ?: return null
        val h = m.groupValues[1].toInt()
        val min = m.groupValues[2].toInt()
        return if (h in 0..23 && min in 0..59) h to min else null
    }

    /** times with t added, sorted and each once, MAX_TIMES at most (the latest added is dropped past it). */
    fun addTime(times: List<String>, t: String): List<String> {
        if (t in times || times.size >= MAX_TIMES) return times
        return (times + t).sorted()
    }

    /** times with the one at i changed to t, sorted and each once. */
    fun changeTime(times: List<String>, i: Int, t: String): List<String> =
        times.toMutableList().also { it[i] = t }.distinct().sorted()

    /** The notification ID of a notice of the core (Notice.ID), apart from the others Pinky posts. */
    fun noticeId(id: Long): Int = NOTICE_IDS + (id % 100_000).toInt()

    /** The notification ID of an approval a scheduled task waits for. */
    fun needId(need: String): Int = NEED_IDS + Math.floorMod(need.hashCode(), 100_000)

    private const val NOTICE_IDS = 200_000
    private const val NEED_IDS = 300_000
}

/**
 * The schedules' alarm (Schedules.ACTION_DUE), and what clears or moves it:
 * a restart of the phone, an update of the app, a new time or time zone,
 * and "Alarms & reminders" allowed. Not exported; Android's own broadcasts
 * still reach it. In the service's process (the app's main one), where the
 * core runs.
 */
class ScheduleReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        val app = context.applicationContext
        val pending = goAsync()
        CoreClient.background {
            try {
                if (intent.action == Schedules.ACTION_DUE) {
                    Schedules.due(app)
                } else {
                    try {
                        Schedules.sync(app)
                    } catch (e: Exception) {
                        // After a restart the core is not up yet; the service
                        // sets the alarm once it is.
                        Log.i(TAG, "cannot set the schedules' alarm yet (${intent.action}): ${e.message}")
                    }
                }
            } finally {
                pending.finish()
            }
        }
    }

    companion object {
        private const val TAG = "PinkySchedules"
    }
}

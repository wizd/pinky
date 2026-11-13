package app.pinky.companion

import android.Manifest
import android.app.Activity
import android.app.NotificationManager
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.Log
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONObject

/**
 * Pinky's scheduled tasks on the phone (Schedules): what Android must let
 * Pinky do first, then each schedule with when it runs next. A schedule
 * opens in ScheduleEditActivity. Opened from the task screen, in its
 * process, and closed to agents as it is (Look).
 *
 * Pinky changes none of Android's settings itself: each row opens the page
 * where the person allows it.
 */
class SchedulesActivity : Activity() {

    private val main = Handler(Looper.getMainLooper())
    private lateinit var look: Look
    private lateinit var allowHeading: TextView
    private lateinit var allowGroup: LinearLayout
    private lateinit var notifyRow: Look.Row
    private lateinit var exactRow: Look.Row
    private lateinit var batteryRow: Look.Row
    private lateinit var list: LinearLayout
    private lateinit var note: TextView
    private var reads = 0

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        look = Look(this)
        val col = look.column()
        col.addView(look.header(getString(R.string.phone_schedules)), look.matchWrap())
        col.addView(look.paragraph(getString(R.string.phone_sched_note)), look.matchWrap())

        notifyRow = look.row(getString(R.string.phone_sched_notifications), desc = getString(R.string.phone_sched_notifications_desc)) {
            askNotifications()
        }
        exactRow = look.row(getString(R.string.phone_sched_exact), desc = getString(R.string.phone_sched_exact_desc)) { openExact() }
        batteryRow = look.row(getString(R.string.phone_battery), desc = getString(R.string.phone_sched_battery_desc)) {
            Battery.askUnrestricted(this)
        }
        allowHeading = look.section(getString(R.string.phone_sched_allow))
        col.addView(allowHeading, look.matchWrap())
        allowGroup = look.group()
        look.addRow(allowGroup, notifyRow.view)
        look.addRow(allowGroup, exactRow.view)
        look.addRow(allowGroup, batteryRow.view)
        col.addView(allowGroup, look.matchWrap())

        col.addView(look.section(getString(R.string.phone_sched_list)), look.matchWrap())
        list = look.group()
        col.addView(list, look.matchWrap())
        note = look.note()
        note.say("")
        col.addView(note, look.matchWrap())
        val add = look.button(getString(R.string.phone_sched_new), Look.Kind.PRIMARY) {
            startActivity(Intent(this, ScheduleEditActivity::class.java))
        }
        col.addView(look.buttons(add).apply { gravity = Gravity.CENTER }, look.below(12))
        look.setScreen(col)
    }

    override fun onResume() {
        super.onResume()
        // Back from the editor, or from Android's settings.
        renderAllow()
        read()
    }

    override fun onDestroy() {
        main.removeCallbacksAndMessages(null)
        look.dismissDialog()
        super.onDestroy()
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        renderAllow()
    }

    // What Android does not let Pinky do yet; the section hides once it lets it all.
    private fun renderAllow() {
        val notify = notificationsAllowed(this)
        val exact = Schedules.exactAllowed(this)
        val battery = Battery.unrestricted(this)
        notifyRow.view.shown(!notify)
        exactRow.view.shown(!exact)
        batteryRow.view.shown(!battery)
        notifyRow.value.say(getString(R.string.phone_off))
        exactRow.value.say(getString(R.string.phone_off))
        batteryRow.value.say(getString(R.string.phone_battery_optimized))
        allowHeading.shown(!(notify && exact && battery))
        allowGroup.shown(!(notify && exact && battery))
    }

    private fun askNotifications() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED &&
            !askedNotifications(this)
        ) {
            setAskedNotifications(this)
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQUEST_NOTIFY)
            return
        }
        // Asked before, or turned off in Android's settings: Android asks no
        // more, and the person turns them on there.
        look.open(Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(Settings.EXTRA_APP_PACKAGE, packageName))
    }

    private fun openExact() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            look.open(Intent(Settings.ACTION_REQUEST_SCHEDULE_EXACT_ALARM, Uri.fromParts("package", packageName, null)))
        }
    }

    private fun read() {
        val n = ++reads
        CoreClient.background {
            val r = runCatching { CoreClient.call(Schedules.cmd("schedules")) }
            r.onSuccess { Schedules.arm(this, it.optLong("next_ms")) }
            main.post {
                if (n != reads || isDestroyed) return@post
                r.onSuccess { render(it) }.onFailure { e ->
                    Log.w(TAG, "could not read the schedules", e)
                    list.removeAllViews()
                    list.shown(false)
                    note.say(getString(R.string.phone_sched_core_down))
                }
            }
        }
    }

    private fun render(r: JSONObject) {
        list.removeAllViews()
        val all = r.optJSONArray("schedules")
        val count = all?.length() ?: 0
        for (i in 0 until count) {
            val s = all!!.optJSONObject(i) ?: continue
            val id = s.optString("id")
            var desc = ScheduleWords.repeat(this, s)
            val cond = s.optString("condition")
            if (cond.isNotEmpty()) desc = getString(R.string.phone_sched_sum_condition, desc, cond)
            val row = look.row(s.optString("task"), if (s.optBoolean("paused")) getString(R.string.phone_sched_paused) else "", desc) {
                startActivity(Intent(this, ScheduleEditActivity::class.java).putExtra(ScheduleEditActivity.EXTRA_ID, id))
            }
            row.title.maxLines = 2
            look.addRow(list, row.view)
        }
        list.shown(count > 0)
        note.say(if (count == 0) getString(R.string.phone_sched_empty) else "")
        // The first schedule saved: Android 13 and later ask the person
        // whether Pinky may tell them how it goes.
        if (count > 0) {
            askNotificationsOnce(this)
        }
    }

    companion object {
        private const val TAG = "PinkyScreens"
        private const val REQUEST_NOTIFY = 1
        private const val PREFS = "schedules"
        private const val KEY_ASKED = "asked_notifications"

        /** Whether Pinky may post notifications: the person's yes on Android 13 and later, and not turned off. */
        fun notificationsAllowed(activity: Activity): Boolean {
            val nm = activity.getSystemService(NotificationManager::class.java) ?: return false
            return nm.areNotificationsEnabled()
        }

        fun askedNotifications(activity: Activity): Boolean =
            activity.getSharedPreferences(PREFS, MODE_PRIVATE).getBoolean(KEY_ASKED, false)

        fun setAskedNotifications(activity: Activity) {
            activity.getSharedPreferences(PREFS, MODE_PRIVATE).edit().putBoolean(KEY_ASKED, true).apply()
        }

        /**
         * Asks for notifications once, when the person saves a schedule:
         * Android 13 and later need their yes. Later, the screen's row
         * opens Android's settings instead.
         */
        fun askNotificationsOnce(activity: Activity) {
            if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
            if (activity.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED) return
            if (askedNotifications(activity)) return
            setAskedNotifications(activity)
            activity.requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQUEST_NOTIFY)
        }
    }
}

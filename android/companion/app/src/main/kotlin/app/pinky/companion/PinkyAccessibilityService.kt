package app.pinky.companion

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.AccessibilityServiceInfo
import android.annotation.SuppressLint
import android.content.ComponentName
import android.content.Intent
import android.content.SharedPreferences
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.Log
import android.view.accessibility.AccessibilityEvent
import org.json.JSONObject

/**
 * Pinky's accessibility service, which serves two masters
 * (docs/notes/android-app.md):
 *
 * - Pinky on a computer turns it on over adb only while an agent works on
 *   the phone, and off again afterwards: the phone's apps can see an
 *   accessibility service that is on, and banking apps refuse to sign in
 *   next to one that is not from an app store. The idle watchdog turns it
 *   off when no computer has used it for a minute.
 * - Pinky on the phone itself (on-phone mode, which the person turns on in
 *   Pinky's screen) keeps it on: it hosts Pinky's Go core (Core), which
 *   drives the phone through this service's own socket and the commands of
 *   Actions, and draws Pinky's controls over other apps (Overlays).
 *
 * Either way it serves the companion protocol
 * (docs/notes/companion-protocol.md) and changes nothing on the phone by
 * itself.
 */
class PinkyAccessibilityService : AccessibilityService() {

    val labels = AppLabels(this)
    val displays = Displays(this)
    val events = EventHub(labels, displays)

    lateinit var tree: TreeDumper
        private set

    lateinit var textSetter: TextSetter
        private set

    /** Where Pinky's own windows are, so that agents cannot touch them. */
    val ownUI = OwnUI(this)
    val actions = Actions(this)

    /** Pinky's pill and approval card, while on-phone mode is on. */
    var overlays: Overlays? = null

    // Pinky's floating button, when the person turned it on (QuickEntry).
    private var quickEntry: QuickEntry? = null
        private set

    /**
     * Whether on-phone mode is on: Pinky runs tasks on this phone by itself,
     * so the service stays on and the core runs. Kept in its own
     * preferences, so that it outlives a restart of the process or the
     * phone; turning the service off turns it off too.
     */
    @Volatile
    var onPhone = false
        private set

    /** Turn off after this long without a connection; 0 keeps the service on. */
    @Volatile
    var idleDisableMs = DEFAULT_IDLE_DISABLE_MS
        private set

    @Volatile
    var includeNotImportant = false
        private set

    private val notices by lazy { ScheduleNotices.Watcher(this) }

    private var server: CompanionServer? = null
    private val main = Handler(Looper.getMainLooper())

    // What config set, kept until the service is turned off, so that it
    // survives the service restarting: a reboot, an app update, a
    // uiautomator dump. Pinky until 2026-11-08 kept the service on for
    // good this way (idle_disable_ms 0).
    private val prefs: SharedPreferences by lazy { getSharedPreferences(PREFS, MODE_PRIVATE) }
    private val modePrefs: SharedPreferences by lazy { getSharedPreferences(MODE_PREFS, MODE_PRIVATE) }

    override fun onCreate() {
        super.onCreate()
        tree = TreeDumper(this)
        textSetter = TextSetter(this)
    }

    override fun onServiceConnected() {
        super.onServiceConnected()
        idleDisableMs = prefs.getLong(KEY_IDLE_DISABLE_MS, DEFAULT_IDLE_DISABLE_MS)
        onPhone = onPhoneBuild && Core.available(this) && modePrefs.getBoolean(KEY_ON_PHONE, false)
        includeNotImportant =
            serviceInfo.flags and AccessibilityServiceInfo.FLAG_INCLUDE_NOT_IMPORTANT_VIEWS != 0
        val keep = prefs.getBoolean(KEY_INCLUDE_NOT_IMPORTANT, includeNotImportant)
        if (keep != includeNotImportant) setIncludeNotImportant(keep)
        // A service that was only restarted keeps counting idle time from
        // its clients' last activity; one that was turned on starts now.
        if (!restarted) IdleClock.touch()
        restarted = false
        server?.stop()
        server = CompanionServer(this).also { it.start() }
        // Which display each open app is on, for toasts that come before
        // any window event of their app.
        server?.handler?.runOnWorker {
            val seed = displays.appDisplays()
            main.post { events.seed(seed) }
        }
        // Check now rather than 10 s from now, so that frequent rebinding
        // cannot postpone the check for ever.
        main.removeCallbacks(watchdog)
        main.post(watchdog)
        if (onPhone) startOnPhone()
        Log.i(TAG, "connected")
    }

    override fun onAccessibilityEvent(event: AccessibilityEvent?) {
        if (event == null) return
        events.onEvent(event)
        if (ScheduleRules.touches(event.eventType)) UserActivity.touch()
        if (event.eventType == AccessibilityEvent.TYPE_WINDOWS_CHANGED ||
            event.eventType == AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED
        ) {
            overlays?.windowsChanged()
            quickEntry?.windowsChanged()
        }
    }

    override fun onInterrupt() {}

    // commit, not apply: the process may be killed right after an unbind.
    @SuppressLint("ApplySharedPref")
    override fun onUnbind(intent: Intent?): Boolean {
        shutdown()
        // Android also unbinds the service without turning it off, for a
        // uiautomator dump or an update; only a service that was turned off
        // (in Android Settings, by Pinky or by the watchdog) forgets config
        // and on-phone mode, which Pinky's screen turns on again.
        val listed = listedInSettings()
        if (!listed) {
            prefs.edit().clear().commit()
            modePrefs.edit().clear().commit()
            onPhone = false
        }
        restarted = listed
        return super.onUnbind(intent)
    }

    override fun onDestroy() {
        shutdown()
        labels.shutdown()
        super.onDestroy()
    }

    /**
     * Runs a command that only Pinky on the phone needs: the result's JSON,
     * or null for an unknown command or a build without on-phone mode. It
     * runs on the connection's thread. Gestures and global actions are in
     * every build (RequestHandler): desktop Pinky uses them on background
     * displays of phones that drop the input adb injects there.
     */
    fun actionCommand(cmd: String, req: JSONObject): String? = if (!onPhoneBuild) null else when (cmd) {
        "screenshot" -> actions.screenshot(req)
        "launch" -> actions.launch(req)
        "apps" -> actions.apps()
        else -> null
    }

    /** The features of actionCommand and the service, for ping's list. */
    fun actionFeatures(): List<String> = if (!onPhoneBuild) emptyList() else
        listOf("screenshot", "launch", "apps", "on_phone")

    /**
     * Whether this build runs tasks on the phone (on_phone_ui): only then
     * may it take screenshots, launch apps and list them, and only then
     * does its service config ask for screenshots.
     */
    val onPhoneBuild: Boolean by lazy { resources.getBoolean(R.bool.on_phone_ui) }

    /**
     * Turns on-phone mode on or off, for Pinky's own screen: on starts the
     * core and the overlays and keeps the service on; off puts the overlays
     * away and lets the idle watchdog turn the service off again once no
     * computer uses it. The core stays loaded but idle until the process
     * ends: a Go runtime cannot be unloaded.
     *
     * The reply says why the core did not start once it tried and failed
     * (core_error): only this process knows, and Pinky's screens show it.
     */
    @SuppressLint("ApplySharedPref")
    fun setOnPhone(on: Boolean): String {
        if (!onPhoneBuild) throw RequestError("unsupported", "this build does not run tasks on the phone")
        if (on && !Core.available(this)) throw RequestError("unsupported", "this phone cannot run Pinky's core (${Core.ABI} only)")
        modePrefs.edit().putBoolean(KEY_ON_PHONE, on).commit()
        onPhone = on
        if (!on) IdleClock.touch() // a minute from now, not at once
        main.post { if (on) startOnPhone() else stopOnPhone() }
        val j = Json()
        j.beginObject()
        j.field("on_phone", on)
        j.fieldIfNotEmpty("core_error", Core.error)
        j.endObject()
        return j.toString()
    }

    private fun startOnPhone() {
        Core.start(this)
        // The overlays follow the core's state; so do the schedules'
        // notifications (ScheduleNotices), and the first state arms the
        // schedules' alarm (Schedules.sync).
        if (overlays == null) overlays = Overlays(this) { st -> notices.onState(st) }.also { it.start() }
        if (quickEntry == null) quickEntry = QuickEntry(this).also { it.start() }
    }

    private fun stopOnPhone() {
        overlays?.stop()
        overlays = null
        quickEntry?.stop()
        quickEntry = null
    }

    /**
     * Applies a config request and returns the resulting settings. They are
     * saved until the service is turned off; committed before the reply, so
     * an app update right after it cannot lose them.
     */
    @SuppressLint("ApplySharedPref")
    fun configure(req: JSONObject): String {
        val edit = prefs.edit()
        if (req.has("include_not_important")) {
            val v = req.getBoolean("include_not_important")
            setIncludeNotImportant(v)
            edit.putBoolean(KEY_INCLUDE_NOT_IMPORTANT, v)
        }
        if (req.has("idle_disable_ms")) {
            idleDisableMs = req.getLong("idle_disable_ms").coerceAtLeast(0)
            edit.putLong(KEY_IDLE_DISABLE_MS, idleDisableMs)
        }
        edit.commit()
        val j = Json()
        j.beginObject()
        j.field("include_not_important", includeNotImportant)
        j.field("idle_disable_ms", idleDisableMs)
        j.endObject()
        return j.toString()
    }

    private fun setIncludeNotImportant(v: Boolean) {
        val info = serviceInfo
        val flag = AccessibilityServiceInfo.FLAG_INCLUDE_NOT_IMPORTANT_VIEWS
        info.flags = if (v) info.flags or flag else info.flags and flag.inv()
        serviceInfo = info
        includeNotImportant = v
        // Cached nodes were fetched with the old flag.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) clearCache()
    }

    /**
     * Whether enabled_accessibility_services still lists this service. True
     * when the setting cannot be read, so that config is kept rather than
     * lost.
     */
    private fun listedInSettings(): Boolean = try {
        val me = ComponentName(this, javaClass)
        val value = Settings.Secure.getString(contentResolver, Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES)
        value.orEmpty().split(':').any { ComponentName.unflattenFromString(it.trim()) == me }
    } catch (e: SecurityException) {
        Log.w(TAG, "cannot read the enabled services", e)
        true
    }

    private fun shutdown() {
        stopOnPhone()
        main.removeCallbacks(watchdog)
        server?.stop()
        server = null
        events.closeAll()
    }

    // A backstop for a Pinky that crashed or lost the phone without turning
    // the service off: no computer for idleDisableMs turns it off. While an
    // agent works, Pinky keeps an event subscription open. This app's own
    // connections (the core, Pinky's screens) do not count: in on-phone
    // mode the service stays on anyway.
    private val watchdog = object : Runnable {
        override fun run() {
            val s = server ?: return
            val limit = if (onPhone) 0L else idleDisableMs
            val idle = IdleClock.idleMs()
            if (limit > 0 && s.externalCount == 0 && idle >= limit) {
                Log.i(TAG, "no client for ${idle / 1000} s (limit ${limit / 1000} s), turning the service off")
                disableSelf()
                return
            }
            main.postDelayed(this, WATCHDOG_INTERVAL_MS)
        }
    }

    companion object {
        /**
         * Whether the person turned on-phone mode on in Pinky's screen, as
         * kept: for the schedules' alarm (Schedules.due), which skips a
         * time while it is off.
         */
        fun onPhoneMode(context: android.content.Context): Boolean =
            context.getSharedPreferences(MODE_PREFS, android.content.Context.MODE_PRIVATE).getBoolean(KEY_ON_PHONE, false)

        private const val TAG = "PinkyService"
        const val DEFAULT_IDLE_DISABLE_MS = 60_000L

        // Whether the last unbind left the service turned on (a restart),
        // across service instances in this process. Main thread only.
        private var restarted = false
        private const val WATCHDOG_INTERVAL_MS = 10_000L
        private const val PREFS = "service_config"
        private const val KEY_IDLE_DISABLE_MS = "idle_disable_ms"
        private const val KEY_INCLUDE_NOT_IMPORTANT = "include_not_important"
        private const val MODE_PREFS = "mode"
        private const val KEY_ON_PHONE = "on_phone"
    }
}

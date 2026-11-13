package app.pinky.companion

import android.annotation.SuppressLint
import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.PowerManager
import android.provider.Settings

/**
 * What keeps Pinky running on the phone through a long task: Android's
 * battery limits for it, and the autostart switch some vendors add on top.
 *
 * Android stops or holds back an app whose battery use is optimized while
 * it is in the background; MIUI and HyperOS, EMUI, ColorOS and Funtouch OS
 * also stop apps that are not allowed to start on their own, accessibility
 * services included. Pinky asks the person to lift both; it changes
 * nothing itself. The screen stays on during a task anyway (the pill's
 * window keeps it on, Overlays).
 */
object Battery {

    /** Whether Android lets Pinky run without battery limits ("Unrestricted"). */
    fun unrestricted(context: Context): Boolean {
        val pm = context.getSystemService(PowerManager::class.java) ?: return true
        return pm.isIgnoringBatteryOptimizations(context.packageName)
    }

    /**
     * Asks Android to lift Pinky's battery limits: its own dialog, which
     * needs REQUEST_IGNORE_BATTERY_OPTIMIZATIONS (debug builds only for
     * now). Without the dialog, the list of apps' battery optimization, and
     * then App info, where the person changes it themselves.
     */
    @SuppressLint("BatteryLife") // Pinky is not in Google Play, and it must run while the person is elsewhere.
    fun askUnrestricted(activity: Activity) {
        val pkg = Uri.fromParts("package", activity.packageName, null)
        if (start(activity, Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, pkg))) return
        if (start(activity, Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))) return
        openAppInfo(activity)
    }

    /** Opens App info, where the person finds Pinky's battery use. */
    fun openAppInfo(activity: Activity) {
        start(activity, Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", activity.packageName, null)))
    }

    /** Whether this phone's vendor adds an autostart switch (AUTOSTART). */
    fun hasAutostart(): Boolean = vendor() != null

    /**
     * Opens the vendor's page with Pinky's autostart switch; App info when
     * none of its known pages opens (they move between versions).
     */
    fun openAutostart(activity: Activity) {
        for (c in AUTOSTART[vendor()].orEmpty()) {
            if (start(activity, Intent().setComponent(c))) return
        }
        openAppInfo(activity)
    }

    private fun vendor(): String? {
        val names = listOf(Build.MANUFACTURER, Build.BRAND).map { it.lowercase() }
        return AUTOSTART.keys.firstOrNull { v -> names.any { it in VENDORS.getValue(v) } }
    }

    private fun start(activity: Activity, intent: Intent): Boolean = try {
        activity.startActivity(intent)
        true
    } catch (_: ActivityNotFoundException) {
        false
    } catch (_: SecurityException) {
        // A vendor's page that is not exported, or the dialog without its permission.
        false
    }

    // The manufacturers and brands of each vendor's phones.
    private val VENDORS = mapOf(
        "xiaomi" to setOf("xiaomi", "redmi", "poco"),
        "huawei" to setOf("huawei", "honor"),
        "oppo" to setOf("oppo", "realme", "oneplus"),
        "vivo" to setOf("vivo", "iqoo"),
    )

    // Each vendor's autostart pages, newest first.
    private val AUTOSTART = mapOf(
        "xiaomi" to listOf(
            ComponentName("com.miui.securitycenter", "com.miui.permcenter.autostart.AutoStartManagementActivity"),
        ),
        "huawei" to listOf(
            ComponentName("com.huawei.systemmanager", "com.huawei.systemmanager.startupmgr.ui.StartupNormalAppListActivity"),
            ComponentName("com.hihonor.systemmanager", "com.hihonor.systemmanager.startupmgr.ui.StartupNormalAppListActivity"),
            ComponentName("com.huawei.systemmanager", "com.huawei.systemmanager.optimize.process.ProtectActivity"),
        ),
        "oppo" to listOf(
            ComponentName("com.coloros.safecenter", "com.coloros.safecenter.permission.startup.StartupAppListActivity"),
            ComponentName("com.coloros.safecenter", "com.coloros.safecenter.startupapp.StartupAppListActivity"),
            ComponentName("com.oppo.safe", "com.oppo.safe.permission.startup.StartupAppListActivity"),
        ),
        "vivo" to listOf(
            ComponentName("com.vivo.permissionmanager", "com.vivo.permissionmanager.activity.BgStartUpManagerActivity"),
            ComponentName("com.iqoo.secure", "com.iqoo.secure.ui.phoneoptimize.BgStartUpManager"),
        ),
    )
}

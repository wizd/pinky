package app.pinky.companion

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import app.pinky.companion.BackgroundScreen.Status
import org.json.JSONObject
import java.io.Closeable

/**
 * Settings › Background screen: tasks run on a second, hidden screen that
 * a floating window shows, so the person keeps using the phone meanwhile
 * (docs/notes/android-wadb.md). It needs Wireless debugging, which gives
 * Pinky a debugging connection to the phone: this screen says what that
 * is for, takes the person through Developer options, Wireless debugging
 * and pairing, and shows where it stands. Pinky never turns a system
 * setting on by itself; it opens the page and the person does it.
 *
 * In the screens' process (":ui"), closed to agents like the others (Look).
 */
class BackgroundActivity : Activity() {

    private val main = Handler(Looper.getMainLooper())
    private lateinit var look: Look
    private var sub: Closeable? = null
    private var shell: JSONObject? = null // the core's state.shell; null until it answers
    private var coreUp = false

    private lateinit var switchRow: Look.Row
    private lateinit var statusNote: TextView
    private lateinit var steps: LinearLayout
    private lateinit var stepsTitle: TextView
    private lateinit var devRow: Look.Row
    private lateinit var wirelessRow: Look.Row
    private lateinit var pairRow: Look.Row
    private lateinit var codeRow: Look.Row
    private lateinit var forgetGroup: LinearLayout

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        look = Look(this)
        val col = look.column()
        col.addView(look.header(getString(R.string.phone_bg_title)), look.matchWrap())
        col.addView(look.paragraph(getString(R.string.phone_bg_intro)), look.matchWrap())

        switchRow = look.row(getString(R.string.phone_bg_switch)) { toggle() }
        statusNote = look.note()
        val g = look.group()
        look.addRow(g, switchRow.view)
        col.addView(g, look.below(8))
        col.addView(statusNote, look.matchWrap())

        devRow = look.row(getString(R.string.phone_bg_step_dev), desc = getString(R.string.phone_bg_step_dev_desc)) {
            if (devOptionsOn()) Pairing.openWirelessDebugging(look) else look.open(Intent(Settings.ACTION_DEVICE_INFO_SETTINGS))
        }
        wirelessRow = look.row(getString(R.string.phone_bg_step_wireless), desc = getString(R.string.phone_bg_step_wireless_desc)) {
            Pairing.openWirelessDebugging(look)
        }
        pairRow = look.row(getString(R.string.phone_bg_step_pair), desc = getString(R.string.phone_bg_step_pair_desc)) { startPairing() }
        codeRow = look.row(getString(R.string.phone_bg_enter_code), desc = getString(R.string.phone_bg_code_desc)) { askCode() }
        stepsTitle = look.section(getString(R.string.phone_bg_steps))
        col.addView(stepsTitle, look.matchWrap())
        steps = look.group()
        for (r in listOf(devRow, wirelessRow, pairRow, codeRow)) look.addRow(steps, r.view)
        col.addView(steps, look.matchWrap())

        forgetGroup = look.group()
        look.addRow(
            forgetGroup,
            look.row(getString(R.string.phone_bg_forget), desc = getString(R.string.phone_bg_forget_desc)) { forget() }.view,
        )
        col.addView(forgetGroup, look.below(24))

        col.addView(look.section(getString(R.string.phone_bg_safety)), look.matchWrap())
        val safety = look.group()
        safety.addView(look.paragraph(getString(R.string.phone_bg_safety_body)), look.matchWrap())
        col.addView(safety, look.matchWrap())

        look.setScreen(col)
        Pairing.changed = { render() }
    }

    override fun onStart() {
        super.onStart()
        sub = CoreClient.subscribe(this, { st -> main.post { onState(st) } }, { main.post { onDown() } })
    }

    override fun onResume() {
        super.onResume()
        render() // back from Android's settings
    }

    override fun onStop() {
        sub?.close()
        sub = null
        super.onStop()
    }

    override fun onDestroy() {
        if (Pairing.changed != null) Pairing.changed = null
        look.dismissDialog()
        main.removeCallbacksAndMessages(null)
        super.onDestroy()
    }

    private fun onState(st: JSONObject) {
        val s = st.optJSONObject("shell")
        if (coreUp && s?.toString() == shell?.toString()) return
        coreUp = true
        shell = s
        if (s != null && !BackgroundScreen.needsPairing(Status.of(s.optString("status")))) Pairing.stop()
        render()
    }

    private fun onDown() {
        if (!coreUp && shell == null) {
            render()
            return
        }
        coreUp = false
        render()
    }

    private fun status(): Status = Status.of(shell?.optString("status"))

    private fun supported() = Build.VERSION.SDK_INT >= BackgroundScreen.MIN_SDK

    private fun devOptionsOn(): Boolean = try {
        Settings.Global.getInt(contentResolver, Settings.Global.DEVELOPMENT_SETTINGS_ENABLED, 0) != 0
    } catch (_: Exception) {
        false
    }

    private fun render() {
        val st = status()
        val on = shell?.optBoolean("on") == true
        switchRow.value.say(getString(if (on) R.string.phone_on else R.string.phone_off))
        look.enable(switchRow.view, coreUp && supported() && shell != null)
        statusNote.say(
            when {
                !supported() -> getString(R.string.phone_bg_needs_13)
                !coreUp -> getString(R.string.phone_bg_core_down)
                shell == null -> getString(R.string.phone_bg_unavailable)
                else -> statusLine(st)
            },
        )
        statusNote.setTextColor(if (st == Status.FAILED || st == Status.REFUSED) look.red else look.ink3)

        val setup = on && coreUp
        stepsTitle.shown(setup)
        steps.shown(setup)
        val dev = devOptionsOn()
        devRow.value.say(getString(if (dev) R.string.phone_on else R.string.phone_off))
        val wireless = st == Status.READY || st == Status.CONNECTED || (st != Status.NO_PORT && wirelessOn())
        wirelessRow.value.say(getString(if (wireless) R.string.phone_on else R.string.phone_off))
        look.enable(wirelessRow.view, dev)
        val needsPairing = BackgroundScreen.needsPairing(st)
        pairRow.value.say(
            when {
                !needsPairing -> getString(R.string.phone_bg_paired_short)
                Pairing.result == Pairing.Result.Pairing -> getString(R.string.phone_bg_pairing)
                else -> ""
            },
        )
        look.enable(pairRow.view, dev && needsPairing)
        codeRow.view.shown(needsPairing)
        codeRow.desc.say(pairingNote() ?: getString(R.string.phone_bg_code_desc))
        forgetGroup.shown(coreUp && shell != null && !needsPairing && st != Status.OFF)
    }

    // Wireless debugging's setting, where Android lets apps read it.
    private fun wirelessOn(): Boolean = try {
        Settings.Global.getInt(contentResolver, "adb_wifi_enabled", 0) != 0
    } catch (_: Exception) {
        false
    }

    private fun statusLine(st: Status): String = when (st) {
        Status.OFF -> getString(R.string.phone_bg_status_off)
        Status.UNPAIRED -> getString(R.string.phone_bg_status_unpaired)
        Status.NO_PORT -> getString(R.string.phone_bg_status_no_port)
        Status.READY -> getString(R.string.phone_bg_status_ready)
        Status.CONNECTED -> getString(R.string.phone_bg_status_connected)
        Status.REFUSED -> getString(R.string.phone_bg_status_refused)
        Status.FAILED -> getString(R.string.phone_bg_status_failed, shell?.optString("error").orEmpty())
    }

    private fun pairingNote(): String? = when (val r = Pairing.result) {
        Pairing.Result.WrongCode -> getString(R.string.phone_bg_wrong_code)
        Pairing.Result.NoDialog -> getString(R.string.phone_bg_no_dialog)
        is Pairing.Result.Failed -> getString(R.string.phone_bg_pair_failed, r.why)
        else -> null
    }

    private fun toggle() {
        val on = shell?.optBoolean("on") == true
        if (on) {
            send("off")
            Pairing.stop()
            return
        }
        // Turning it on is the person's yes to Pinky holding a debugging
        // connection: say exactly what for, first.
        look.confirm(
            getString(R.string.phone_bg_confirm_title),
            getString(R.string.phone_bg_confirm_body),
            getString(R.string.phone_bg_confirm_ok),
        ) { send("on") }
    }

    private fun send(op: String) {
        CoreClient.send(CoreClient.cmd("shell", "op" to op)) { err ->
            if (err != null) main.post { toast(err.message ?: err.toString()) }
        }
    }

    private fun startPairing() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU && !Pairing.canNotify(this)) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQUEST_NOTIFY)
            return
        }
        Pairing.start(this)
        Pairing.openWirelessDebugging(look)
        toast(getString(R.string.phone_bg_pair_toast))
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode != REQUEST_NOTIFY) return
        // Without notifications the code goes into this screen's own field,
        // with the two side by side in split screen.
        Pairing.start(this)
        Pairing.openWirelessDebugging(look)
        if (!Pairing.canNotify(this)) toast(getString(R.string.phone_bg_no_notify))
    }

    private fun askCode() {
        look.edit(
            getString(R.string.phone_bg_enter_code),
            getString(R.string.phone_bg_code_dialog_desc),
            "",
            getString(R.string.phone_bg_code_hint),
            Look.Field.TEXT,
            check = { if (BackgroundScreen.pairingCode(it) == null) getString(R.string.phone_bg_not_a_code) else null },
        ) { typed ->
            Pairing.pair(this, BackgroundScreen.pairingCode(typed)!!)
            render()
        }
    }

    private fun forget() {
        look.confirm(
            getString(R.string.phone_bg_forget),
            getString(R.string.phone_bg_forget_confirm),
            getString(R.string.phone_bg_forget),
        ) { send("forget") }
    }

    private fun toast(s: String) {
        Toast.makeText(applicationContext, s, Toast.LENGTH_LONG).show()
    }

    companion object {
        private const val REQUEST_NOTIFY = 1
    }
}

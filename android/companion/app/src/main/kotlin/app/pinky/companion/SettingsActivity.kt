package app.pinky.companion

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import org.json.JSONObject
import java.io.Closeable
import java.io.IOException

/**
 * Settings for Pinky on the phone, in the task screen's process (":ui"),
 * as desktop Pinky has them in Settings › Ask Pinky and Settings ›
 * Approvals (ui/ask.go): the AI model, its key and its address; a task's
 * limits; when Pinky asks; Pinky's settings in Android; its version and
 * updates.
 *
 * The model, its address and the limits are kept here (TaskSettings), the
 * keys in Secrets, each per service. The model's check and what it used
 * come from the core (the control socket's check and usage commands). When Pinky asks belongs to the core
 * (policy.Preset), so it changes only while the core runs. Like the task
 * screen, this one is closed to agents (Look).
 */
class SettingsActivity : Activity() {

    private val main = Handler(Looper.getMainLooper())
    private lateinit var look: Look
    private lateinit var settings: TaskSettings
    private lateinit var secrets: Secrets
    private var sub: Closeable? = null

    private var coreUp = false // the core answers, so when Pinky asks can change
    private var backgroundOn = false // the core's state.shell.on
    private var known = false // the core answered, or could not be reached, at least once
    private var preset = 0 // the core's policy.Preset; 0 until it says
    private var checking = false
    private var updateFound: UpdateCheck.Update? = null
    private var updateNote = ""
    private var open: Open? = null // the dialog opened last, to open again with the screen
    private var checkRun = "" // the model a check runs for (checkSig), or ""
    private var checkFor = "" // the model the last check was of
    private var checkResult: JSONObject? = null // the core's pilot.CheckResult, or {"problem":"core_down"}
    private var usage: List<ModelUsage.Row> = emptyList()
    private var usageReads = 0

    private lateinit var serviceRow: Look.Row
    private lateinit var keyRow: Look.Row
    private lateinit var modelRow: Look.Row
    private lateinit var urlRow: Look.Row
    private lateinit var checkRow: Look.Row
    private lateinit var usageRows: List<Look.Row>
    private lateinit var usageDays: LinearLayout
    private lateinit var stepsRow: Look.Row
    private lateinit var timeRow: Look.Row
    private lateinit var costRow: Look.Row
    private val presets = ArrayList<Pair<Int, Look.Choice>>()
    private lateinit var presetNote: TextView
    private lateinit var accessibilityRow: Look.Row
    private lateinit var batteryRow: Look.Row
    private lateinit var backgroundRow: Look.Row
    private lateinit var floatingRow: Look.Row
    private lateinit var updateRow: Look.Row

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        look = Look(this)
        settings = TaskSettings(this)
        secrets = Secrets(this)
        updateFound = UpdateCheck.offered(this)?.also { updateNote = getString(R.string.phone_update_available, it.version) }

        val col = look.column()
        col.addView(look.header(getString(R.string.phone_settings)), look.matchWrap())

        serviceRow = look.row(getString(R.string.phone_service), desc = getString(R.string.phone_service_desc)) { chooseService() }
        keyRow = look.row(getString(R.string.phone_key), desc = getString(R.string.phone_key_desc)) { keyMenu() }
        modelRow = look.row(getString(R.string.phone_model)) { chooseModel() }
        urlRow = look.row(getString(R.string.phone_url)) { editUrl() }
        checkRow = look.row(getString(R.string.phone_check), desc = getString(R.string.phone_check_desc)) { runCheck() }
        addGroup(col, R.string.phone_section_model, serviceRow.view, keyRow.view, modelRow.view, urlRow.view, checkRow.view)

        stepsRow = look.row(getString(R.string.phone_steps_limit), desc = getString(R.string.phone_steps_limit_desc)) { chooseSteps() }
        timeRow = look.row(getString(R.string.phone_time_limit), desc = getString(R.string.phone_time_limit_desc)) { chooseTime() }
        costRow = look.row(getString(R.string.phone_cost_limit)) { chooseCost() }
        addGroup(col, R.string.phone_section_limits, stepsRow.view, timeRow.view, costRow.view)

        val memoryRow = look.row(getString(R.string.phone_memory), desc = getString(R.string.phone_memory_desc)) {
            startActivity(Intent(this, MemoryActivity::class.java))
        }
        addGroup(col, R.string.phone_memory, memoryRow.view)

        usageRows = listOf(R.string.phone_today, R.string.phone_usage_week, R.string.phone_usage_month).map {
            look.row(getString(it), getString(R.string.phone_usage_none))
        }
        addGroup(col, R.string.phone_section_usage, *usageRows.map { it.view }.toTypedArray())
        col.addView(look.note(getString(R.string.phone_usage_desc)), look.matchWrap())
        usageDays = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        col.addView(usageDays, look.matchWrap())

        for ((id, title, desc) in PRESETS) {
            presets += id to look.choice(getString(title), getString(desc)) { setPreset(id) }
        }
        addGroup(col, R.string.phone_section_ask, *presets.map { it.second.view }.toTypedArray())
        presetNote = look.note(getString(R.string.phone_preset_core_down))
        col.addView(presetNote, look.matchWrap())

        accessibilityRow = look.row(getString(R.string.phone_accessibility), desc = getString(R.string.phone_accessibility_desc)) {
            look.openAccessibility()
        }
        val appInfo = look.row(getString(R.string.phone_app_info), desc = getString(R.string.phone_app_info_desc)) {
            look.openAppInfo()
        }
        batteryRow = look.row(getString(R.string.phone_battery), desc = getString(R.string.phone_battery_desc)) {
            if (Battery.unrestricted(this)) Battery.openAppInfo(this) else Battery.askUnrestricted(this)
        }
        backgroundRow = look.row(getString(R.string.phone_bg_title), desc = getString(R.string.phone_bg_row_desc)) {
            look.open(Intent(this, BackgroundActivity::class.java))
        }
        floatingRow = look.row(getString(R.string.phone_floating), desc = getString(R.string.phone_floating_desc)) {
            QuickAccess.setBubble(this, !QuickAccess.bubbleOn(this))
            render()
        }
        val phoneRows = mutableListOf(accessibilityRow.view, batteryRow.view, backgroundRow.view, floatingRow.view)
        // Only on phones whose vendor adds the switch; whether it is on cannot be read.
        if (Battery.hasAutostart()) {
            phoneRows += look.row(getString(R.string.phone_autostart), desc = getString(R.string.phone_autostart_desc)) {
                Battery.openAutostart(this)
            }.view
        }
        phoneRows += appInfo.view
        addGroup(col, R.string.phone_section_phone, *phoneRows.toTypedArray())

        val version = look.row(getString(R.string.phone_version), "${BuildConfig.VERSION_NAME} (${BuildConfig.VERSION_CODE})")
        updateRow = look.row(getString(R.string.phone_update_check)) { onUpdateRow() }
        addGroup(col, R.string.phone_section_about, version.view, updateRow.view)

        look.setScreen(col)
    }

    override fun onStart() {
        super.onStart()
        sub = CoreClient.subscribe(this, { st -> main.post { onState(st) } }, { main.post { onDown() } })
        readUsage()
    }

    // The screen is made anew when it is turned, or its theme or language
    // changes: the dialog open then opens again, with what was typed in its
    // box. Never a key: its box starts again empty.

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        val o = open
        if (o != null && look.dialogOpen()) {
            outState.putString(STATE_OPEN, o.name)
            look.dialogText()?.let { outState.putString(STATE_TEXT, it) }
        }
    }

    override fun onRestoreInstanceState(savedInstanceState: Bundle) {
        super.onRestoreInstanceState(savedInstanceState)
        val o = savedInstanceState.getString(STATE_OPEN)?.let { runCatching { Open.valueOf(it) }.getOrNull() }
        val typed = savedInstanceState.getString(STATE_TEXT)
        when (o) {
            Open.SERVICE -> chooseService()
            Open.KEY_MENU -> keyMenu()
            Open.KEY -> askKey(settings.keyName())
            Open.MODEL -> chooseModel(typed)
            Open.URL -> editUrl(typed)
            Open.STEPS -> chooseSteps()
            Open.TIME -> chooseTime()
            Open.COST -> chooseCost()
            null -> {}
        }
    }

    override fun onResume() {
        super.onResume()
        // The person may come back from Android's settings.
        render()
    }

    override fun onStop() {
        sub?.close()
        sub = null
        super.onStop()
    }

    override fun onDestroy() {
        look.dismissDialog()
        main.removeCallbacksAndMessages(null)
        super.onDestroy()
    }

    private fun onState(st: JSONObject) {
        val p = st.optInt("preset", 0)
        val bg = st.optJSONObject("shell")?.optBoolean("on") == true
        if (coreUp && p == preset && bg == backgroundOn) return
        coreUp = true
        known = true
        preset = p
        backgroundOn = bg
        render()
    }

    private fun onDown() {
        if (known && !coreUp) return
        coreUp = false
        known = true
        render()
    }

    private fun render() {
        val id = settings.service
        val svc = settings.svc(id)
        serviceRow.value.say(settings.serviceLabel(resources, svc))
        val hasKey = secrets.has(svc.keyName)
        keyRow.value.say(
            getString(
                when {
                    hasKey -> R.string.phone_key_saved
                    svc.keyOptional -> R.string.phone_key_none
                    else -> R.string.phone_key_add
                },
            ),
        )
        keyRow.value.setTextColor(if (hasKey || svc.keyOptional) look.ink3 else look.accent)
        keyRow.desc.say(getString(if (svc.keyOptional) R.string.phone_key_optional_desc else R.string.phone_key_desc))
        val model = settings.modelLabel(id)
        modelRow.value.say(model.ifEmpty { getString(R.string.phone_model_none) })
        modelRow.value.setTextColor(if (model.isEmpty()) look.accent else look.ink3)
        modelRow.desc.say(modelDesc(svc))
        urlRow.value.say(settings.address(id).ifEmpty { getString(R.string.phone_url_default) })
        urlRow.desc.say(urlDesc(svc))
        renderCheck()

        stepsRow.value.say(Words.steps(resources, settings.maxSteps))
        timeRow.value.say(Words.minutes(resources, settings.maxMinutes))
        costRow.value.say(costLabel(settings.maxCost))
        costRow.desc.say(
            getString(if (settings.priced(id)) R.string.phone_cost_limit_desc else R.string.phone_cost_limit_desc_tokens),
        )

        for ((id, c) in presets) {
            c.radio.isChecked = id == preset
            look.enable(c.view, coreUp)
        }
        presetNote.shown(known && !coreUp)

        accessibilityRow.value.say(getString(if (look.serviceOn()) R.string.phone_on else R.string.phone_off))
        floatingRow.value.say(getString(if (QuickAccess.bubbleOn(this)) R.string.phone_on else R.string.phone_off))
        val unrestricted = Battery.unrestricted(this)
        batteryRow.value.say(getString(if (unrestricted) R.string.phone_battery_unrestricted else R.string.phone_battery_optimized))
        batteryRow.value.setTextColor(if (unrestricted) look.ink3 else look.accent)
        backgroundRow.value.say(getString(if (backgroundOn) R.string.phone_on else R.string.phone_off))

        updateRow.desc.say(if (UpdateCheck.enabled) updateNote else getString(R.string.phone_update_dev))
        updateRow.value.say(if (updateFound != null) getString(R.string.phone_update_download) else "")
        updateRow.value.setTextColor(look.accent)
        look.enable(updateRow.view, UpdateCheck.enabled && !checking)
    }

    // The AI model.

    private fun chooseService() {
        open = Open.SERVICE
        val list = ModelServices.all(this)
        look.choose(
            getString(R.string.phone_service),
            list.map { settings.serviceLabel(resources, it) },
            list.indexOfFirst { it.id == settings.service },
        ) { i ->
            settings.service = list[i].id
            render()
        }
    }

    private fun keyMenu() {
        val name = settings.keyName()
        if (!secrets.has(name)) {
            askKey(name)
            return
        }
        open = Open.KEY_MENU
        look.menu(
            getString(R.string.phone_key),
            listOf(getString(R.string.phone_key_change), getString(R.string.phone_key_remove)),
        ) { i ->
            if (i == 0) {
                askKey(name)
            } else {
                secrets.forget(name)
                checkFor = ""
                checkResult = null
                toast(getString(R.string.phone_key_removed_toast))
                render()
            }
        }
    }

    // Adds a key, or changes the saved one, under its name (TaskSettings.keyName); then checks the model with it.
    private fun askKey(name: String) {
        open = Open.KEY
        val title = getString(if (secrets.has(name)) R.string.phone_key_change else R.string.phone_setup_key)
        val desc = getString(if (settings.keyOptional()) R.string.phone_key_optional_desc else R.string.phone_key_desc)
        look.edit(title, desc, "", getString(R.string.phone_key_hint), Look.Field.SECRET) { v ->
            val key = v.trim()
            // The Keystore may take a moment, the first time above all.
            CoreClient.background {
                val saved = try {
                    secrets.save(name, key)
                    true
                } catch (e: Exception) {
                    // Only its class: its message could say how long the key is.
                    Log.w(TAG, "could not save the key: ${e.javaClass.name}")
                    false
                }
                main.post {
                    toast(getString(if (saved) R.string.phone_key_saved_toast else R.string.phone_key_err_save))
                    render()
                    if (saved && !isDestroyed) runCheck()
                }
            }
        }
    }

    // typed is what the box held when the screen was made anew: the box
    // was open, rather than the list.
    private fun chooseModel(typed: String? = null) {
        open = Open.MODEL
        val id = settings.service
        val svc = settings.svc(id)
        if (typed != null || svc.models.isEmpty()) {
            typeModel(id, typed)
            return
        }
        // The models the service suggests, the one set some other way, and Other… to type one.
        val cur = settings.model(id)
        val ids = svc.models.map { it.id }.toMutableList()
        val labels = svc.models.map { it.label }.toMutableList()
        if (cur.isNotEmpty() && cur !in ids) {
            ids += cur
            labels += cur
        }
        look.choose(getString(R.string.phone_model), labels + getString(R.string.phone_model_other), ids.indexOf(cur)) { i ->
            if (i == ids.size) typeModel(id, null) else setModel(id, ids[i])
        }
    }

    private fun typeModel(id: String, typed: String?) {
        open = Open.MODEL
        look.edit(
            getString(R.string.phone_model),
            modelDesc(settings.svc(id)),
            typed ?: settings.model(id),
            getString(R.string.phone_model_hint),
            Look.Field.TEXT,
        ) { v -> setModel(id, v) }
    }

    private fun setModel(id: String, v: String) {
        settings.setModel(id, v)
        render()
        runCheck()
    }

    private fun editUrl(typed: String? = null) {
        open = Open.URL
        val id = settings.service
        val svc = settings.svc(id)
        look.edit(
            getString(R.string.phone_url),
            urlDesc(svc),
            typed ?: settings.address(id),
            svc.baseUrl.ifEmpty { if (svc.api == TaskSettings.OPENAI) "https://…/v1" else "https://api.anthropic.com" },
            Look.Field.URL,
            { v -> if (TaskSettings.urlOK(v)) null else getString(R.string.phone_url_err) },
        ) { v ->
            settings.setBaseUrl(id, v)
            render()
            runCheck()
        }
    }

    private fun modelDesc(svc: ModelServices.Service): String = getString(
        when {
            svc.id == TaskSettings.ANTHROPIC -> R.string.phone_model_claude_desc
            svc.models.isNotEmpty() -> R.string.phone_model_suggested_desc
            svc.local -> R.string.phone_model_local_desc
            else -> R.string.phone_model_desc
        },
    )

    private fun urlDesc(svc: ModelServices.Service): String = getString(
        when {
            svc.id == ModelServices.CUSTOM_ANTHROPIC -> R.string.phone_url_desc_anthropic
            svc.id == ModelServices.CUSTOM_OPENAI -> R.string.phone_url_desc_openai
            svc.local -> R.string.phone_url_desc_local
            else -> R.string.phone_url_desc_service
        },
    )

    // The model's check: one tiny request, in the core (pilot.Check).

    /** The model a check is of. */
    private fun checkSig(): String = settings.service + "\n" + settings.address() + "\n" + settings.model()

    private fun runCheck() {
        val sig = checkSig()
        if (checkRun == sig) return
        checkRun = sig
        renderCheck()
        val id = settings.service
        val req = settings.checkRequest("", id)
        val name = settings.keyName(id)
        CoreClient.background {
            val res = try {
                // The key goes to the core for this one request; it is not kept there.
                req.put("key", secrets.load(name).orEmpty())
                CoreClient.call(req).optJSONObject("check") ?: JSONObject().put("problem", "bad_answer")
            } catch (e: Exception) {
                Log.w(TAG, "could not check the model: ${e.javaClass.name}")
                JSONObject().put("problem", "core_down")
            }
            main.post {
                if (checkRun != sig || isDestroyed) return@post
                checkRun = ""
                checkFor = sig
                checkResult = res
                renderCheck()
                readUsage() // the check used a few tokens
            }
        }
    }

    private fun renderCheck() {
        val sig = checkSig()
        val res = checkResult?.takeIf { checkFor == sig }
        when {
            checkRun == sig -> {
                checkRow.value.say(getString(R.string.phone_checking))
                checkRow.value.setTextColor(look.ink3)
            }
            else -> {
                checkRow.value.say(getString(if (res != null) R.string.phone_check_again else R.string.phone_check_now))
                checkRow.value.setTextColor(look.accent)
            }
        }
        if (res == null || checkRun == sig) {
            checkRow.desc.say(getString(R.string.phone_check_desc))
            checkRow.desc.setTextColor(look.ink3)
            return
        }
        val ok = res.optBoolean("ok")
        val detail = checkDetail(res)
        checkRow.desc.say(checkText(res) + if (detail.isEmpty()) "" else "\n" + getString(R.string.phone_check_detail, detail))
        checkRow.desc.setTextColor(if (ok) look.green else look.red)
    }

    /** How a check went, in the phone's language (ui/ask.go, checkText). */
    private fun checkText(res: JSONObject): String {
        if (res.optBoolean("ok")) {
            return getString(R.string.phone_check_ok, String.format(java.util.Locale.ROOT, "%.1f", res.optLong("ms") / 1000.0))
        }
        return getString(
            when (res.optString("problem")) {
                "core_down" -> R.string.phone_check_core_down
                "no_model" -> R.string.phone_check_no_model
                "no_key" -> R.string.phone_check_no_key
                "bad_key" -> R.string.phone_check_bad_key
                "no_credit" -> R.string.phone_check_no_credit
                "not_found" -> R.string.phone_check_not_found
                "busy" -> R.string.phone_check_busy
                // With an HTTP status the service refused the request: the model never answered.
                "no_tools" -> if (res.optInt("status") != 0) R.string.phone_check_tools_unsupported else R.string.phone_check_no_tools
                "tools_api" -> R.string.phone_check_tools_api
                "unreachable" -> R.string.phone_check_unreachable
                "bad_answer" -> R.string.phone_check_bad_answer
                else -> R.string.phone_check_rejected
            },
        )
    }

    /** What the service said of a failed check, with its HTTP status; "" for nothing worth showing (checkDetail). */
    private fun checkDetail(res: JSONObject): String {
        val detail = res.optString("detail")
        val status = res.optInt("status")
        if (res.optBoolean("ok") || detail.isEmpty() || res.optString("problem") == "no_tools" && status == 0) return ""
        return if (status != 0) "HTTP $status · $detail" else detail
    }

    // What the model used.

    private fun readUsage() {
        val n = ++usageReads
        CoreClient.background {
            val rows = try {
                ModelUsage.read()
            } catch (_: Exception) {
                null // the core is not there: what was read stays
            }
            main.post {
                if (rows == null || n != usageReads || isDestroyed) return@post
                usage = rows
                renderUsage()
            }
        }
    }

    private fun renderUsage() {
        for ((row, days) in usageRows.zip(listOf(0, 6, 29))) {
            val t = ModelUsage.total(usage, ModelUsage.dayOf(-days))
            row.value.say(if (t.tokens > 0) ModelUsage.amount(resources, t) else getString(R.string.phone_usage_none))
        }
        // The last days, by service and model.
        usageDays.removeAllViews()
        val days = usage.map { it.day }.distinct().take(USAGE_DAYS)
        if (days.isEmpty()) return
        val g = look.group()
        for (day in days) {
            val ms = ModelUsage.dayMillis(day) ?: continue
            val lines = usage.filter { it.day == day }.joinToString("\n") { r ->
                ModelUsage.who(settings, resources, r) + "\n" +
                    resources.getQuantityString(R.plurals.phone_usage_requests, r.requests, r.requests) + " · " +
                    ModelUsage.amount(resources, r.tokens, r.cost, r.unpriced)
            }
            look.addRow(g, look.row(Words.day(resources, ms), desc = lines).view)
        }
        usageDays.addView(g, look.below(12))
    }

    // A task's limits.

    private fun chooseSteps() {
        open = Open.STEPS
        chooseLimit(R.string.phone_steps_limit, TaskSettings.STEP_CHOICES, settings.maxSteps, { Words.steps(resources, it) }) {
            settings.maxSteps = it
        }
    }

    private fun chooseTime() {
        open = Open.TIME
        chooseLimit(R.string.phone_time_limit, TaskSettings.MINUTE_CHOICES, settings.maxMinutes, { Words.minutes(resources, it) }) {
            settings.maxMinutes = it
        }
    }

    private fun chooseCost() {
        open = Open.COST
        chooseLimit(R.string.phone_cost_limit, TaskSettings.COST_CHOICES, settings.maxCost, { costLabel(it) }) {
            settings.maxCost = it
        }
    }

    private fun <T> chooseLimit(title: Int, choices: List<T>, cur: T, label: (T) -> String, set: (T) -> Unit) {
        // A value set some other way stays on the list, first.
        val options = if (cur in choices) choices else listOf(cur) + choices
        look.choose(getString(title), options.map(label), options.indexOf(cur)) { i ->
            set(options[i])
            render()
        }
    }

    /** The cost limit as the person reads it: dollars, or tokens for a model without prices (pilot.Config.TokenLimit). */
    private fun costLabel(v: Double): String = Words.limit(resources, settings.priced(), v, (v * 1_000_000).toLong())

    // When Pinky asks.

    private fun setPreset(id: Int) {
        if (!coreUp || id == preset) return
        val was = preset
        preset = id
        render()
        CoreClient.send(CoreClient.cmd("preset", "preset" to id)) { err ->
            // The core's next state says what holds; until then, the old choice.
            if (err != null) {
                main.post {
                    preset = was
                    render()
                }
            }
        }
    }

    // Updates.

    private fun onUpdateRow() {
        updateFound?.let {
            UpdateInstall.startFrom(this, it)
            return
        }
        if (!UpdateCheck.enabled || checking) return
        checking = true
        updateNote = getString(R.string.phone_update_checking)
        render()
        val app = applicationContext
        CoreClient.background {
            var found: UpdateCheck.Update? = null
            val note = try {
                found = UpdateCheck.check(app)
                found?.let { getString(R.string.phone_update_available, it.version) } ?: getString(R.string.phone_update_current)
            } catch (e: Exception) {
                Log.w(TAG, "could not check for updates", e)
                // The feed's own failures are not IOExceptions (UpdateCheck).
                getString(if (e is IOException) R.string.phone_update_offline else R.string.phone_update_failed)
            }
            main.post {
                checking = false
                updateNote = note
                // Offered even when the task screen's card was put off: the person asked.
                updateFound = found
                render()
            }
        }
    }

    // Helpers.

    private fun addGroup(col: LinearLayout, title: Int, vararg rows: View) {
        col.addView(look.section(getString(title)), look.matchWrap())
        val g = look.group()
        for (r in rows) look.addRow(g, r)
        col.addView(g, look.matchWrap())
    }

    private fun toast(s: String) {
        Toast.makeText(applicationContext, s, Toast.LENGTH_SHORT).show()
    }

    // The dialogs, as the screen keeps the one open when it is made anew.
    private enum class Open { SERVICE, KEY_MENU, KEY, MODEL, URL, STEPS, TIME, COST }

    companion object {
        private const val TAG = "PinkyScreens"
        private const val STATE_OPEN = "open_dialog"
        private const val STATE_TEXT = "open_dialog_text"
        private const val USAGE_DAYS = 7 // how many days Usage lists one by one (ui/ask.go, usageDays)

        // policy.Preset's values, as desktop Pinky lists them.
        private val PRESETS = listOf(
            Triple(1, R.string.phone_preset_commits, R.string.phone_preset_commits_desc), // PresetCommits
            Triple(2, R.string.phone_preset_payments, R.string.phone_preset_payments_desc), // PresetPayments
            Triple(3, R.string.phone_preset_every, R.string.phone_preset_every_desc), // PresetEvery
        )
    }
}

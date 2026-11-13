package app.pinky.companion

import android.app.Activity
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.text.Editable
import android.text.InputType
import android.text.TextUtils
import android.text.TextWatcher
import android.util.Log
import android.util.TypedValue
import android.view.Gravity
import android.view.KeyEvent
import android.view.View
import android.view.ViewGroup
import android.view.inputmethod.EditorInfo
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONArray
import org.json.JSONObject
import java.io.Closeable

/**
 * Pinky's own screen on the phone, in a process of its own (":ui"): the
 * person asks Pinky for a task, follows it and stops it, as in desktop
 * Pinky's Ask Pinky (ui/ask.go), and finds the tasks that ended under
 * Activity (PastTasks), and schedules tasks (SchedulesActivity). Approvals and handoffs are answered on the card
 * over other apps (Overlays); the model, its key and a task's limits are
 * in SettingsActivity.
 *
 * The agent cannot use this screen: the gateway refuses every action
 * while it is in front and hides what it shows (policy.IsPinkyScreen),
 * OwnUI refuses gestures on it, and its content is sensitive (Look). Only
 * the window's root stays visible to the service, which needs it to see
 * that Pinky is in front.
 */
class TaskActivity : Activity() {

    private val main = Handler(Looper.getMainLooper())
    private lateinit var look: Look
    private lateinit var settings: TaskSettings
    private lateinit var secrets: Secrets
    private var sub: Closeable? = null

    private var state: JSONObject? = null // the core's latest; null while it cannot be reached
    private var down = "" // why the core cannot be reached, as logged
    private var notReadySince = 0L // uptime since when Pinky is on but not ready, or 0
    private var serviceOn = false
    private var hasKey = false
    private var unrestricted = true // Android lets Pinky use the battery without limits (Battery)
    private var starting = false
    private var checkingUpdate = false
    private var update: UpdateCheck.Update? = null

    private lateinit var updateCard: View
    private lateinit var updateTitle: TextView
    private lateinit var updateNotes: TextView
    private lateinit var noCoreCard: View
    private var hasCore = true
    private lateinit var coreCard: View
    private lateinit var coreTitle: TextView
    private lateinit var coreDetail: TextView
    private lateinit var setupCard: View
    private lateinit var serviceStep: Look.SetupStep
    private lateinit var modelStep: Look.SetupStep
    private lateinit var setupButton: TextView
    private lateinit var batteryCard: View
    private lateinit var batteryBody: TextView
    private lateinit var batteryAllow: TextView
    private lateinit var computerCard: View
    private lateinit var computerTask: TextView
    private lateinit var computerStop: TextView
    private lateinit var runCard: View
    private lateinit var runTask: TextView
    private lateinit var runUsage: TextView
    private lateinit var runWaiting: TextView
    private lateinit var runHint: TextView
    private lateinit var stopButton: TextView
    private lateinit var endCard: EndCard
    // The person's messages to the running task (steer), and going on with
    // the task that ended (continue): Steering.
    private lateinit var steerBox: View
    private lateinit var steerPending: LinearLayout
    private lateinit var steerInput: EditText
    private lateinit var steerSend: TextView
    private lateinit var steerError: TextView
    private lateinit var continueBox: View
    private lateinit var continueUnread: TextView
    private lateinit var continuePending: LinearLayout
    private lateinit var continueInput: EditText
    private lateinit var continueButton: TextView
    private lateinit var continueError: TextView
    private var sending = false // a steer on its way
    private var continuing = false // a continue on its way
    private var pendingShown: List<Steering.Message>? = null // what steerPending lists
    private var unreadShown: List<Steering.Message>? = null // what continuePending lists
    private lateinit var suggestions: Suggestions
    private lateinit var askBox: View
    private lateinit var input: EditText
    private lateinit var modelLine: TextView
    private lateinit var limitLine: TextView
    private lateinit var todayLine: TextView
    private var usedToday: String? = null // what the model used today (ModelUsage.today), as last read
    private lateinit var startButton: TextView
    private lateinit var askFooter: TextView
    private lateinit var error: TextView
    private lateinit var stepsHeading: TextView
    private lateinit var steps: StepList
    private lateinit var historyHeading: TextView
    private lateinit var historyGroup: LinearLayout
    private var historyShown = 0 // the tasks listed under Activity
    private var historyFor: String? = null // the task above the list when it was read ("" for none); null reads it again
    private var historyReads = 0 // counts reads, so that only the latest is shown

    private val rerender = Runnable { render() }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        look = Look(this)
        settings = TaskSettings(this)
        secrets = Secrets(this)
        hasCore = Core.available(this)
        val col = look.column()
        val header = look.header(getString(R.string.app_name), mark = true)
        header.addView(
            look.iconButton(R.drawable.ic_activity, getString(R.string.phone_activity)) {
                startActivity(Intent(this, HistoryActivity::class.java))
            },
        )
        header.addView(
            look.iconButton(R.drawable.ic_alarm, getString(R.string.phone_schedules)) {
                startActivity(Intent(this, SchedulesActivity::class.java))
            },
        )
        header.addView(look.iconButton(R.drawable.ic_settings, getString(R.string.phone_settings)) { openSettings() })
        col.addView(header, look.matchWrap())
        col.addView(noCoreCard(), look.below(12))
        col.addView(updateCard(), look.below(12))
        col.addView(setupCard(), look.below(12))
        col.addView(coreCard(), look.below(12))
        col.addView(batteryCard(), look.below(12))
        col.addView(computerCard(), look.below(12))
        col.addView(runCard(), look.below(12))
        col.addView(steerBox(), look.below(12))
        endCard = EndCard(this, look) { dismiss() }
        suggestions = Suggestions(this, look) { i, keep -> decideNote(i, keep) }
        endCard.view.addView(suggestions.view, look.matchWrap())
        col.addView(endCard.view, look.below(12))
        col.addView(continueBox(), look.below(12))
        col.addView(askBox(), look.below(12))
        error = look.text("", Look.SMALL, look.red).apply { setPadding(look.dp(4), look.dp(8), look.dp(4), 0) }
        error.say("")
        col.addView(error, look.matchWrap())
        stepsHeading = look.section(getString(R.string.phone_task_steps))
        col.addView(stepsHeading, look.matchWrap())
        steps = StepList(this, look)
        col.addView(steps.group, look.matchWrap())
        historyHeading = look.section(getString(R.string.phone_activity))
        col.addView(historyHeading, look.matchWrap())
        historyGroup = look.group()
        col.addView(historyGroup, look.matchWrap())
        look.setScreen(col)
    }

    override fun onStart() {
        super.onStart()
        // Without the core there is nothing to reach, and on-phone mode stays off.
        if (!hasCore) return
        sub = CoreClient.subscribe(this, { st -> main.post { onState(st) } }, { why -> main.post { onDown(why) } })
    }

    override fun onResume() {
        super.onResume()
        serviceOn = look.serviceOn()
        hasKey = settings.keyOptional() || secrets.has(settings.keyName())
        // The person may come back from Android's battery settings.
        unrestricted = Battery.unrestricted(this)
        update = UpdateCheck.offered(this)
        if (UpdateCheck.due(this)) checkUpdate()
        historyFor = null // Activity is read again.
        render()
    }

    override fun onStop() {
        sub?.close()
        sub = null
        super.onStop()
    }

    override fun onDestroy() {
        main.removeCallbacksAndMessages(null)
        super.onDestroy()
    }

    // The core.

    private fun onState(st: JSONObject) {
        state = st
        down = ""
        // A service turned off leaves the core running, without its phone.
        if (st.optJSONObject("device")?.optBoolean("ready") != true) serviceOn = look.serviceOn()
        render()
    }

    private fun onDown(why: String) {
        // The core is tried again every second: each reason is logged once.
        if (why != down) Log.w(TAG, "the core cannot be reached: $why")
        state = null
        down = why
        serviceOn = look.serviceOn()
        render()
    }

    // The screen: at most one of what Pinky needs first (the service, the
    // core, a key and a model), then Pinky on a computer while it uses the
    // phone, the task that runs or the one that ended, then the box for a
    // new one.

    private fun render() {
        main.removeCallbacks(rerender)
        noCoreCard.shown(!hasCore)
        if (!hasCore) {
            // A phone the core does not run on: Pinky works there with a
            // computer, and this screen says only that.
            for (v in listOf(
                updateCard, coreCard, setupCard, batteryCard, computerCard, runCard, steerBox, endCard.view,
                continueBox, askBox, error, stepsHeading, steps.group, historyHeading, historyGroup,
            )) v.shown(false)
            return
        }
        val st = state
        val device = st?.optJSONObject("device")
        val ready = serviceOn && device?.optBoolean("ready") == true
        val now = SystemClock.uptimeMillis()
        if (ready || !serviceOn) {
            notReadySince = 0
        } else if (notReadySince == 0L) {
            notReadySince = now
        }
        // The core takes a moment to start: say it failed only after that.
        val startingCore = !ready && serviceOn && now - notReadySince < CORE_GRACE_MS
        if (startingCore) main.postDelayed(rerender, CORE_GRACE_MS - (now - notReadySince))
        val agent = st?.optJSONObject("agent")
        val running = agent?.optBoolean("running") == true
        val setUp = hasKey && settings.model().isNotEmpty()

        val u = update
        updateCard.shown(u != null)
        if (u != null) {
            updateTitle.say(getString(R.string.phone_update_available, u.version))
            updateNotes.say(u.notes)
        }

        // Setup, one step at a time: the service, then the model and its
        // key. The core's own start is not a step: it says so on its card.
        setupCard.shown(!serviceOn || (!setUp && !running))
        serviceStep.state(done = serviceOn, now = !serviceOn)
        modelStep.state(done = setUp, now = serviceOn && !setUp)
        setupButton.text = getString(if (hasKey) R.string.phone_setup_model else R.string.phone_setup_key)
        coreCard.shown(serviceOn && !ready)
        if (serviceOn && !ready) {
            coreTitle.say(getString(if (startingCore) R.string.phone_core_starting else R.string.phone_core_failed))
            coreDetail.say(if (startingCore) "" else whyNotReady(device))
        }
        // Once Pinky is set up, what keeps it running through a long task,
        // until the person allows it or puts it off. Whether a vendor's
        // autostart is on cannot be read, so that card stays until put off.
        batteryCard.shown(
            ready && setUp && !running && !settings.batteryPutOff && (!unrestricted || Battery.hasAutostart()),
        )
        batteryBody.shown(!unrestricted)
        batteryAllow.shown(!unrestricted)

        // Pinky on a computer drives the phone (its claim): a task here
        // waits until it is done.
        val computer = st?.optJSONObject("computer")?.takeIf { !running }
        computerCard.shown(ready && computer != null)
        if (computer != null) {
            computerTask.say(computer.optString("task"))
            computerStop.shown(computer.optBoolean("stoppable"))
        } else {
            look.enable(computerStop, true)
        }

        runCard.shown(running)
        if (agent != null && running) {
            runTask.say(agent.optString("task"))
            runUsage.say(Words.usage(resources, agent))
            val need = st?.optJSONArray("needs")?.optJSONObject(0)
            runWaiting.say(need?.let { getString(R.string.phone_waiting_for, Words.shown(it, "title")) })
            runHint.shown(need != null)
        } else {
            look.enable(stopButton, true)
        }

        // While it runs, the person's messages that wait for the model, and
        // a field for another.
        val pending = Steering.pending(agent)
        steerBox.shown(running)
        if (running) {
            showPending(steerPending, pending, editable = true)
        } else {
            steerError.say("")
        }
        updateSend()

        endCard.view.shown(agent != null && !running)
        if (agent != null && !running) {
            endCard.show(agent)
            suggestions.show(agent)
        }

        // Once it ended, it may go on: with the messages it did not read,
        // and a new one.
        val continuable = ready && setUp && Steering.continuable(agent)
        continueBox.shown(continuable)
        if (continuable) {
            continueUnread.shown(pending.isNotEmpty())
            continuePending.shown(pending.isNotEmpty())
            showPending(continuePending, pending, editable = false)
        } else {
            continueError.say("")
        }
        updateContinue()

        askBox.shown(ready && setUp && !running)
        askFooter.shown(ready && setUp && !running && agent == null)
        modelLine.say(settings.modelLabel())
        todayLine.say(usedToday)
        todayLine.shown(usedToday != null)
        limitLine.say(
            getString(R.string.phone_task_limit, Words.limit(resources, settings.priced(), settings.maxCost, settings.tokenLimit)),
        )
        updateStart()
        val task = st?.optJSONObject("task")
        val taskSteps = task?.optJSONArray("steps") ?: JSONArray()
        stepsHeading.shown(taskSteps.length() > 0)
        steps.group.shown(taskSteps.length() > 0)
        steps.show(taskSteps, task?.optString("state").orEmpty())
        renderHistory(st, running)
    }

    private fun whyNotReady(device: JSONObject?): String {
        // Why the core cannot be reached is in the log (onDown), in a
        // developer's words; the person gets what to do.
        if (state == null) return getString(R.string.phone_core_failed_hint)
        return listOf(device?.let { Words.shown(it, "line") }.orEmpty(), device?.optString("detail").orEmpty())
            .filter { it.isNotEmpty() }.distinct().joinToString("\n")
    }

    private fun updateStart() {
        // Start waits while Pinky on a computer uses the phone.
        val computer = state?.optJSONObject("computer") != null
        look.enable(startButton, input.text.isNotBlank() && !starting && !computer)
    }

    // A task.

    private fun startTask() {
        val task = input.text.toString().trim()
        if (task.isEmpty() || starting) return
        starting = true
        error.say("")
        updateStart()
        val id = settings.service
        val name = settings.keyName(id)
        val optional = settings.keyOptional(id)
        CoreClient.background {
            val why = try {
                val key = secrets.load(name)
                when {
                    key != null || optional && !secrets.has(name) -> {
                        CoreClient.call(settings.startRequest(task, key.orEmpty(), id))
                        null
                    }
                    secrets.has(name) -> getString(R.string.phone_err_key_lost)
                    else -> getString(R.string.phone_no_key)
                }
            } catch (e: CoreClient.Refused) {
                startError(e)
            } catch (e: Exception) {
                Log.w(TAG, "could not start the task", e)
                getString(R.string.phone_err_start)
            }
            main.post { started(why) }
        }
    }

    private fun started(why: String?) {
        starting = false
        if (why == null) {
            input.setText("")
            // The agent starts from the app the person was in; the Working
            // pill follows the task there.
            moveTaskToBack(true)
        } else {
            error.say(why)
        }
        render()
    }

    private fun listen() {
        try {
            @Suppress("DEPRECATION")
            startActivityForResult(QuickAccess.voiceIntent(), REQ_VOICE)
        } catch (e: Exception) {
            Log.w(TAG, "no speech recognizer", e)
            error.say(getString(R.string.phone_voice_none))
        }
    }

    @Deprecated("Activity's own; Pinky's screens need no AndroidX")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        @Suppress("DEPRECATION")
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != REQ_VOICE || resultCode != RESULT_OK) return
        val heard = QuickAccess.heard(data)
        if (heard.isEmpty()) return
        val before = input.text.toString().trimEnd()
        input.setText(if (before.isEmpty()) heard else "$before $heard")
        input.setSelection(input.text.length)
    }

    private fun startError(e: CoreClient.Refused): String = when (e.code) {
        "no_device" -> getString(R.string.phone_err_no_device)
        "no_key" -> getString(R.string.phone_no_key)
        "no_model" -> getString(R.string.phone_err_no_model)
        "busy" -> getString(R.string.phone_err_busy)
        "computer" -> getString(R.string.phone_err_computer)
        "no_task" -> "" // Start waits for a task; nothing to say.
        else -> {
            Log.w(TAG, "the core refused the task [${e.code}]: ${e.message}")
            getString(R.string.phone_err_start)
        }
    }

    // The person's messages to Pinky (Steering).

    private fun updateSend() {
        look.enable(steerSend, Steering.canSend(steerInput.text, sending))
    }

    private fun updateContinue() {
        val computer = state?.optJSONObject("computer") != null
        val pending = Steering.pending(state?.optJSONObject("agent")).size
        look.enable(continueButton, Steering.canContinue(continueInput.text, pending, continuing, computer))
    }

    /** Sends the field's message to the running task; the model reads it at its next step. */
    private fun sendSteer() {
        val text = steerInput.text.toString().trim()
        if (!Steering.canSend(text, sending)) return
        sending = true
        steerError.say("")
        updateSend()
        CoreClient.background {
            val failed = try {
                CoreClient.call(CoreClient.cmd("steer", "text" to text))
                null
            } catch (e: Exception) {
                e
            }
            main.post { sent(text, failed) }
        }
    }

    private fun sent(text: String, failed: Exception?) {
        sending = false
        when {
            failed == null -> steerInput.setText("")
            failed is CoreClient.Refused && Steering.refusal(failed.code) == Steering.Refusal.ENDED -> {
                // The task ended meanwhile: the words go on with it instead.
                steerInput.setText("")
                val moved = Steering.joined(continueInput.text.toString(), text)
                continueInput.setText(moved)
                continueInput.setSelection(moved.length)
            }
            else -> steerError.say(messageError(failed))
        }
        render()
    }

    /** Changes a message the model has not read; empty text withdraws it. */
    private fun editMessage(m: Steering.Message) {
        look.edit(
            getString(R.string.phone_steer_edit), "", m.text, getString(R.string.phone_steer_hint), Look.Field.TEXT,
        ) { text ->
            messageCommand(CoreClient.cmd("steer", "id" to m.id, "text" to text.trim()))
        }
    }

    private fun withdrawMessage(m: Steering.Message) {
        messageCommand(CoreClient.cmd("withdraw", "id" to m.id))
    }

    private fun messageCommand(req: JSONObject) {
        steerError.say("")
        CoreClient.send(req) { err ->
            if (err != null) main.post { steerError.say(messageError(err)) }
        }
    }

    /** What the screen says when steer, an edit or withdraw failed; "" for nothing. */
    private fun messageError(e: Exception): String {
        if (e !is CoreClient.Refused) {
            Log.w(TAG, "could not send the message", e)
            return getString(R.string.phone_steer_failed, e.message ?: e.toString())
        }
        return when (Steering.refusal(e.code)) {
            Steering.Refusal.READ -> getString(R.string.phone_steer_read)
            // Withdrawn already, nothing typed, or the task ended: the state
            // shows what became of it.
            Steering.Refusal.GONE, Steering.Refusal.EMPTY, Steering.Refusal.ENDED -> ""
            Steering.Refusal.FAILED -> {
                Log.w(TAG, "the core refused the message [${e.code}]: ${e.message}")
                getString(R.string.phone_steer_failed, e.message.orEmpty())
            }
        }
    }

    /**
     * Goes on with the task that ended, with the field's message and those
     * it did not read. Like Start, it hands the core the key again.
     */
    private fun continueTask() {
        val text = continueInput.text.toString().trim()
        val computer = state?.optJSONObject("computer") != null
        val pending = Steering.pending(state?.optJSONObject("agent")).size
        if (!Steering.canContinue(text, pending, continuing, computer)) return
        continuing = true
        continueError.say("")
        updateContinue()
        // The key of the service the task runs on, which Settings may have
        // changed since: the name it is saved under, or, from a core of
        // before, the API's, the name keys had then. A local server needs
        // none.
        val agent = state?.optJSONObject("agent")
        val keyName = agent?.optString("key_name").orEmpty()
            .ifEmpty { agent?.optString("provider").orEmpty() }
            .ifEmpty { settings.keyName() }
        val optional = agent?.optBoolean("key_optional") == true
        CoreClient.background {
            val why = try {
                val key = secrets.load(keyName)
                when {
                    key != null || optional && !secrets.has(keyName) -> {
                        CoreClient.call(CoreClient.cmd("continue", "text" to text, "key" to key.orEmpty()))
                        null
                    }
                    secrets.has(keyName) -> getString(R.string.phone_err_key_lost)
                    else -> getString(R.string.phone_no_key)
                }
            } catch (e: CoreClient.Refused) {
                continueRefused(e)
            } catch (e: Exception) {
                Log.w(TAG, "could not continue the task", e)
                getString(R.string.phone_steer_failed, e.message ?: e.toString())
            }
            main.post { continued(why) }
        }
    }

    private fun continued(why: String?) {
        continuing = false
        if (why == null) {
            continueInput.setText("")
            // As after Start: the agent goes on in the app the person was
            // in, and the Working pill follows it there.
            moveTaskToBack(true)
        } else {
            continueError.say(why)
        }
        render()
    }

    private fun continueRefused(e: CoreClient.Refused): String = when (e.code) {
        // As Start says them.
        "no_device", "no_key", "no_model", "busy", "computer" -> startError(e)
        "empty" -> "" // Continue waits for a message; nothing to say.
        else -> {
            Log.w(TAG, "the core refused to continue [${e.code}]: ${e.message}")
            getString(R.string.phone_steer_failed, e.message.orEmpty())
        }
    }

    /**
     * Lists messages in group, one row each with its text, and while the
     * task runs the note that the model has not read it yet and Edit and
     * Withdraw. The rows are made again only when the messages changed.
     */
    private fun showPending(group: LinearLayout, messages: List<Steering.Message>, editable: Boolean) {
        val shown = if (editable) pendingShown else unreadShown
        if (shown == messages) return
        if (editable) pendingShown = messages else unreadShown = messages
        group.removeAllViews()
        group.shown(messages.isNotEmpty())
        for (m in messages) look.addRow(group, pendingRow(m, editable))
    }

    private fun pendingRow(m: Steering.Message, editable: Boolean): View {
        val v = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(look.dp(16), look.dp(12), look.dp(16), if (editable) look.dp(4) else look.dp(12))
        }
        v.addView(look.text(m.text, Look.BODY, look.ink), look.matchWrap())
        if (editable) {
            v.addView(
                look.text(getString(R.string.phone_steer_pending), Look.SMALL, look.ink3).apply { setPadding(0, look.dp(2), 0, 0) },
                look.matchWrap(),
            )
            v.addView(
                look.buttons(
                    look.button(getString(R.string.phone_steer_edit), Look.Kind.PLAIN) { editMessage(m) },
                    look.button(getString(R.string.phone_steer_withdraw), Look.Kind.PLAIN) { withdrawMessage(m) },
                ).apply { setPadding(0, look.dp(2), 0, 0) },
                look.matchWrap(),
            )
        }
        return v
    }

    private fun stop() {
        look.enable(stopButton, false)
        CoreClient.send(CoreClient.cmd("stop")) { err -> if (err != null) main.post { look.enable(stopButton, true) } }
    }

    // Stop on the computer's card ends Pinky on a computer's claim, as Stop
    // on the pill does; desktop Pinky stops its agent when it hears.
    private fun stopComputer() {
        look.enable(computerStop, false)
        CoreClient.send(CoreClient.cmd("stop", "computer" to true)) { err ->
            if (err != null) main.post { look.enable(computerStop, true) }
        }
    }

    private fun dismiss() {
        error.say("")
        CoreClient.send(CoreClient.cmd("dismiss"))
    }

    // Keep or Drop on a note the task suggested; the state shows the answer.
    private fun decideNote(i: Int, keep: Boolean) {
        CoreClient.send(CoreClient.cmd(if (keep) "keep" else "drop", "index" to i)) { err ->
            if (err != null) main.post { if (!isDestroyed) suggestions.failed((err as? CoreClient.Refused)?.code ?: "") }
        }
    }

    private fun openSettings() {
        startActivity(Intent(this, SettingsActivity::class.java))
    }

    // Updates, for builds that do not come with desktop Pinky's.

    private fun checkUpdate() {
        if (checkingUpdate) return
        checkingUpdate = true
        val app = applicationContext
        CoreClient.background {
            try {
                UpdateCheck.check(app)
            } catch (_: Exception) {
                // Offline, or no feed: the next visit tries again.
            }
            main.post {
                checkingUpdate = false
                update = UpdateCheck.offered(app)
                render()
            }
        }
    }

    private fun putOffUpdate() {
        update?.let { UpdateCheck.putOff(this, it) }
        update = null
        render()
    }

    private fun downloadUpdate() {
        val u = update ?: return
        UpdateInstall.startFrom(this, u)
    }

    // Activity: the latest tasks that ended, without the one the screen
    // shows above. It is read again when the screen comes back and when
    // that task changes (one ended, or was dismissed), and hidden while a
    // task runs and while the core cannot be reached. Show all opens the
    // whole history.

    private fun renderHistory(st: JSONObject?, running: Boolean) {
        val shown = if (st == null || running) null else st.optJSONObject("task")?.optString("id").orEmpty()
        if (shown != null && shown != historyFor) readHistory(shown)
        val on = shown != null && historyShown > 0
        historyHeading.shown(on)
        historyGroup.shown(on)
    }

    private fun readHistory(shown: String) {
        historyFor = shown
        val n = ++historyReads
        CoreClient.background {
            // What the model used today changes as tasks end, as the list does.
            val today = try {
                ModelUsage.today(resources, ModelUsage.read())
            } catch (_: Exception) {
                usedToday
            }
            main.post {
                if (!isDestroyed && today != usedToday) {
                    usedToday = today
                    render()
                }
            }
            // A read that fails keeps the list as it was, until the next.
            val page = try {
                PastTasks.page(this, 0, RECENT + 1)
            } catch (_: Exception) {
                null
            }
            main.post { if (page != null && n == historyReads && !isDestroyed) showHistory(page, shown) }
        }
    }

    private fun showHistory(page: PastTasks.Page, shown: String) {
        val tasks = page.tasks.filter { it.optString("id") != shown }
        historyGroup.removeAllViews()
        for (t in tasks.take(RECENT)) {
            val id = t.optString("id")
            look.addRow(historyGroup, PastTasks.row(this, look, t, withDay = true) { PastTasks.open(this, id) }.view)
        }
        if (page.more || tasks.size > RECENT) {
            val all = look.row(getString(R.string.phone_history_all)) { startActivity(Intent(this, HistoryActivity::class.java)) }
            all.title.setTextColor(look.accent)
            look.addRow(historyGroup, all.view)
        }
        historyShown = minOf(tasks.size, RECENT)
        render()
    }

    // The cards.

    private fun updateCard(): View {
        updateTitle = look.text("", Look.BODY, look.ink, 600)
        updateNotes = look.text("", Look.SMALL, look.ink2).apply {
            maxLines = 4
            ellipsize = TextUtils.TruncateAt.END
            setPadding(0, look.dp(4), 0, 0)
        }
        val c = look.card(look.accentTint)
        c.addView(updateTitle)
        c.addView(updateNotes)
        c.addView(
            look.buttons(
                look.button(getString(R.string.phone_update_later), Look.Kind.PLAIN) { putOffUpdate() },
                look.button(getString(R.string.phone_update_download), Look.Kind.PRIMARY) { downloadUpdate() },
            ),
            look.matchWrap(),
        )
        updateCard = c
        return c
    }

    private fun noCoreCard(): View {
        val c = look.card()
        c.addView(look.text(getString(R.string.phone_no_core_title), Look.HEADLINE, look.ink, 600))
        c.addView(look.text(getString(R.string.phone_no_core_body), Look.BODY, look.ink2).apply { setPadding(0, look.dp(6), 0, 0) })
        noCoreCard = c
        return c
    }

    // Setting Pinky up: the service, then the model and its key.
    private fun setupCard(): View {
        val c = look.card()
        c.addView(look.text(getString(R.string.phone_setup_title), Look.HEADLINE, look.ink, 600))
        c.addView(look.text(getString(R.string.phone_setup), Look.SMALL, look.ink2).apply { setPadding(0, look.dp(4), 0, look.dp(2)) })

        serviceStep = look.SetupStep(1, getString(R.string.phone_setup_service))
        serviceStep.body.addView(look.text(getString(R.string.phone_off_body), Look.SMALL, look.ink2), look.matchWrap())
        serviceStep.body.addView(
            look.buttons(look.button(getString(R.string.phone_open_accessibility), Look.Kind.PRIMARY) { look.openAccessibility() })
                .apply { gravity = Gravity.START },
            look.matchWrap(),
        )
        // From Android 13 the setting is restricted for apps installed
        // outside an app store until the person allows it in App info.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            serviceStep.body.addView(
                look.text(getString(R.string.phone_restricted), Look.SMALL, look.ink3).apply { setPadding(0, look.dp(12), 0, 0) },
                look.matchWrap(),
            )
            serviceStep.body.addView(
                look.buttons(look.button(getString(R.string.phone_app_info)) { look.openAppInfo() }).apply {
                    gravity = Gravity.START
                    setPadding(0, look.dp(8), 0, 0)
                },
                look.matchWrap(),
            )
        }
        c.addView(serviceStep.view, look.matchWrap())

        modelStep = look.SetupStep(2, getString(R.string.phone_setup_model_step))
        modelStep.body.addView(look.text(getString(R.string.phone_setup_model_body), Look.SMALL, look.ink2), look.matchWrap())
        setupButton = look.button("", Look.Kind.PRIMARY) { openSettings() }
        modelStep.body.addView(look.buttons(setupButton).apply { gravity = Gravity.START }, look.matchWrap())
        c.addView(modelStep.view, look.matchWrap())
        setupCard = c
        return c
    }

    private fun coreCard(): View {
        coreTitle = look.text("", Look.BODY, look.ink, 600)
        coreDetail = look.text("", Look.SMALL, look.ink3).apply { setPadding(0, look.dp(6), 0, 0) }
        coreDetail.say("")
        val c = look.card()
        c.addView(coreTitle)
        c.addView(coreDetail)
        coreCard = c
        return c
    }

    private fun batteryCard(): View {
        val c = look.card()
        c.addView(look.text(getString(R.string.phone_battery_title), Look.BODY, look.ink, 600))
        batteryBody = look.text(getString(R.string.phone_battery_body), Look.SMALL, look.ink2).apply {
            setPadding(0, look.dp(6), 0, 0)
        }
        c.addView(batteryBody, look.matchWrap())
        val buttons = mutableListOf<View>(
            look.button(getString(R.string.phone_update_later), Look.Kind.PLAIN) {
                settings.batteryPutOff = true
                render()
            },
        )
        if (Battery.hasAutostart()) {
            c.addView(
                look.text(getString(R.string.phone_autostart_body), Look.SMALL, look.ink2).apply { setPadding(0, look.dp(6), 0, 0) },
                look.matchWrap(),
            )
            buttons += look.button(getString(R.string.phone_autostart_open)) { Battery.openAutostart(this) }
        }
        batteryAllow = look.button(getString(R.string.phone_battery_allow), Look.Kind.PRIMARY) { Battery.askUnrestricted(this) }
        buttons += batteryAllow
        c.addView(look.buttons(*buttons.toTypedArray()), look.matchWrap())
        batteryCard = c
        return c
    }

    private fun computerCard(): View {
        val top = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        top.addView(look.text("●", Look.SMALL, look.accent).apply { setPadding(0, 0, look.dp(8), 0) })
        top.addView(
            look.text(getString(R.string.phone_computer_title), Look.BODY, look.ink, 600),
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f),
        )
        computerStop = look.button(getString(R.string.phone_stop), Look.Kind.DANGER) { stopComputer() }
        top.addView(computerStop)
        computerTask = look.text("", Look.BODY, look.ink2).apply { setPadding(0, look.dp(8), 0, 0) }
        computerTask.say("")
        val detail = look.text(getString(R.string.phone_computer_detail), Look.SMALL, look.ink3).apply {
            setPadding(0, look.dp(6), 0, 0)
        }
        val c = look.card(look.accentTint)
        c.addView(top, look.matchWrap())
        c.addView(computerTask, look.matchWrap())
        c.addView(detail, look.matchWrap())
        computerCard = c
        return c
    }

    private fun runCard(): View {
        val top = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        top.addView(look.text("●", Look.SMALL, look.green).apply { setPadding(0, 0, look.dp(8), 0) })
        top.addView(
            look.text(getString(R.string.phone_task_working), Look.BODY, look.ink, 600),
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f),
        )
        stopButton = look.button(getString(R.string.phone_stop), Look.Kind.DANGER) { stop() }
        top.addView(stopButton)
        runTask = look.text("", Look.BODY, look.ink).apply { setPadding(0, look.dp(8), 0, 0) }
        runUsage = look.text("", Look.SMALL, look.ink2).apply { setPadding(0, look.dp(4), 0, 0) }
        runWaiting = look.text("", Look.BODY, look.amber, 600).apply { setPadding(0, look.dp(10), 0, 0) }
        runHint = look.text(getString(R.string.phone_waiting_hint), Look.SMALL, look.ink3).apply { setPadding(0, look.dp(2), 0, 0) }
        val c = look.card(look.accentTint)
        c.addView(top, look.matchWrap())
        for (v in listOf(runTask, runUsage, runWaiting, runHint)) c.addView(v, look.matchWrap())
        runCard = c
        return c
    }

    // While a task runs: the messages the model has not read, then a line
    // for another and Send.
    private fun steerBox(): View {
        steerPending = look.group()
        steerPending.shown(false)
        steerInput = messageField(
            R.id.steer_input, R.string.phone_steer_label, R.string.phone_steer_hint, EditorInfo.IME_ACTION_SEND,
            { sendSteer() },
        ) {
            steerError.say("")
            updateSend()
        }
        steerSend = look.button(getString(R.string.phone_steer_send), Look.Kind.PRIMARY) { sendSteer() }
        steerError = look.text("", Look.SMALL, look.red).apply { setPadding(look.dp(4), look.dp(8), look.dp(4), 0) }
        steerError.say("")
        val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        box.addView(steerPending, look.matchWrap())
        box.addView(fieldRow(steerInput, steerSend), look.below(8))
        box.addView(steerError, look.matchWrap())
        steerBox = box
        return box
    }

    // Once a task ended and may go on: the messages it did not read, then a
    // line for the next and Continue. The task box below still starts a
    // new task.
    private fun continueBox(): View {
        continueUnread = look.text(getString(R.string.phone_continue_unread), Look.SMALL, look.ink2).apply {
            setPadding(look.dp(4), 0, look.dp(4), look.dp(8))
        }
        continuePending = look.group()
        continuePending.shown(false)
        continueInput = messageField(
            R.id.continue_input, R.string.phone_continue_label, R.string.phone_continue_hint, EditorInfo.IME_ACTION_GO,
            { continueTask() },
        ) {
            continueError.say("")
            updateContinue()
        }
        continueButton = look.button(getString(R.string.phone_continue), Look.Kind.PRIMARY) { continueTask() }
        continueError = look.text("", Look.SMALL, look.red).apply { setPadding(look.dp(4), look.dp(8), look.dp(4), 0) }
        continueError.say("")
        val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        box.addView(continueUnread, look.matchWrap())
        box.addView(continuePending, look.matchWrap())
        box.addView(fieldRow(continueInput, continueButton), look.below(8))
        box.addView(continueError, look.matchWrap())
        continueBox = box
        return box
    }

    /** A one-line field for a message to Pinky, which sends with the keyboard's action (and Enter) too. */
    private fun messageField(id: Int, label: Int, hint: Int, action: Int, onSend: () -> Unit, onChange: () -> Unit): EditText =
        EditText(this).apply {
            // Keeps what the person typed when the screen is made again.
            this.id = id
            contentDescription = getString(label)
            this.hint = getString(hint)
            setTextColor(look.ink)
            setHintTextColor(look.ink3)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, Look.BODY)
            isSingleLine = true
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_CAP_SENTENCES
            imeOptions = action
            background = look.rounded(look.groupBg, 14)
            setPadding(look.dp(14), look.dp(10), look.dp(14), look.dp(10))
            minHeight = look.dp(44)
            addTextChangedListener(object : TextWatcher {
                override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
                override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}
                override fun afterTextChanged(s: Editable?) = onChange()
            })
            setOnEditorActionListener { _, actionId, ev ->
                val enter = ev != null && ev.keyCode == KeyEvent.KEYCODE_ENTER
                if (actionId != action && !enter) return@setOnEditorActionListener false
                if (!enter || ev?.action == KeyEvent.ACTION_DOWN) onSend()
                true
            }
        }

    private fun fieldRow(field: EditText, button: View): View = LinearLayout(this).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
        addView(field, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        addView(
            button,
            LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT)
                .apply { marginStart = look.dp(8) },
        )
    }

    private fun askBox(): View {
        input = EditText(this).apply {
            // Keeps what the person typed when the screen is made again.
            id = R.id.task_input
            hint = getString(R.string.phone_task_hint)
            setTextColor(look.ink)
            setHintTextColor(look.ink3)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, Look.BODY)
            minLines = 3
            maxLines = 8
            gravity = Gravity.TOP or Gravity.START
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE or
                InputType.TYPE_TEXT_FLAG_CAP_SENTENCES
            background = look.rounded(look.groupBg, 14)
            setPadding(look.dp(14), look.dp(12), look.dp(14), look.dp(12))
            addTextChangedListener(object : TextWatcher {
                override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
                override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}
                override fun afterTextChanged(s: Editable?) = updateStart()
            })
            // Ctrl+Enter on a keyboard starts, as Cmd+Enter does on a computer.
            setOnKeyListener { _, code, ev ->
                if (code != KeyEvent.KEYCODE_ENTER || !ev.isCtrlPressed) return@setOnKeyListener false
                if (ev.action == KeyEvent.ACTION_DOWN) startTask()
                true
            }
        }
        modelLine = look.text("", Look.SMALL, look.ink2, 500)
        limitLine = look.text("", Look.SMALL, look.ink3)
        todayLine = look.text("", Look.SMALL, look.ink3)
        val texts = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(modelLine)
            addView(limitLine)
            addView(todayLine)
        }
        startButton = look.button(getString(R.string.phone_start), Look.Kind.PRIMARY) { startTask() }
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(look.dp(4), look.dp(10), 0, 0)
            addView(texts, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            // Voice input: Android's own speech recognizer.
            if (QuickAccess.voiceAvailable(this@TaskActivity)) {
                addView(look.iconButton(R.drawable.ic_mic, getString(R.string.phone_voice)) { listen() })
            }
            addView(startButton)
        }
        askFooter = look.note(getString(R.string.phone_task_footer))
        val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        box.addView(input, look.matchWrap())
        box.addView(row, look.matchWrap())
        box.addView(askFooter, look.matchWrap())
        askBox = box
        return box
    }

    companion object {
        private const val TAG = "PinkyScreens"
        private const val CORE_GRACE_MS = 3_000L
        private const val RECENT = 5 // the tasks Activity lists
        private const val REQ_VOICE = 1
    }
}

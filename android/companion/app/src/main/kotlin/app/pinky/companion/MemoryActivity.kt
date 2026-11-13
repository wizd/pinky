package app.pinky.companion

import android.app.Activity
import android.content.res.Resources
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.Gravity
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.Switch
import android.widget.TextView
import android.widget.Toast
import org.json.JSONArray
import org.json.JSONObject

/**
 * Pinky's memory on this phone, as desktop Pinky has it in Settings ›
 * Agents › Memory (ui/memory.go): whether tasks use it, and every note,
 * to add, change and delete. The notes are the core's (memory.json in its
 * data folder, internal/standalone/memory.go), never the computer's. It
 * opens from Settings, in its process, and is closed to agents as it is
 * (Look).
 */
class MemoryActivity : Activity() {

    private val main = Handler(Looper.getMainLooper())
    private lateinit var look: Look
    private lateinit var settings: TaskSettings
    private lateinit var useSwitch: Switch
    private lateinit var list: LinearLayout
    private lateinit var note: TextView
    private lateinit var addButton: TextView
    private lateinit var deleteAll: TextView

    private var notes = JSONArray()
    private var failed = false
    private var reads = 0 // counts commands, so that only the latest reply is shown

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        look = Look(this)
        settings = TaskSettings(this)
        val col = look.column()
        col.addView(look.header(getString(R.string.phone_memory)), look.matchWrap())
        col.addView(look.text(getString(R.string.phone_memory_intro), Look.SMALL, look.ink2).apply {
            setPadding(look.dp(4), look.dp(4), look.dp(4), 0)
        }, look.matchWrap())

        // On or off: a switch at the row's right, which the whole row turns.
        val use = look.row(getString(R.string.phone_memory_use), desc = getString(R.string.phone_memory_use_desc)) {
            useSwitch.toggle()
        }
        useSwitch = Switch(this).apply {
            isChecked = settings.useMemory
            contentDescription = getString(R.string.phone_memory_use)
            setOnCheckedChangeListener { _, on -> settings.useMemory = on }
        }
        (use.title.parent as LinearLayout).addView(useSwitch)
        val g = look.group()
        look.addRow(g, use.view)
        col.addView(g, look.below(16))

        col.addView(look.section(getString(R.string.phone_memory_notes)), look.matchWrap())
        list = look.group()
        col.addView(list, look.matchWrap())
        note = look.note()
        note.say("")
        col.addView(note, look.matchWrap())
        addButton = look.button(getString(R.string.phone_memory_add)) { add() }
        deleteAll = look.button(getString(R.string.phone_memory_delete_all), Look.Kind.DANGER) { confirmDeleteAll() }
        col.addView(look.buttons(addButton, deleteAll).apply { gravity = Gravity.START }, look.matchWrap())
        look.setScreen(col)
        render()
        run(CoreClient.cmd("memories"))
    }

    override fun onRestart() {
        super.onRestart()
        run(CoreClient.cmd("memories"))
    }

    override fun onDestroy() {
        look.dismissDialog()
        main.removeCallbacksAndMessages(null)
        super.onDestroy()
    }

    // Sends a notes command; its reply holds the notes as they are now.
    // error gets why the core said no, for the dialog that sent it.
    private fun run(cmd: JSONObject, error: ((String) -> Unit)? = null) {
        val n = ++reads
        CoreClient.background {
            val r = runCatching { CoreClient.call(cmd) }
            main.post {
                if (isDestroyed) return@post
                r.onSuccess {
                    if (n == reads) {
                        failed = false
                        notes = it.optJSONArray("memories") ?: JSONArray()
                    }
                }.onFailure { e ->
                    Log.w(TAG, "memory: ${cmd.optString("cmd")}", e)
                    if (e is CoreClient.Refused && error != null) {
                        error(errorText(resources, e.code))
                    } else if (n == reads) {
                        failed = true
                    }
                }
                render()
            }
        }
    }

    private fun render() {
        list.removeAllViews()
        for (i in 0 until notes.length()) {
            val m = notes.optJSONObject(i) ?: continue
            val id = m.optString("id")
            val text = m.optString("text")
            val r = look.row(text, desc = source(resources, m.optString("kind"), m.optString("app"))) {
                look.menu(text, listOf(getString(R.string.phone_memory_edit), getString(R.string.phone_memory_delete))) { which ->
                    if (which == 0) edit(id, text) else run(CoreClient.cmd("memory_delete", "id" to id))
                }
            }
            r.title.setTextIsSelectable(false)
            look.addRow(list, r.view)
        }
        list.shown(notes.length() > 0)
        note.say(
            when {
                failed -> getString(R.string.phone_memory_read_failed)
                notes.length() == 0 -> getString(R.string.phone_memory_empty)
                else -> ""
            },
        )
        look.enable(addButton, !failed && notes.length() < MAX_NOTES)
        deleteAll.shown(notes.length() > 0)
    }

    private fun add(initial: String = "") {
        look.edit(getString(R.string.phone_memory_add), "", initial, getString(R.string.phone_memory_hint), Look.Field.TEXT, ::check) { v ->
            save(CoreClient.cmd("memory_add", "text" to v)) { why -> reopen(why) { add(v) } }
        }
    }

    private fun edit(id: String, text: String) {
        look.edit(getString(R.string.phone_memory_edit), "", text, getString(R.string.phone_memory_hint), Look.Field.TEXT, ::check) { v ->
            save(CoreClient.cmd("memory_edit", "id" to id, "text" to v)) { why -> reopen(why) { edit(id, v) } }
        }
    }

    // What the dialog can tell at once; the core checks the rest (secrets,
    // numbers and addresses) and says no with a code.
    private fun check(v: String): String? {
        val t = v.trim().replace(Regex("\\s+"), " ")
        return when {
            t.isEmpty() -> ""
            t.codePointCount(0, t.length) > MAX_CHARS -> errorText(resources, "memory_long")
            else -> null
        }
    }

    private fun save(cmd: JSONObject, refused: (String) -> Unit) {
        if (cmd.optString("text").isBlank()) return
        run(cmd, refused)
    }

    // Says why a note was not saved: Android's dialogs close on Save, so a
    // toast tells why and the dialog opens again.
    private fun reopen(why: String, again: () -> Unit) {
        Toast.makeText(applicationContext, why, Toast.LENGTH_LONG).show()
        again()
    }

    private fun confirmDeleteAll() {
        look.confirm(
            getString(R.string.phone_memory_delete_all_title),
            getString(R.string.phone_memory_delete_all_message),
            getString(R.string.phone_memory_delete_all),
            destructive = true,
        ) { run(CoreClient.cmd("memory_delete_all")) }
    }

    companion object {
        private const val TAG = "PinkyScreens"

        // pilot.MaxMemories and pilot.MaxMemoryChars.
        const val MAX_NOTES = 50
        const val MAX_CHARS = 200

        /** Where a note came from: the person, or a task in an app. */
        fun source(res: Resources, kind: String, app: String): String = when {
            kind != "learned" -> res.getString(R.string.phone_memory_from_you)
            app.isNotEmpty() -> res.getString(R.string.phone_memory_learned_in, app)
            else -> res.getString(R.string.phone_memory_learned)
        }

        /** Why the core kept no note, from its code (standalone's CodeMemory* constants). */
        fun errorText(res: Resources, code: String): String = when (code) {
            "memory_sensitive" -> res.getString(R.string.phone_memory_err_sensitive)
            "memory_long" -> res.getString(R.string.phone_memory_err_long, MAX_CHARS)
            "memory_full" -> res.getString(R.string.phone_memory_err_full, MAX_NOTES)
            else -> res.getString(R.string.phone_memory_err_failed)
        }
    }
}

/**
 * The end card's question after a task: whether to remember each note it
 * suggested (AgentState.suggested), as desktop Pinky asks under how a task
 * ended (ui/memory.go). onDecide sends keep or drop for an index, and
 * failed says why the core said no.
 */
class Suggestions(private val activity: Activity, private val look: Look, private val onDecide: (Int, Boolean) -> Unit) {

    val view: LinearLayout = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        setPadding(0, look.dp(12), 0, 0)
    }
    private val error = look.text("", Look.SMALL, look.red).apply { setPadding(0, look.dp(6), 0, 0) }
    private var shown = ""

    /** Says why a suggestion was not kept, until the suggestions change. */
    fun failed(code: String) {
        error.say(MemoryActivity.errorText(activity.resources, code))
    }

    /** Shows agent's suggestions, or nothing when it has none. */
    fun show(agent: JSONObject) {
        val list = agent.optJSONArray("suggested") ?: JSONArray()
        view.shown(list.length() > 0)
        val key = list.toString()
        if (key == shown) return
        shown = key
        view.removeAllViews()
        error.say("")
        if (list.length() == 0) return
        view.addView(look.text(activity.getString(R.string.phone_memory_ask), Look.SMALL, look.ink, 600), look.matchWrap())
        val res = activity.resources
        for (i in 0 until list.length()) {
            val s = list.optJSONObject(i) ?: continue
            val box = LinearLayout(activity).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(0, look.dp(8), 0, 0)
            }
            box.addView(look.text(s.optString("text"), Look.BODY, look.ink).apply { setTextIsSelectable(true) }, look.matchWrap())
            val row = LinearLayout(activity).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
            }
            row.addView(
                look.text(MemoryActivity.source(res, s.optString("kind"), s.optString("app")), Look.SMALL, look.ink3),
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f),
            )
            when {
                s.optBoolean("kept") -> row.addView(look.text(activity.getString(R.string.phone_memory_kept), Look.SMALL, look.green))
                s.optBoolean("dropped") -> row.addView(look.text(activity.getString(R.string.phone_memory_dropped), Look.SMALL, look.ink3))
                else -> {
                    val drop = look.button(activity.getString(R.string.phone_memory_drop), Look.Kind.PLAIN) { onDecide(i, false) }
                    val keep = look.button(activity.getString(R.string.phone_memory_keep), Look.Kind.PRIMARY) { onDecide(i, true) }
                    row.addView(look.buttons(drop, keep).apply { setPadding(0, 0, 0, 0) })
                }
            }
            box.addView(row, look.matchWrap())
            view.addView(box, look.matchWrap())
        }
        view.addView(error, look.matchWrap())
    }
}

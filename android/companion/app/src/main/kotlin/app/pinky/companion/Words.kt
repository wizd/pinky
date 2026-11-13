package app.pinky.companion

import android.content.Context
import android.content.res.Resources
import android.text.format.DateFormat
import org.json.JSONObject
import java.text.NumberFormat
import java.text.SimpleDateFormat
import java.util.Calendar
import java.util.Date
import java.util.Locale

/**
 * Amounts, times and days, how a task ended and how its steps read, in the
 * phone's language and as desktop Pinky says them (ui/format.go, ui/ask.go
 * and ui/extras.go: elapsedIn, usageText, dayLabel, pilotEndIn,
 * stepDisplay). The agent's and the task's fields are those of the core's
 * state (internal/standalone/state.go, AgentState and TaskState) and
 * history (history.go, PastTask).
 */
object Words {

    /** A duration: 42 s, 3 min 05 s, 1 h 02 min. */
    fun elapsed(res: Resources, seconds: Long): String {
        val s = seconds.coerceAtLeast(0)
        return when {
            s < 60 -> res.getString(R.string.phone_time_seconds, s.toInt())
            s < 3600 -> res.getString(R.string.phone_time_minutes, (s / 60).toInt(), two(s % 60))
            else -> res.getString(R.string.phone_time_hours, (s / 3600).toInt(), two(s % 3600 / 60))
        }
    }

    fun dollars(res: Resources, v: Double): String =
        res.getString(R.string.phone_dollars, String.format(Locale.ROOT, "%.2f", v))

    fun steps(res: Resources, n: Int): String = res.getQuantityString(R.plurals.phone_steps, n, n)

    fun minutes(res: Resources, n: Int): String = res.getQuantityString(R.plurals.phone_minutes, n, n)

    fun tokens(res: Resources, n: Long): String = res.getQuantityString(R.plurals.phone_tokens, count(n), number(n))

    /** A task's cost limit: in dollars for a model whose prices Pinky knows, else in tokens. */
    fun limit(res: Resources, priced: Boolean, maxCost: Double, tokenLimit: Long): String =
        if (priced) dollars(res, maxCost) else tokens(res, tokenLimit)

    /** What a task used: its steps, its cost (or tokens, for a model without prices) and its time. */
    fun usage(res: Resources, agent: JSONObject): String {
        val parts = mutableListOf(steps(res, agent.optInt("steps")))
        val used = agent.optLong("tokens")
        if (agent.optBoolean("priced")) {
            parts += dollars(res, agent.optDouble("cost", 0.0))
        } else if (used > 0) {
            parts += tokens(res, used)
        }
        parts += elapsed(res, agent.optLong("seconds"))
        return parts.joinToString(" · ")
    }

    /** How a task ended: a title, and why, which may be empty. */
    fun end(res: Resources, agent: JSONObject): Pair<String, String> {
        val summary = agent.optString("summary")
        val stopped = res.getString(R.string.phone_end_stopped)
        val failed = res.getString(R.string.phone_end_failed)
        val notStarted = res.getString(R.string.phone_end_not_started)
        return when (agent.optString("end")) {
            "done" -> res.getString(R.string.phone_end_done) to summary
            "stopped" -> stopped to ""
            "steps" -> {
                val n = agent.optInt("max_steps")
                stopped to res.getQuantityString(R.plurals.phone_limit_steps, n, n)
            }
            "time" -> {
                val n = agent.optInt("max_minutes")
                stopped to res.getQuantityString(R.plurals.phone_limit_minutes, n, n)
            }
            "cost" -> stopped to if (agent.optBoolean("priced")) {
                res.getString(R.string.phone_limit_cost, dollars(res, agent.optDouble("max_cost", 0.0)))
            } else {
                val n = agent.optLong("token_limit")
                res.getQuantityString(R.plurals.phone_limit_tokens, count(n), number(n))
            }
            "refused" -> failed to res.getString(R.string.phone_refused)
            "no_key" -> notStarted to res.getString(R.string.phone_no_key)
            "bad_key" -> notStarted to res.getString(R.string.phone_bad_key)
            else -> failed to summary
        }
    }

    /** How a task ended, in one line. */
    fun endLine(res: Resources, agent: JSONObject): String {
        val (title, reason) = end(res, agent)
        return if (reason.isEmpty()) title else res.getString(R.string.phone_end_line, title, reason)
    }

    /** How a task of the history ended, in a word (PastTask.Outcome). */
    fun outcome(res: Resources, outcome: String): String = res.getString(
        when (outcome) {
            "done" -> R.string.phone_end_done
            "stopped" -> R.string.phone_end_stopped
            "not_started" -> R.string.phone_end_not_started
            else -> R.string.phone_end_failed
        },
    )

    /**
     * The day of a time (Unix ms), as desktop Pinky heads Activity's days
     * (dayLabel): Today, Yesterday, else its date, with the weekday this
     * year and with the year before it. short leaves the weekday out, for
     * a row.
     */
    fun day(res: Resources, ms: Long, short: Boolean = false): String {
        val t = Calendar.getInstance().apply { timeInMillis = ms }
        val d = Calendar.getInstance()
        if (sameDay(t, d)) return res.getString(R.string.phone_today)
        val thisYear = t.get(Calendar.YEAR) == d.get(Calendar.YEAR)
        d.add(Calendar.DAY_OF_YEAR, -1)
        if (sameDay(t, d)) return res.getString(R.string.phone_yesterday)
        val skeleton = when {
            short && thisYear -> "MMMd"
            short -> "yMMMd"
            thisYear -> "MMMMEEEEd"
            else -> "yMMMMd"
        }
        val locale = res.configuration.locales[0]
        return SimpleDateFormat(DateFormat.getBestDateTimePattern(locale, skeleton), locale).format(Date(ms))
    }

    /** A time of day (Unix ms), on the phone's 12- or 24-hour clock. */
    fun clock(context: Context, ms: Long): String = DateFormat.getTimeFormat(context).format(Date(ms))

    private fun sameDay(a: Calendar, b: Calendar) =
        a.get(Calendar.YEAR) == b.get(Calendar.YEAR) && a.get(Calendar.DAY_OF_YEAR) == b.get(Calendar.DAY_OF_YEAR)

    /** A step's mark (ui/format.go, kindOf). */
    enum class Kind {
        DONE, // over: done, or not, for the reason under it
        WAIT, // waits for the person
        NOW, // the step the agent is on
        YOU, // the person's own: an answer, a handoff, taking over
    }

    /** How a step reads: its line, a smaller line under it, and its mark. */
    class Step(val text: String, val sub: String, val kind: Kind)

    /**
     * How one of the task's steps reads, as desktop Pinky words it
     * (ui/extras.go, stepDisplay). Besides the step's status it reads the
     * gateway's own words: an approval's step says "Waiting for you: Tapped
     * …" while it waits and keeps that text when it is denied or withdrawn,
     * with only its status changing. The core says the gateway's words in
     * the screens' language beside the English (shown, asked; desktop
     * Pinky's internal/gwtext), and the step reads them when they are
     * there; the English says what kind of step it is. last is whether it
     * is the task's last step, taskState the task's state
     * (gateway.TaskState).
     */
    fun step(res: Resources, s: JSONObject, last: Boolean, taskState: String): Step {
        val status = s.optString("status")
        val full = s.optString("text")
        // A step's second line, after a newline, is a detail.
        val first = full.substringBefore('\n')
        val shown = s.optString("shown").ifEmpty { full }
        var sub = shown.substringAfter('\n', "").trim()
        var kind = when {
            status == "approval_pending" -> Kind.WAIT
            // The core's steps carry no tool, as desktop Pinky's carry none
            // outside developer mode: the person's own say "You …".
            status == "user" || full.startsWith("You ") -> Kind.YOU
            (status == "" || status == "running") && last && taskState in RUNNING -> Kind.NOW
            else -> Kind.DONE
        }
        val waiting = first.startsWith(WAITING)
        val action = if (waiting) s.optString("asked").ifEmpty { imperative(first.removePrefix(WAITING)) } else ""
        val isHandoff = first.startsWith(HANDOFF)
        val handoff = first.removePrefix(HANDOFF)
        val isAsk = first.startsWith(ASK)
        val asked = first.removePrefix(ASK)
        fun askedAbove(why: String) = withSub(res.getString(R.string.phone_step_asked, why), sub)
        var said: String? = null // Pinky's words, not the gateway's
        when (status) {
            "approval_pending" -> when {
                // Answered: the gateway closes the step once the action ran.
                waiting && taskState == "working" -> {
                    said = res.getString(R.string.phone_step_answered, action)
                    kind = Kind.YOU
                }
                waiting -> said = res.getString(R.string.phone_step_waiting, action)
            }
            "approval_denied" -> {
                when {
                    waiting -> said = res.getString(R.string.phone_step_denied, action)
                    isHandoff -> {
                        said = res.getString(R.string.phone_step_declined)
                        sub = askedAbove(handoff)
                    }
                    isAsk -> said = res.getString(R.string.phone_step_said_no, asked)
                }
                if (waiting || isHandoff || isAsk) kind = Kind.YOU
            }
            "settled" -> when {
                isHandoff -> {
                    said = res.getString(R.string.phone_step_handed_back)
                    sub = askedAbove(handoff)
                    kind = Kind.YOU
                }
                isAsk -> {
                    said = res.getString(R.string.phone_step_agreed, asked)
                    kind = Kind.YOU
                }
            }
            "user_took_over" -> {
                if (waiting) said = upperFirst(action)
                sub = res.getString(R.string.phone_step_took_over)
                kind = Kind.YOU
            }
            "paused" -> sub = res.getString(R.string.phone_step_in_control)
            "stopped" -> {
                if (waiting) said = upperFirst(action)
                sub = res.getString(R.string.phone_step_stopped)
            }
            "stale" -> sub = res.getString(R.string.phone_step_stale)
            // The gateway's line under it stays: an Always allow that let the
            // step run without asking.
            "uncertain" -> sub = withSub(res.getString(R.string.phone_step_maybe), sub)
            "expired" -> {
                if (waiting) said = upperFirst(action)
                sub = res.getString(R.string.phone_step_not_used)
            }
            "device_locked" -> sub = res.getString(R.string.phone_step_locked)
            "device_offline" -> sub = res.getString(R.string.phone_step_offline)
            "device_needs_setup" -> sub = res.getString(R.string.phone_step_needs_setup)
            else -> FAILURES[status]?.let { sub = res.getString(R.string.phone_step_failed, res.getString(it)) }
        }
        val text = said ?: if (waiting && kind != Kind.WAIT) upperFirst(action) else shown.substringBefore('\n')
        return Step(text, sub, kind)
    }

    /**
     * One of the gateway's words in the state (a need's title, detail or
     * rule, the phone's line) as the core says it in the screens' language,
     * its field_shown, or as the gateway wrote it.
     */
    fun shown(j: JSONObject, field: String): String = j.optString(field + "_shown").ifEmpty { j.optString(field) }

    // withSub puts Pinky's line about a step above the step's own second
    // line, when it has one.
    private fun withSub(line: String, sub: String) = if (sub.isEmpty()) line else line + "\n" + sub

    // imperative turns a step's past tense into what was asked: "Tapped
    // “Pay”" becomes "tap “Pay”".
    private fun imperative(s: String): String {
        for ((past, ask) in IMPERATIVES) {
            if (s.startsWith(past)) return ask + s.substring(past.length)
        }
        return lowerFirst(s)
    }

    // lowerFirst lowers a sentence's first letter for use mid-sentence,
    // keeping acronyms such as "USB" as they are.
    private fun lowerFirst(s: String): String {
        if (s.isEmpty()) return s
        val c = s.codePointAt(0)
        val n = Character.charCount(c)
        if (!Character.isUpperCase(c)) return s
        if (n < s.length && Character.isUpperCase(s.codePointAt(n))) return s // an acronym
        return String(Character.toChars(Character.toLowerCase(c))) + s.substring(n)
    }

    private fun upperFirst(s: String): String {
        if (s.isEmpty()) return s
        val c = s.codePointAt(0)
        return String(Character.toChars(Character.toUpperCase(c))) + s.substring(Character.charCount(c))
    }

    // The gateway's words that begin a step waiting for the person, one
    // that asked them to take over, and one that asked them a question.
    private const val WAITING = "Waiting for you: "
    private const val HANDOFF = "Asked you to take over: "
    private const val ASK = "Asked you: "

    // States of a task that has not ended (ui/format.go, running).
    private val RUNNING = setOf("working", "waiting", "paused")

    private val IMPERATIVES = listOf(
        "Long-pressed " to "long-press ", "Tapped " to "tap ", "Typed " to "type ", "Swiped " to "swipe ",
        "Pressed " to "press ", "Opened " to "open ", "Zoomed " to "zoom ", "Turned " to "turn ",
        "Dragged " to "drag ",
    )

    // The error codes a step can end with (ui/extras.go, failures).
    private val FAILURES = mapOf(
        "lease_held" to R.string.phone_code_lease_held,
        "scope_denied" to R.string.phone_code_scope_denied,
        "not_found" to R.string.phone_code_not_found,
        "unsupported" to R.string.phone_code_unsupported,
        "invalid_argument" to R.string.phone_code_invalid_argument,
        "internal" to R.string.phone_code_internal,
    )

    private fun two(n: Long) = String.format(Locale.ROOT, "%02d", n)

    private fun number(n: Long): String = NumberFormat.getIntegerInstance().format(n)

    // A plural's quantity: Android's are Ints.
    private fun count(n: Long): Int = n.coerceIn(0, Int.MAX_VALUE.toLong()).toInt()
}

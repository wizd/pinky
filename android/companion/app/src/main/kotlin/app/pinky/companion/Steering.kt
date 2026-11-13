package app.pinky.companion

import org.json.JSONObject

/**
 * The person's messages to Pinky's own agent (internal/standalone,
 * control.go): steer sends one to the running task, which the model reads
 * at its next step; until then it waits in the state's agent.pending and
 * can be edited or withdrawn. Once a task ended and agent.continuable says
 * so, continue goes on with it, sending a new message and those left
 * unread. The task screen (TaskActivity) shows both.
 */
object Steering {

    /** A message the model has not read yet (standalone.Message). */
    data class Message(val id: String, val text: String, val ms: Long)

    /** The messages waiting in an agent's state (AgentState.Pending), oldest first. */
    fun pending(agent: JSONObject?): List<Message> {
        val a = agent?.optJSONArray("pending") ?: return emptyList()
        val out = ArrayList<Message>(a.length())
        for (i in 0 until a.length()) {
            val m = a.optJSONObject(i) ?: continue
            val id = m.optString("id")
            if (id.isEmpty()) continue
            out += Message(id, m.optString("text"), m.optLong("ms"))
        }
        return out
    }

    /** Whether the agent's task ended and may go on (AgentState.Continuable). */
    fun continuable(agent: JSONObject?): Boolean =
        agent != null && !agent.optBoolean("running") && agent.optBoolean("continuable")

    /** Whether Send may send text: there is some, and nothing is on its way. */
    fun canSend(text: CharSequence, sending: Boolean): Boolean = text.isNotBlank() && !sending

    /**
     * Whether Continue may go on: with a message, or without one when
     * messages wait to be sent; not while a continue is on its way or
     * Pinky on a computer uses the phone.
     */
    fun canContinue(text: CharSequence, pending: Int, sending: Boolean, computer: Boolean): Boolean =
        (text.isNotBlank() || pending > 0) && !sending && !computer

    /** What the screen does with the core's refusal of steer or withdraw. */
    enum class Refusal {
        /** The model read the message already: say so. */
        READ,

        /** The message was withdrawn already: nothing to say. */
        GONE,

        /** The task ended meanwhile: the text moves to Continue's field. */
        ENDED,

        /** Nothing to send: Send waits for text. */
        EMPTY,

        /** Anything else: Couldn't send, with why. */
        FAILED,
    }

    /** The screen's answer to a refusal's code (control.go's Code constants). */
    fun refusal(code: String): Refusal = when (code) {
        "read" -> Refusal.READ
        "no_message" -> Refusal.GONE
        "not_running" -> Refusal.ENDED
        "empty" -> Refusal.EMPTY
        else -> Refusal.FAILED
    }

    /**
     * Text moved into a field that may already hold some: the person's
     * own words come first, then the moved ones (the fields are single
     * lines).
     */
    fun joined(field: String, moved: String): String {
        val a = field.trim()
        val b = moved.trim()
        return when {
            a.isEmpty() -> b
            b.isEmpty() -> a
            else -> "$a $b"
        }
    }
}

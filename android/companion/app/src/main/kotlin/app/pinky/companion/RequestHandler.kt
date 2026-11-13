package app.pinky.companion

import android.os.Build
import android.os.SystemClock
import android.util.Log
import org.json.JSONException
import org.json.JSONObject
import java.util.concurrent.ExecutionException
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException

/**
 * A request failed in a way the client should see. Codes are documented in
 * the protocol notes; details adds fields to the error object.
 */
class RequestError(val code: String, message: String, val details: ((Json) -> Unit)? = null) : Exception(message)

/** A reply line, plus work to run once it has been written. */
class Reply(val line: String, val after: (() -> Unit)? = null)

/**
 * Parses one request line and runs it. Everything that touches the
 * accessibility tree runs on a single worker thread: Android keeps one
 * interaction client per calling thread, and one thread also keeps two
 * clients from walking the tree at the same time.
 */
class RequestHandler(
    private val service: PinkyAccessibilityService,
    private val server: CompanionServer,
) {

    private val worker = Executors.newSingleThreadExecutor { r ->
        // Trees can be deep and the walk is recursive.
        Thread(null, r, "pinky-a11y", 8L shl 20).apply { isDaemon = true }
    }

    fun shutdown() {
        worker.shutdownNow()
    }

    /** Runs work that touches the accessibility tree on the worker thread, without a request. */
    fun runOnWorker(task: () -> Unit) {
        try {
            worker.execute {
                try {
                    task()
                } catch (e: Exception) {
                    Log.w(TAG, "background work failed", e)
                }
            }
        } catch (_: java.util.concurrent.RejectedExecutionException) {
            // Shut down already.
        }
    }

    fun handle(line: String, conn: Connection): Reply {
        var id: Any? = null
        return try {
            val req = JSONObject(line)
            id = req.opt("id")
            val cmd = req.optString("cmd")
            when (cmd) {
                "ping" -> Reply(ok(id, ping()))
                "tree" -> Reply(ok(id, onWorker(req) { service.tree.dump(treeOptions(req)) }))
                "set_text" -> Reply(ok(id, onWorker(req) { service.textSetter.setText(req) }))
                "events" -> subscribe(id, req, conn)
                "wait_idle" -> Reply(ok(id, waitIdle(req)))
                "config" -> Reply(ok(id, service.configure(req)))
                "gesture" -> Reply(ok(id, service.actions.gesture(req)))
                // Another display's focus is read from its windows, on the
                // worker like the other commands that read windows.
                "global" -> Reply(
                    ok(id, if ((displayParam(req) ?: 0) != 0) onWorker(req) { service.actions.global(req) } else service.actions.global(req)),
                )
                "on_phone" -> {
                    if (!conn.own) throw RequestError("forbidden", "only Pinky on the phone may change its mode")
                    Reply(ok(id, service.setOnPhone(req.getBoolean("enabled"))))
                }
                "claim" -> Reply(ok(id, claim(req, conn)))
                "debug_model" -> Reply(ok(id, debugModel(req, conn)))
                else -> service.actionCommand(cmd, req)?.let { Reply(ok(id, it)) }
                    ?: throw RequestError("unknown_command", "unknown command: $cmd")
            }
        } catch (e: RequestError) {
            Reply(error(id, e.code, e.message ?: e.code, e.details))
        } catch (e: JSONException) {
            Reply(error(id, "bad_request", e.message ?: "invalid JSON"))
        } catch (e: Exception) {
            Log.w(TAG, "request failed", e)
            Reply(error(id, "internal", e.toString()))
        }
    }

    private fun ping(): String {
        val j = Json()
        j.beginObject()
        j.field("protocol", PROTOCOL_VERSION)
        j.field("version", BuildConfig.VERSION_NAME)
        j.field("version_code", BuildConfig.VERSION_CODE)
        j.field("sdk", Build.VERSION.SDK_INT)
        j.field("release", Build.VERSION.RELEASE)
        j.field("manufacturer", Build.MANUFACTURER)
        j.field("model", Build.MODEL)
        j.field("time", System.currentTimeMillis())
        j.field("uptime_ms", SystemClock.uptimeMillis())
        j.name("displays").beginArray()
        onWorker(null) { service.displays.list() }.forEach { j.value(it) }
        j.endArray()
        j.name("features").beginArray()
        features().forEach { j.value(it) }
        j.endArray()
        j.field("connections", server.pingCount)
        j.field("include_not_important", service.includeNotImportant)
        j.field("idle_disable_ms", service.idleDisableMs)
        j.field("on_phone", service.onPhone)
        server.holder?.let { h ->
            j.name("claim").beginObject()
            h.write(j)
            j.endObject()
        }
        j.endObject()
        return j.toString()
    }

    /**
     * debug_model sets the model's service (or API), address and model and saves its
     * key, as Settings does, for tests with a real model (corectl model,
     * which reads the key from its standard input so that it never appears
     * on a command line). Only debug builds have it, and only for this
     * app's own uid (run-as); release builds answer unknown_command.
     * Neither the key nor its length is logged or sent back.
     */
    private fun debugModel(req: JSONObject, conn: Connection): String {
        if (!BuildConfig.DEBUG) throw RequestError("unknown_command", "unknown command: debug_model")
        if (!conn.own) throw RequestError("forbidden", "only this app's own uid may set the model")
        val settings = TaskSettings(service)
        // A service, or an API as before there were services (corectl model).
        val id = req.optString("service").ifEmpty {
            val p = if (req.optString("provider") == TaskSettings.OPENAI) TaskSettings.OPENAI else TaskSettings.ANTHROPIC
            ModelServices.serviceFor(p, req.optString("base_url"))
        }
        if (ModelServices.byId(service, id) == null) throw RequestError("bad_request", "unknown service $id")
        settings.service = id
        if (req.has("base_url")) {
            val u = req.getString("base_url")
            if (!TaskSettings.urlOK(u)) throw RequestError("bad_request", "base_url must be http or https")
            settings.setBaseUrl(id, u)
        }
        if (req.has("model")) settings.setModel(id, req.getString("model"))
        val key = req.optString("key")
        val name = settings.keyName(id)
        if (key.isNotEmpty()) Secrets(service).save(name, key)
        val j = Json()
        j.beginObject()
        j.field("service", id)
        j.field("provider", settings.provider(id))
        j.field("key_saved", Secrets(service).has(name))
        j.endObject()
        return j.toString()
    }

    /**
     * claim gives the phone to this connection; with release it ends the
     * connection's claim, and with stop Pinky on the phone ends a computer's.
     * The result is who holds the phone afterwards (nothing when it is
     * free), and stop adds whether it ended a claim.
     */
    private fun claim(req: JSONObject, conn: Connection): String {
        val j = Json()
        j.beginObject()
        when {
            req.optBoolean("stop") -> {
                if (!conn.own) throw RequestError("forbidden", "only Pinky on the phone may stop a computer's claim")
                j.field("stopped", server.stopClaim())
            }
            req.optBoolean("release") -> server.release(conn)
            else -> {
                val by = req.getString("by")
                val self = if (conn.own) CompanionServer.BY_PHONE else CompanionServer.BY_COMPUTER
                if (by != self) throw RequestError("bad_request", "this connection claims as $self, not $by")
                val task = if (req.isNull("task")) "" else clip(req.getString("task"), MAX_TASK_CHARS)
                server.claim(conn, task)?.let { h ->
                    val who = if (h.by == CompanionServer.BY_PHONE) "Pinky on the phone" else "Pinky on a computer"
                    throw RequestError("busy", "$who is using the phone") { h.write(it) }
                }
            }
        }
        server.holder?.write(j)
        j.endObject()
        return j.toString()
    }

    // The first max chars of s, without splitting a surrogate pair.
    private fun clip(s: String, max: Int): String {
        if (s.length <= max) return s
        val end = if (Character.isHighSurrogate(s[max - 1])) max - 1 else max
        return s.substring(0, end)
    }

    /**
     * What this app supports on this phone, beyond protocol 1 as it first
     * shipped. Clients check a feature before relying on it: an older app
     * ignores parameters it does not know.
     */
    private fun features(): List<String> {
        val f = mutableListOf<String>()
        // tree, set_text, events and wait_idle take a display other than 0.
        if (service.displays.multiDisplay) f += FEATURE_DISPLAYS
        // set_text tries the input connection and ACTION_PASTE when
        // ACTION_SET_TEXT is refused, and says which went in (method).
        f += FEATURE_SET_TEXT_FALLBACK
        // gesture and global, which refuse Pinky's own controls (own_ui);
        // on other displays gesture plays segments and holds a finger
        // down, and global presses back and enter (display_input).
        f += listOf(FEATURE_GESTURE, FEATURE_GLOBAL, FEATURE_OWN_UI)
        if (service.displays.multiDisplay) f += FEATURE_DISPLAY_INPUT
        // claim and claim events: who drives the phone.
        f += FEATURE_CLAIM
        f += service.actionFeatures()
        return f
    }

    /** The optional display parameter, checked; null when absent. */
    private fun displayParam(req: JSONObject): Int? {
        if (!req.has("display") || req.isNull("display")) return null
        val d = req.getInt("display")
        service.displays.check(d)
        return d
    }

    private fun treeOptions(req: JSONObject) = TreeDumper.Options(
        display = req.optInt("display", 0),
        ime = req.optBoolean("ime"),
        invisible = req.optBoolean("invisible"),
        bars = req.optBoolean("bars"),
        maxNodes = req.optInt("max_nodes", 20_000).coerceIn(1, 100_000),
    )

    private fun subscribe(id: Any?, req: JSONObject, conn: Connection): Reply {
        val kinds = mutableSetOf<String>()
        val arr = req.optJSONArray("kinds")
        if (arr == null) {
            kinds += EventHub.DEFAULT_KINDS
        } else {
            for (i in 0 until arr.length()) {
                val k = arr.getString(i)
                if (k !in EventHub.ALL_KINDS) throw RequestError("bad_request", "unknown event kind: $k")
                kinds += k
            }
        }
        val display = displayParam(req)
        val sub = conn.subscribe(kinds, display)
        val j = Json()
        j.beginObject()
        j.name("kinds").beginArray()
        kinds.sorted().forEach { j.value(it) }
        j.endArray()
        display?.let { j.field("display", it) }
        j.field("time", System.currentTimeMillis())
        j.endObject()
        return Reply(ok(id, j.toString())) { sub?.start() }
    }

    private fun waitIdle(req: JSONObject): String {
        val quiet = req.optLong("quiet_ms", 300).coerceIn(10, 10_000)
        val timeout = req.optLong("timeout_ms", 3_000).coerceIn(0, 30_000)
        return service.events.waitIdle(quiet, timeout, displayParam(req))
    }

    private fun <T> onWorker(req: JSONObject?, task: () -> T): T {
        val timeout = req?.optLong("timeout_ms", DEFAULT_TIMEOUT_MS) ?: DEFAULT_TIMEOUT_MS
        val f = worker.submit<T> { task() }
        try {
            return f.get(timeout, TimeUnit.MILLISECONDS)
        } catch (e: TimeoutException) {
            f.cancel(true)
            throw RequestError("timeout", "the request took longer than $timeout ms")
        } catch (e: ExecutionException) {
            throw e.cause ?: e
        }
    }

    private fun ok(id: Any?, result: String): String {
        val sb = StringBuilder(result.length + 32)
        sb.append('{')
        appendId(sb, id)
        sb.append("\"ok\":true,\"result\":").append(result).append('}')
        return sb.toString()
    }

    private fun error(id: Any?, code: String, message: String, details: ((Json) -> Unit)? = null): String {
        val j = Json()
        j.beginObject()
        when (id) {
            is Number -> j.field("id", id.toLong())
            is String -> j.field("id", id)
        }
        j.field("ok", false)
        j.name("error").beginObject().field("code", code).field("message", message)
        details?.invoke(j)
        j.endObject()
        j.endObject()
        return j.toString()
    }

    private fun appendId(sb: StringBuilder, id: Any?) {
        when (id) {
            is Number -> sb.append("\"id\":").append(id.toLong()).append(',')
            is String -> sb.append("\"id\":").append(Json().value(id).toString()).append(',')
        }
    }

    companion object {
        private const val TAG = "PinkyRequests"
        const val PROTOCOL_VERSION = 1
        const val FEATURE_DISPLAYS = "displays"
        const val FEATURE_SET_TEXT_FALLBACK = "set_text_fallback"
        const val FEATURE_GESTURE = "gesture"
        const val FEATURE_GLOBAL = "global"
        const val FEATURE_OWN_UI = "own_ui"
        const val FEATURE_DISPLAY_INPUT = "display_input"
        const val FEATURE_CLAIM = "claim"
        private const val DEFAULT_TIMEOUT_MS = 10_000L

        // A claim's task as the phone keeps and shows it.
        private const val MAX_TASK_CHARS = 500
    }
}

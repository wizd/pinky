package app.pinky.companion

import android.content.res.Resources
import org.json.JSONObject
import java.text.SimpleDateFormat
import java.util.Calendar
import java.util.Locale

/**
 * What Pinky's agent on the phone used, as the core counts it by day,
 * service and model (pilot.Usage, the control socket's usage command),
 * and how the screens say it, as desktop Pinky does (ui/ask.go: askUsage,
 * amountText, usageWho). It stays on the phone.
 */
object ModelUsage {

    /** A day's use of one model on one service: pilot.UsageRow. */
    data class Row(
        val day: String, // 2006-01-02, the phone's day
        val service: String,
        val host: String, // the address's host, for the custom entries
        val model: String,
        val requests: Int,
        val input: Long,
        val output: Long,
        val cacheRead: Long,
        val cacheWrite: Long,
        val cost: Double, // US dollars, for the tokens whose prices Pinky knows
        val unpriced: Long, // the tokens whose prices Pinky doesn't know
    ) {
        val tokens: Long get() = input + output + cacheRead + cacheWrite
    }

    /** What some rows add up to. */
    data class Total(val requests: Int, val tokens: Long, val cost: Double, val unpriced: Long)

    /** How many days back Settings shows, today included. */
    const val DAYS = 30

    /** The rows of the last DAYS days, newest first; blocks, so call it off the main thread. Throws when the core is not there. */
    fun read(): List<Row> {
        val r = CoreClient.call(CoreClient.cmd("usage", "since" to dayOf(-(DAYS - 1))))
        val arr = r.optJSONArray("usage") ?: return emptyList()
        return (0 until arr.length()).map { parse(arr.getJSONObject(it)) }
    }

    fun parse(o: JSONObject) = Row(
        day = o.optString("day"),
        service = o.optString("service"),
        host = o.optString("host"),
        model = o.optString("model"),
        requests = o.optInt("requests"),
        input = o.optLong("input"),
        output = o.optLong("output"),
        cacheRead = o.optLong("cache_read"),
        cacheWrite = o.optLong("cache_write"),
        cost = o.optDouble("cost", 0.0),
        unpriced = o.optLong("unpriced"),
    )

    /** The day offset days from today, 2006-01-02, as the core names days. */
    fun dayOf(offset: Int): String {
        val c = Calendar.getInstance()
        c.add(Calendar.DAY_OF_YEAR, offset)
        return SimpleDateFormat("yyyy-MM-dd", Locale.ROOT).format(c.time)
    }

    /** The day's time at noon (Unix ms), for Words.day; null for a day it cannot read. */
    fun dayMillis(day: String): Long? = try {
        SimpleDateFormat("yyyy-MM-dd", Locale.ROOT).parse(day)?.time?.plus(12 * 3600 * 1000L)
    } catch (_: Exception) {
        null
    }

    /** What the rows of the days from since on add up to. */
    fun total(rows: List<Row>, since: String = ""): Total {
        var req = 0
        var tok = 0L
        var cost = 0.0
        var unp = 0L
        for (r in rows) {
            if (r.day < since) continue
            req += r.requests
            tok += r.tokens
            cost += r.cost
            unp += r.unpriced
        }
        return Total(req, tok, cost, unp)
    }

    /** Tokens and, when some had known prices, what they cost: "48,210 tokens · US$0.214". */
    fun amount(res: Resources, tokens: Long, cost: Double, unpriced: Long): String {
        var s = Words.tokens(res, tokens)
        if (unpriced < tokens) {
            val digits = if (cost < 1) 3 else 2
            s += " · " + res.getString(R.string.phone_dollars, String.format(Locale.ROOT, "%.${digits}f", cost))
        }
        return s
    }

    fun amount(res: Resources, t: Total): String = amount(res, t.tokens, t.cost, t.unpriced)

    /** The service and model of a row: "DeepSeek · deepseek-flash". */
    fun who(settings: TaskSettings, res: Resources, r: Row): String {
        val svc = settings.svcOrNull(r.service)
        var who = r.host
        if (svc != null && (who.isEmpty() || !svc.custom)) who = settings.serviceLabel(res, svc)
        if (who.isEmpty()) who = r.service
        return who + " · " + (svc?.modelLabel(r.model) ?: r.model)
    }

    /** Today's use, for the task screen; null when nothing. */
    fun today(res: Resources, rows: List<Row>): String? {
        val t = total(rows, dayOf(0))
        return if (t.tokens > 0) res.getString(R.string.phone_usage_today_line, amount(res, t)) else null
    }
}

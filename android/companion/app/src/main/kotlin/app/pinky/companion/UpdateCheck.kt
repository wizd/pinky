package app.pinky.companion

import android.content.Context
import android.os.Build
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.util.Locale

/**
 * Looks for a newer Pinky for Android in Pinky's release feed
 * (docs/notes/updates.md, "The feed"), for people who use Pinky on the
 * phone without a computer; with a computer, desktop Pinky updates the app
 * over adb as before. The feed's android-arm64 asset names the APK and its
 * version_code: the APK changes only when the app does, so a release that
 * changes only the desktop app offers nothing here.
 *
 * Only an APK from Pinky's own releases is offered (UpdateRules), and
 * UpdateInstall downloads it, checks it against the feed's sha256 and size,
 * and hands it to Android's installer, which installs it as an update only
 * when Pinky's own key signed it.
 */
object UpdateCheck {

    /** A newer build than this one. */
    data class Update(
        val version: String,
        val versionCode: Int,
        val url: String,
        val sha256: String,
        val size: Long,
        val notes: String,
    )

    /** How often the app looks, at most. */
    const val INTERVAL_MS = 12 * 60 * 60 * 1000L

    private const val FEED = "https://github.com/DIYgod/pinky/releases/latest/download/latest.json"
    private const val ASSET = "android-arm64"
    private const val MAX_FEED = 1 shl 20
    private const val PREFS = "update"

    /** Whether this build looks for updates: development builds never update. */
    val enabled: Boolean get() = !BuildConfig.DEBUG && !BuildConfig.VERSION_NAME.startsWith("0.0.0")

    /**
     * Fetches the feed and returns the update it offers, or null when this
     * build is current; offered returns it from then on. Runs on a
     * background thread; throws when the feed cannot be read.
     */
    fun check(context: Context): Update? {
        if (!enabled) return null
        val feed = JSONObject(fetch(FEED))
        val u = parse(feed, Locale.getDefault())?.takeIf { it.versionCode > BuildConfig.VERSION_CODE }
        val e = prefs(context).edit().putLong("checked", System.currentTimeMillis())
        if (u != null) {
            e.putString("version", u.version).putInt("version_code", u.versionCode)
                .putString("url", u.url).putString("sha256", u.sha256).putLong("size", u.size)
                .putString("notes", u.notes)
        } else {
            e.remove("version").remove("version_code").remove("url").remove("sha256").remove("size").remove("notes")
        }
        e.apply()
        return u
    }

    /** Whether the last check is older than INTERVAL_MS. */
    fun due(context: Context): Boolean {
        val last = prefs(context).getLong("checked", 0)
        val now = System.currentTimeMillis()
        return enabled && (now - last >= INTERVAL_MS || now < last)
    }

    /**
     * The update the last check found, unless it is installed by now or
     * the person put it off.
     */
    fun offered(context: Context): Update? {
        val p = prefs(context)
        val version = p.getString("version", null) ?: return null
        val code = p.getInt("version_code", 0)
        val url = p.getString("url", null) ?: return null
        val sha256 = p.getString("sha256", null) ?: return null
        val size = p.getLong("size", 0)
        if (!enabled || code <= BuildConfig.VERSION_CODE || p.getString("put_off", null) == version) return null
        // Kept by an older build, which took any address: checked again.
        if (!UpdateRules.isReleaseApk(url) || !UpdateRules.isSha256(sha256) || !UpdateRules.sizeOk(size)) return null
        return Update(version, code, url, sha256, size, p.getString("notes", null).orEmpty())
    }

    /** Stops offering this update; a later one is offered again. */
    fun putOff(context: Context, u: Update) {
        prefs(context).edit().putString("put_off", u.version).apply()
    }

    /** What Pinky tells the release servers it is. */
    val userAgent: String
        get() = "Pinky/${BuildConfig.VERSION_NAME} (android; ${Build.SUPPORTED_ABIS.firstOrNull() ?: "unknown"})"

    private fun prefs(context: Context) = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /**
     * The feed's update for Android, or null when it has none that is
     * valid: an APK of Pinky's own releases, with its sha256 and size.
     */
    fun parse(feed: JSONObject, locale: Locale): Update? {
        val version = feed.optString("version")
        if (version.isEmpty()) return null
        val a = feed.optJSONObject("assets")?.optJSONObject(ASSET) ?: return null
        val url = a.optString("url")
        val code = a.optInt("version_code", 0)
        val sha256 = a.optString("sha256").lowercase(Locale.ROOT)
        val size = a.optLong("size", 0)
        if (!UpdateRules.isReleaseApk(url) || code <= 0 || !UpdateRules.isSha256(sha256) || !UpdateRules.sizeOk(size)) return null
        return Update(version, code, url, sha256, size, notes(feed, locale))
    }

    /** The release notes in the app's language: Chinese when it has them, else English. */
    private fun notes(feed: JSONObject, locale: Locale): String {
        val localized = feed.optJSONObject("localized_notes")
        if (localized != null && locale.language == "zh") {
            val zh = localized.optString("zh-Hans")
            if (zh.isNotEmpty()) return zh
        }
        return feed.optString("notes")
    }

    private fun fetch(url: String): String {
        var target = URL(url)
        // HttpURLConnection does not follow a redirect from one host to
        // another, and GitHub sends release files elsewhere.
        repeat(5) {
            val c = target.openConnection() as HttpURLConnection
            try {
                c.instanceFollowRedirects = false
                c.connectTimeout = 15_000
                c.readTimeout = 15_000
                c.setRequestProperty("User-Agent", userAgent)
                c.setRequestProperty("Accept", "application/json")
                when (val code = c.responseCode) {
                    in 300..399 -> {
                        val next = URL(target, c.getHeaderField("Location") ?: throw IllegalStateException("redirect without a location"))
                        if (!UpdateRules.isReleaseHost(next.toString())) throw IllegalStateException("redirect to ${next.protocol}://${next.host}")
                        target = next
                    }
                    200 -> {
                        val out = ByteArrayOutputStream()
                        c.inputStream.use { input ->
                            val buf = ByteArray(16 shl 10)
                            while (true) {
                                val n = input.read(buf)
                                if (n < 0) break
                                out.write(buf, 0, n)
                                if (out.size() > MAX_FEED) throw IllegalStateException("the feed is too large")
                            }
                        }
                        return out.toString("UTF-8")
                    }
                    else -> throw IllegalStateException("the feed answered $code")
                }
            } finally {
                c.disconnect()
            }
        }
        throw IllegalStateException("too many redirects")
    }
}

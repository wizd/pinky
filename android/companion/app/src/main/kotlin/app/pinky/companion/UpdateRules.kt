package app.pinky.companion

import java.net.URI

/**
 * Where Pinky for Android takes updates from: Pinky's own GitHub releases
 * and the servers GitHub sends their files from, and only an APK the feed
 * vouches for with its sha256 and size. Plain Kotlin, so that unit tests
 * check it without a phone.
 */
object UpdateRules {

    /** Pinky's releases; the feed and every APK come from here. */
    const val RELEASES = "https://github.com/DIYgod/pinky/releases/"

    /** The APK's name among a release's assets (scripts/release/manifest.go). */
    const val APK = "pinky-android.apk"

    /** The largest APK Pinky takes: the APK with the core is about 24 MB. */
    const val MAX_SIZE = 200L shl 20

    /** The hosts GitHub serves release files from, after a redirect. */
    private val FILE_HOSTS = setOf(
        "github.com",
        "objects.githubusercontent.com",
        "release-assets.githubusercontent.com",
    )

    /**
     * Whether url is the APK of one of Pinky's releases:
     * https://github.com/DIYgod/pinky/releases/download/<tag>/pinky-android.apk.
     */
    fun isReleaseApk(url: String): Boolean {
        val prefix = RELEASES + "download/"
        if (!url.startsWith(prefix) || !url.endsWith("/$APK")) return false
        val tag = url.substring(prefix.length, url.length - APK.length - 1)
        if (tag.isEmpty() || tag == "." || tag == ".." || tag.contains('%')) return false
        if (!tag.all { it.isLetterOrDigit() && it.code < 128 || it in "._-" }) return false
        val u = parse(url) ?: return false
        return u.scheme == "https" && u.host == "github.com" && u.port == -1 && u.rawQuery == null && u.rawFragment == null && u.rawUserInfo == null
    }

    /** Whether a download may go to url: https on Pinky's releases' hosts. */
    fun isReleaseHost(url: String): Boolean {
        val u = parse(url) ?: return false
        return u.scheme == "https" && u.rawUserInfo == null && (u.port == -1 || u.port == 443) && u.host?.lowercase() in FILE_HOSTS
    }

    /** Whether s is a sha256 in lowercase hex, as the feed writes it. */
    fun isSha256(s: String): Boolean = s.length == 64 && s.all { it in '0'..'9' || it in 'a'..'f' }

    /** Whether the feed's size of an APK is one Pinky takes. */
    fun sizeOk(size: Long): Boolean = size in 1..MAX_SIZE

    /** Lowercase hex of a digest. */
    fun hex(b: ByteArray): String = b.joinToString("") { "%02x".format(it.toInt() and 0xff) }

    private fun parse(url: String): URI? = try {
        URI(url)
    } catch (_: Exception) {
        null
    }
}

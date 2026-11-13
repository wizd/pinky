package app.pinky.companion

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdateRulesTest {

    @Test
    fun takesOnlyPinkysReleaseApk() {
        val ok = listOf(
            "https://github.com/DIYgod/pinky/releases/download/v2026.1112.9/pinky-android.apk",
            "https://github.com/DIYgod/pinky/releases/download/v2026.1107.2-rc_1/pinky-android.apk",
        )
        for (u in ok) assertTrue(u, UpdateRules.isReleaseApk(u))
        val no = listOf(
            "",
            "http://github.com/DIYgod/pinky/releases/download/v1/pinky-android.apk",
            "https://github.com/someone/pinky/releases/download/v1/pinky-android.apk",
            "https://github.com/DIYgod/pinky-fork/releases/download/v1/pinky-android.apk",
            "https://example.com/DIYgod/pinky/releases/download/v1/pinky-android.apk",
            "https://github.com.example.com/DIYgod/pinky/releases/download/v1/pinky-android.apk",
            "https://github.com@example.com/DIYgod/pinky/releases/download/v1/pinky-android.apk",
            "https://github.com:8443/DIYgod/pinky/releases/download/v1/pinky-android.apk",
            "https://github.com/DIYgod/pinky/releases/download/v1/other.apk",
            "https://github.com/DIYgod/pinky/releases/download/v1/pinky-android.apk?x=1",
            "https://github.com/DIYgod/pinky/releases/download/v1/pinky-android.apk#x",
            "https://github.com/DIYgod/pinky/releases/download//pinky-android.apk",
            "https://github.com/DIYgod/pinky/releases/download/../pinky-android.apk",
            "https://github.com/DIYgod/pinky/releases/download/v1/x/pinky-android.apk",
            "https://github.com/DIYgod/pinky/releases/download/v1%2F..%2Fx/pinky-android.apk",
            "https://github.com/DIYgod/pinky/releases/latest/download/pinky-android.apk",
        )
        for (u in no) assertFalse(u, UpdateRules.isReleaseApk(u))
    }

    @Test
    fun followsRedirectsOnlyToGitHubsFileHosts() {
        assertTrue(UpdateRules.isReleaseHost("https://github.com/DIYgod/pinky/releases/download/v1/pinky-android.apk"))
        assertTrue(UpdateRules.isReleaseHost("https://release-assets.githubusercontent.com/github-production-release-asset/1?sp=r"))
        assertTrue(UpdateRules.isReleaseHost("https://objects.githubusercontent.com/x"))
        assertFalse(UpdateRules.isReleaseHost("http://objects.githubusercontent.com/x"))
        assertFalse(UpdateRules.isReleaseHost("https://evil.example/x"))
        assertFalse(UpdateRules.isReleaseHost("https://objects.githubusercontent.com.evil.example/x"))
        assertFalse(UpdateRules.isReleaseHost("https://user@objects.githubusercontent.com/x"))
        assertFalse(UpdateRules.isReleaseHost("not a url"))
    }

    @Test
    fun checksTheFeedsSumAndSize() {
        assertTrue(UpdateRules.isSha256("a".repeat(64)))
        assertFalse(UpdateRules.isSha256("A".repeat(64)))
        assertFalse(UpdateRules.isSha256("a".repeat(63)))
        assertFalse(UpdateRules.isSha256("g".repeat(64)))
        assertTrue(UpdateRules.sizeOk(24L shl 20))
        assertFalse(UpdateRules.sizeOk(0))
        assertFalse(UpdateRules.sizeOk(UpdateRules.MAX_SIZE + 1))
        assertEquals("00ff10", UpdateRules.hex(byteArrayOf(0, -1, 16)))
    }

    @Test
    fun coreRunsOnlyWhereAndroidInstallsArm64() {
        val apk = setOf("arm64-v8a", "armeabi-v7a", "x86_64")
        assertEquals("arm64-v8a", Core.installedAbi(listOf("arm64-v8a", "armeabi-v7a", "armeabi"), apk))
        assertEquals("armeabi-v7a", Core.installedAbi(listOf("armeabi-v7a", "armeabi"), apk))
        // An x86_64 emulator that also translates arm64 gets the stand-in.
        assertEquals("x86_64", Core.installedAbi(listOf("x86_64", "x86", "arm64-v8a", "armeabi-v7a"), apk))
        assertNull(Core.installedAbi(listOf("x86"), apk))
    }
}

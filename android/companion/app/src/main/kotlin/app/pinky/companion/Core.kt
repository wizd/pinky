package app.pinky.companion

import android.content.Context
import android.os.Build
import android.util.Log
import java.util.zip.ZipFile

/**
 * Pinky's Go core (internal/standalone/libcore), a shared library in this
 * process: the gateway with its rules and approvals, the MCP tools and the
 * agent. It drives the phone through this app's own companion socket and
 * serves Pinky's screens on CORE_SOCKET. Started once per process.
 */
object Core {
    private const val TAG = "PinkyCore"

    @Volatile
    private var started = false

    @Volatile
    var error: String? = null
        private set

    /** The ABI the core is built for; the APK has only a stand-in for the others. */
    const val ABI = "arm64-v8a"

    private const val LIBRARY = "lib/$ABI/libpinkycore.so"

    @Volatile
    private var availableV: Boolean? = null

    /**
     * Whether this phone can run the core: Android installed the APK's
     * arm64 libraries, and the APK has the core. 32-bit-only phones get
     * the APK's stand-in for armeabi-v7a, x86_64 emulators the one for
     * x86_64 (even those that translate arm64), and a development build
     * may have been built without the core. Pinky works there with Pinky
     * on a computer, as before.
     */
    fun available(context: Context): Boolean = availableV ?: run {
        val ok = try {
            ZipFile(context.applicationInfo.sourceDir).use { zip ->
                val names = zip.entries().asSequence().map { it.name }.toList()
                val abis = names.mapNotNull { n -> n.split('/').takeIf { it.size == 3 && it[0] == "lib" }?.get(1) }.toSet()
                LIBRARY in names && installedAbi(Build.SUPPORTED_ABIS.toList(), abis) == ABI
            }
        } catch (e: Exception) {
            Log.w(TAG, "could not read the APK's libraries", e)
            false
        }
        availableV = ok
        if (!ok) Log.i(TAG, "no core for this phone (${Build.SUPPORTED_ABIS.joinToString()})")
        ok
    }

    /**
     * The ABI whose libraries Android installs from an APK that has
     * apkAbis: the phone's first one the APK has.
     */
    fun installedAbi(supported: List<String>, apkAbis: Set<String>): String? = supported.firstOrNull { it in apkAbis }

    @Synchronized
    fun start(context: Context) {
        if (started) return
        started = true
        if (!available(context)) {
            error = "the core library is missing: Pinky runs tasks on the phone only on 64-bit ARM phones"
            Log.e(TAG, "core did not start: $error")
            return
        }
        val err = try {
            System.loadLibrary("pinkycore")
            nativeStart(context.filesDir.absolutePath, context.packageName, BuildConfig.SOCKET_NAME, BuildConfig.CORE_SOCKET)
        } catch (e: UnsatisfiedLinkError) {
            "the core library is missing: $e"
        }
        if (err.isNotEmpty()) {
            error = err
            Log.e(TAG, "core did not start: $err")
        } else {
            Log.i(TAG, "core started")
        }
    }

    /** Starts the core; returns an error message, empty when it started. */
    @JvmStatic
    private external fun nativeStart(dataDir: String, pkg: String, companionSocket: String, coreSocket: String): String
}

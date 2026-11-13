package app.pinky.companion

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.os.Build
import android.util.Log
import android.widget.Toast
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest

/**
 * Installs an update the feed offers (UpdateCheck): it downloads the APK
 * from Pinky's releases (UpdateRules) straight into a PackageInstaller
 * session, checks it against the feed's sha256 and size, and only then
 * commits the session. Android asks the person to confirm (and, the first
 * time, to let Pinky install apps), and installs it as an update only when
 * Pinky's own key signed it.
 */
object UpdateInstall {

    /** Why an update could not be installed. */
    enum class Failure { OFFLINE, MISMATCH, FAILED }

    /** A download that is not the APK the feed vouched for. */
    class Mismatch(message: String) : Exception(message)

    private const val TAG = "PinkyUpdate"
    private const val ACTION = "app.pinky.companion.UPDATE_STATUS"

    @Volatile
    private var running = false

    /** Whether an update is being downloaded. */
    val downloading: Boolean get() = running

    /**
     * Downloads and checks u, then asks Android to install it. done runs
     * on a background thread with null once Android took over, or why it
     * failed. Does nothing while another download runs.
     */
    fun start(context: Context, u: UpdateCheck.Update, done: (Failure?) -> Unit) {
        if (running) return
        running = true
        val app = context.applicationContext
        CoreClient.background {
            val f = try {
                install(app, u)
                null
            } catch (e: Mismatch) {
                Log.w(TAG, "the download is not Pinky's release: ${e.message}")
                Failure.MISMATCH
            } catch (e: IOException) {
                Log.w(TAG, "could not download the update", e)
                Failure.OFFLINE
            } catch (e: Exception) {
                Log.w(TAG, "could not install the update", e)
                Failure.FAILED
            } finally {
                running = false
            }
            done(f)
        }
    }

    /** The words for a failure. */
    fun words(f: Failure): Int = when (f) {
        Failure.OFFLINE -> R.string.phone_update_download_offline
        Failure.MISMATCH -> R.string.phone_update_mismatch
        Failure.FAILED -> R.string.phone_update_install_failed
    }

    /**
     * Download on a screen: says that it started, then why it failed, if it
     * did, in a toast.
     */
    fun startFrom(context: Context, u: UpdateCheck.Update) {
        val app = context.applicationContext
        if (running) return
        Toast.makeText(app, R.string.phone_update_downloading, Toast.LENGTH_SHORT).show()
        val main = android.os.Handler(android.os.Looper.getMainLooper())
        start(app, u) { f ->
            if (f != null) main.post { Toast.makeText(app, words(f), Toast.LENGTH_LONG).show() }
        }
    }

    private fun install(context: Context, u: UpdateCheck.Update) {
        if (!UpdateRules.isReleaseApk(u.url) || !UpdateRules.isSha256(u.sha256) || !UpdateRules.sizeOk(u.size)) {
            throw Mismatch("the feed's asset is not Pinky's release")
        }
        val installer = context.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(context.packageName)
            setSize(u.size)
        }
        val id = installer.createSession(params)
        val session = installer.openSession(id)
        var committed = false
        try {
            val digest = MessageDigest.getInstance("SHA-256")
            var total = 0L
            download(u.url) { input ->
                session.openWrite("pinky.apk", 0, u.size).use { out ->
                    val buf = ByteArray(64 shl 10)
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        total += n
                        if (total > u.size) throw Mismatch("longer than the feed's ${u.size} bytes")
                        digest.update(buf, 0, n)
                        out.write(buf, 0, n)
                    }
                    session.fsync(out)
                }
            }
            if (total != u.size) throw Mismatch("$total bytes, the feed says ${u.size}")
            val sum = UpdateRules.hex(digest.digest())
            if (sum != u.sha256) throw Mismatch("sha256 $sum, the feed says ${u.sha256}")
            val intent = Intent(context, UpdateStatusReceiver::class.java).setAction(ACTION)
            // Mutable: the installer adds the status to it.
            val flags = PendingIntent.FLAG_UPDATE_CURRENT or
                (if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) PendingIntent.FLAG_MUTABLE else 0)
            val pi = PendingIntent.getBroadcast(context, id, intent, flags)
            session.commit(pi.intentSender)
            committed = true
        } finally {
            if (!committed) session.abandon()
            session.close()
        }
    }

    /** Opens url, following GitHub's redirects only to its release hosts. */
    private fun download(url: String, read: (java.io.InputStream) -> Unit) {
        var target = URL(url)
        repeat(5) {
            if (!UpdateRules.isReleaseHost(target.toString())) throw Mismatch("not a release host: ${target.host}")
            val c = target.openConnection() as HttpURLConnection
            try {
                c.instanceFollowRedirects = false
                c.connectTimeout = 15_000
                c.readTimeout = 30_000
                c.setRequestProperty("User-Agent", UpdateCheck.userAgent)
                when (val code = c.responseCode) {
                    in 300..399 -> target = URL(target, c.getHeaderField("Location") ?: throw IOException("redirect without a location"))
                    200 -> {
                        c.inputStream.use(read)
                        return
                    }
                    else -> throw IOException("the release answered $code")
                }
            } finally {
                c.disconnect()
            }
        }
        throw IOException("too many redirects")
    }
}

/**
 * Hears how the install went: Android's confirmation is opened for the
 * person; a failure shows in a toast and the log.
 */
class UpdateStatusReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                val confirm = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                    intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
                } else {
                    @Suppress("DEPRECATION")
                    intent.getParcelableExtra(Intent.EXTRA_INTENT)
                } ?: return
                confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                try {
                    context.startActivity(confirm)
                } catch (e: Exception) {
                    Log.w("PinkyUpdate", "could not ask to install the update", e)
                    Toast.makeText(context, R.string.phone_update_install_failed, Toast.LENGTH_LONG).show()
                }
            }
            PackageInstaller.STATUS_SUCCESS -> Unit
            PackageInstaller.STATUS_FAILURE_ABORTED -> Unit // the person said no
            else -> {
                Log.w("PinkyUpdate", "the update was not installed: $status ${intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE)}")
                Toast.makeText(context, R.string.phone_update_install_failed, Toast.LENGTH_LONG).show()
            }
        }
    }
}

package app.pinky.companion

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.RemoteInput
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.Log

/**
 * Pairing Pinky with the phone's own Wireless debugging, as Android's
 * "Pair device with pairing code" asks: that dialog shows a six-digit code
 * and opens a pairing port, and closes both when the person leaves it. So
 * the code goes into a notification's reply box, which the person reaches
 * without leaving the dialog, or into the background screen's own field
 * (BackgroundActivity) in split screen. The pairing port is found with
 * AdbServices; the core does the pairing (the shell command).
 *
 * Runs in the screens' process (":ui"), with PairingReceiver.
 */
object Pairing {

    private const val TAG = "PinkyPairing"
    private const val CHANNEL = "pinky_pairing"
    private const val NOTIFICATION = 7301
    const val ACTION_CODE = "app.pinky.companion.PAIRING_CODE"
    const val KEY_CODE = "code"

    /** How long Pinky looks for the pairing port after the person starts. */
    private const val LOOK_MS = 10 * 60_000L

    /** How long a code waits for the pairing port to be found. */
    private const val PORT_WAIT_MS = 10_000L

    private val main = Handler(Looper.getMainLooper())
    private var services: AdbServices? = null

    @Volatile
    private var port = 0
    private val stopLooking = Runnable { stop() }

    /** What happened to the last code, for the background screen: null while none was sent. */
    @Volatile
    var result: Result? = null
        private set

    /** Called on the main thread after each change of result. */
    var changed: (() -> Unit)? = null

    sealed class Result {
        object Pairing : Result()
        object Paired : Result()
        object WrongCode : Result()
        object NoDialog : Result() // the pairing port was not found: the dialog is not open
        class Failed(val why: String) : Result()
    }

    /**
     * Starts looking for the pairing port and posts the notification that
     * takes the code, then opens Wireless debugging for the person.
     */
    fun start(context: Context) {
        val app = context.applicationContext
        result = null
        changed?.invoke()
        port = 0
        if (services == null) {
            services = AdbServices(app, AdbServices.PAIRING) { p -> if (p > 0) port = p }
        }
        services?.start()
        main.removeCallbacks(stopLooking)
        main.postDelayed(stopLooking, LOOK_MS)
        notify(app, app.getString(R.string.phone_bg_pair_notify_body))
    }

    fun stop() {
        main.removeCallbacks(stopLooking)
        services?.stop()
        services = null
    }

    /** Whether Pinky may post the notification (Android 13 asks the person). */
    fun canNotify(context: Context): Boolean =
        context.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED &&
            context.getSystemService(NotificationManager::class.java).areNotificationsEnabled()

    /**
     * Pairs with code, off the main thread: waits for the pairing port,
     * then asks the core. The notification and result say how it went.
     */
    fun pair(context: Context, code: String, done: () -> Unit = {}) {
        val app = context.applicationContext
        result = Result.Pairing
        main.post { changed?.invoke() }
        if (services == null) main.post { start(app) }
        CoreClient.background {
            val r = try {
                val deadline = System.currentTimeMillis() + PORT_WAIT_MS
                while (port == 0 && System.currentTimeMillis() < deadline) Thread.sleep(200)
                val p = port
                if (p == 0) {
                    Result.NoDialog
                } else {
                    CoreClient.speak(app)
                    CoreClient.call(CoreClient.cmd("shell", "op" to "pair", "port" to p, "code" to code))
                    Result.Paired
                }
            } catch (e: CoreClient.Refused) {
                if (e.code == "wrong_code") Result.WrongCode else Result.Failed(e.message ?: e.code)
            } catch (e: Exception) {
                Log.w(TAG, "pairing failed", e)
                Result.Failed(e.message ?: e.toString())
            }
            result = r
            main.post {
                when (r) {
                    Result.Paired -> {
                        stop()
                        notify(app, app.getString(R.string.phone_bg_paired), reply = false)
                        main.postDelayed({ cancel(app) }, 4000)
                    }
                    Result.WrongCode -> notify(app, app.getString(R.string.phone_bg_wrong_code))
                    Result.NoDialog -> notify(app, app.getString(R.string.phone_bg_no_dialog))
                    is Result.Failed -> notify(app, app.getString(R.string.phone_bg_pair_failed, r.why))
                    Result.Pairing -> {}
                }
                changed?.invoke()
                done()
            }
        }
    }

    /** Something other than six digits was typed: ask again, without trying it. */
    fun notACode(context: Context) {
        val app = context.applicationContext
        result = Result.WrongCode
        main.post {
            notify(app, app.getString(R.string.phone_bg_wrong_code))
            changed?.invoke()
        }
    }

    fun cancel(context: Context) {
        context.getSystemService(NotificationManager::class.java).cancel(NOTIFICATION)
    }

    private fun notify(context: Context, body: String, reply: Boolean = true) {
        if (!canNotify(context)) return
        val nm = context.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL, context.getString(R.string.phone_bg_pair_channel), NotificationManager.IMPORTANCE_HIGH),
        )
        val b = Notification.Builder(context, CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_pinky)
            .setContentTitle(context.getString(R.string.phone_bg_pair_notify_title))
            .setContentText(body)
            .setStyle(Notification.BigTextStyle().bigText(body))
            .setOnlyAlertOnce(false)
            .setCategory(Notification.CATEGORY_STATUS)
            .setVisibility(Notification.VISIBILITY_PRIVATE)
            .setContentIntent(
                PendingIntent.getActivity(
                    context, 0, Intent(context, BackgroundActivity::class.java),
                    PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
                ),
            )
        if (reply) {
            val input = RemoteInput.Builder(KEY_CODE).setLabel(context.getString(R.string.phone_bg_code_hint)).build()
            // The reply box fills in the intent: it must stay mutable, and
            // it names this app's receiver, so nobody else gets it.
            val pi = PendingIntent.getBroadcast(
                context, 1,
                Intent(context, PairingReceiver::class.java).setAction(ACTION_CODE).setPackage(context.packageName),
                PendingIntent.FLAG_MUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
            b.addAction(
                Notification.Action.Builder(null, context.getString(R.string.phone_bg_enter_code), pi)
                    .addRemoteInput(input)
                    .build(),
            )
        } else {
            b.setAutoCancel(true)
        }
        nm.notify(NOTIFICATION, b.build())
    }

    /** Opens Developer options at Wireless debugging (Settings scrolls to it and highlights it). */
    fun openWirelessDebugging(look: Look) {
        look.open(
            Intent(Settings.ACTION_APPLICATION_DEVELOPMENT_SETTINGS)
                .putExtra(":settings:fragment_args_key", "toggle_adb_wireless")
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
        )
    }
}

/** Takes the pairing code typed into the notification's reply box. */
class PairingReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Pairing.ACTION_CODE) return
        val typed = RemoteInput.getResultsFromIntent(intent)?.getCharSequence(Pairing.KEY_CODE)?.toString() ?: return
        val code = BackgroundScreen.pairingCode(typed)
        if (code == null) {
            Pairing.notACode(context)
            return
        }
        val pending = goAsync()
        Pairing.pair(context, code) { pending.finish() }
    }
}

package app.pinky.companion

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.speech.RecognizerIntent
import android.util.Log
import java.io.File

/**
 * Quick ways to reach Pinky on the phone, after Mika's: the floating
 * button over other apps (QuickEntry), the Quick Settings tiles
 * (AskTileService, BubbleTileService), Share to Pinky and voice input
 * (AskActivity). This holds what they share.
 *
 * Whether the floating button is on is a file in the app's files, not a
 * preference: the service's process draws the button while Settings and
 * the tile change it from others, and SharedPreferences keep a copy per
 * process. A change is told to the service with a broadcast only this app
 * can send or receive.
 */
object QuickAccess {

    /** The broadcast that the floating button was turned on or off. */
    const val ACTION_BUBBLE = "app.pinky.companion.action.BUBBLE"

    private const val BUBBLE_FILE = "floating_button"

    /** Whether the person turned the floating button on (it is off at first). */
    fun bubbleOn(context: Context): Boolean = File(context.filesDir, BUBBLE_FILE).exists()

    fun setBubble(context: Context, on: Boolean) {
        val f = File(context.filesDir, BUBBLE_FILE)
        try {
            if (on) f.createNewFile() else f.delete()
        } catch (e: Exception) {
            Log.w(TAG, "could not turn the floating button ${if (on) "on" else "off"}", e)
        }
        context.sendBroadcast(Intent(ACTION_BUBBLE).setPackage(context.packageName))
    }

    /** Android's own speech recognizer, which records and recognizes in its app; Pinky gets the text. */
    fun voiceIntent(): Intent = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH)
        .putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
        .putExtra(RecognizerIntent.EXTRA_MAX_RESULTS, 1)

    /** Whether the phone has a speech recognizer for voiceIntent. */
    fun voiceAvailable(context: Context): Boolean =
        context.packageManager.queryIntentActivities(voiceIntent(), 0).isNotEmpty()

    /** The text the recognizer heard, from its result. */
    fun heard(data: Intent?): String =
        data?.getStringArrayListExtra(RecognizerIntent.EXTRA_RESULTS)?.firstOrNull()?.trim().orEmpty()

    /** Opens Pinky's ask panel (AskActivity) from outside an activity: the floating button or a tile. */
    fun askIntent(context: Context, screen: Boolean, voice: Boolean = false): Intent =
        Intent(context, AskActivity::class.java)
            .putExtra(AskActivity.EXTRA_SCREEN, screen)
            .putExtra(AskActivity.EXTRA_VOICE, voice)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)

    fun open(context: Context, intent: Intent) {
        try {
            context.startActivity(intent)
        } catch (e: ActivityNotFoundException) {
            Log.w(TAG, "could not open ${intent.component}", e)
        }
    }

    private const val TAG = "PinkyQuick"
}

package app.pinky.companion

import android.app.Activity
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.ImageDecoder
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.Editable
import android.text.InputType
import android.text.TextUtils
import android.text.TextWatcher
import android.util.Base64
import android.util.Log
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsets
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.HorizontalScrollView
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.io.Closeable
import kotlin.math.max

/**
 * Pinky's ask panel: a card over the app the person is in, to start a task
 * from there without opening Pinky.
 *
 * - Ask about this screen (EXTRA_SCREEN), from the floating button
 *   (QuickEntry): the task is a question about what the phone shows. The
 *   panel leaves before the task starts, so that the core's first read is
 *   the app's screen, not Pinky's (which the gateway refuses to read).
 * - Share to Pinky (the ShareToPinky alias): text, a link or pictures from
 *   another app go with the task (StartRequest's shared and images).
 * - A new task (the Quick Settings tile): the task box alone.
 *
 * EXTRA_VOICE opens Android's own speech recognizer at once (a long press
 * on the floating button); the mic button does it any time. Like Pinky's
 * other screens, the card is sensitive: agents can neither read nor press
 * it.
 */
class AskActivity : Activity() {

    private enum class Mode { SCREEN, SHARE, TASK }

    private val main = Handler(Looper.getMainLooper())
    private lateinit var look: Look
    private lateinit var settings: TaskSettings
    private lateinit var secrets: Secrets
    private lateinit var mode: Mode
    private var sub: Closeable? = null

    private lateinit var input: EditText
    private lateinit var askButton: TextView
    private lateinit var note: TextView // what came of the person's steps here
    private lateinit var whyLine: TextView // why a task cannot start now
    private lateinit var openButton: TextView
    private lateinit var pictures: LinearLayout

    private var shared = "" // text or a link shared with the task
    private val images = mutableListOf<Pair<String, ByteArray>>() // MIME and bytes, ready for the model
    private var reading = false // shared pictures are being read
    private var state: JSONObject? = null
    private var down = false
    private var starting = false
    private val created = System.currentTimeMillis()
    private val rerender = Runnable { render() }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        look = Look(this)
        settings = TaskSettings(this)
        secrets = Secrets(this)
        // Only Pinky itself opens the panel for a screen; other apps reach
        // it through the share alias, whose intents carry what they share.
        val ours = intent.component?.className == AskActivity::class.java.name
        mode = when {
            !ours -> Mode.SHARE
            intent.getBooleanExtra(EXTRA_SCREEN, false) -> Mode.SCREEN
            else -> Mode.TASK
        }
        build()
        if (mode == Mode.SHARE) readShared()
        if (savedInstanceState == null && ours && intent.getBooleanExtra(EXTRA_VOICE, false)) listen()
        render()
        main.postDelayed(rerender, GRACE_MS)
    }

    override fun onStart() {
        super.onStart()
        if (!Core.available(this)) return
        sub = CoreClient.subscribe(
            this,
            { st -> main.post { state = st; down = false; render() } },
            { main.post { down = true; render() } },
        )
    }

    override fun onStop() {
        sub?.close()
        sub = null
        super.onStop()
    }

    override fun onDestroy() {
        main.removeCallbacksAndMessages(null)
        super.onDestroy()
    }

    @Deprecated("Activity's own; the panel needs no AndroidX")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        @Suppress("DEPRECATION")
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != REQ_VOICE || resultCode != RESULT_OK) return
        val heard = QuickAccess.heard(data)
        if (heard.isEmpty()) return
        val before = input.text.toString().trimEnd()
        input.setText(if (before.isEmpty()) heard else "$before $heard")
        input.setSelection(input.text.length)
    }

    // The card.

    private fun build() {
        val card = look.card(look.bg).apply {
            setPadding(look.dp(16), look.dp(14), look.dp(16), look.dp(16))
            isClickable = true // a tap on the card is not one outside it
        }
        val title = when (mode) {
            Mode.SCREEN -> R.string.phone_ask_screen_title
            Mode.SHARE -> R.string.phone_ask_share_title
            Mode.TASK -> R.string.phone_ask_task_title
        }
        val header = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            addView(look.mark(24), LinearLayout.LayoutParams(look.dp(24), look.dp(24)).apply { marginEnd = look.dp(10) })
            addView(look.text(getString(title), Look.HEADLINE, look.ink, 600), LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        }
        card.addView(header, look.matchWrap())
        if (mode == Mode.SCREEN) {
            card.addView(look.text(getString(R.string.phone_ask_screen_note), Look.SMALL, look.ink3).apply {
                setPadding(0, look.dp(6), 0, 0)
            }, look.matchWrap())
        }
        pictures = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        if (mode == Mode.SHARE) {
            card.addView(HorizontalScrollView(this).apply {
                isHorizontalScrollBarEnabled = false
                addView(pictures)
            }, look.below(10))
        }
        input = EditText(this).apply {
            id = R.id.task_input
            hint = getString(
                when (mode) {
                    Mode.SCREEN -> R.string.phone_ask_screen_hint
                    Mode.SHARE -> R.string.phone_ask_share_hint
                    Mode.TASK -> R.string.phone_task_hint
                },
            )
            setTextColor(look.ink)
            setHintTextColor(look.ink3)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, Look.BODY)
            minLines = 2
            maxLines = 6
            gravity = Gravity.TOP or Gravity.START
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE or InputType.TYPE_TEXT_FLAG_CAP_SENTENCES
            background = look.rounded(look.groupBg, 14)
            setPadding(look.dp(14), look.dp(12), look.dp(14), look.dp(12))
            addTextChangedListener(object : TextWatcher {
                override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
                override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}
                override fun afterTextChanged(s: Editable?) = render()
            })
        }
        card.addView(input, look.below(12))
        note = look.text("", Look.SMALL, look.ink3).apply { setPadding(look.dp(4), look.dp(8), look.dp(4), 0) }
        note.say("")
        card.addView(note, look.matchWrap())
        whyLine = look.text("", Look.SMALL, look.red).apply { setPadding(look.dp(4), look.dp(8), look.dp(4), 0) }
        whyLine.say("")
        card.addView(whyLine, look.matchWrap())
        askButton = look.button(getString(if (mode == Mode.TASK) R.string.phone_start else R.string.phone_ask), Look.Kind.PRIMARY) { ask() }
        openButton = look.button(getString(R.string.phone_ask_open), Look.Kind.SECONDARY) { openPinky() }
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, look.dp(10), 0, 0)
            if (QuickAccess.voiceAvailable(this@AskActivity)) {
                addView(look.iconButton(R.drawable.ic_mic, getString(R.string.phone_voice)) { listen() })
            }
            addView(View(this@AskActivity), LinearLayout.LayoutParams(0, 1, 1f))
            addView(openButton)
            addView(askButton, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                marginStart = look.dp(8)
            })
        }
        card.addView(row, look.matchWrap())
        Look.sensitive(card)

        // The app shows through above the card; a tap there closes the panel.
        val root = FrameLayout(this).apply {
            setBackgroundColor(0x66000000)
            setOnClickListener { finish() }
            addView(card, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.BOTTOM).apply {
                val m = look.dp(8)
                setMargins(m, m, m, m)
            })
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            window.setDecorFitsSystemWindows(false)
            root.setOnApplyWindowInsetsListener { v, insets ->
                val i = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.displayCutout() or WindowInsets.Type.ime())
                v.setPadding(i.left, i.top, i.right, i.bottom)
                WindowInsets.CONSUMED
            }
        }
        setContentView(root)
        if (mode != Mode.SHARE) input.requestFocus()
    }

    /** Why a task cannot start from here now, or null when it can. */
    private fun whyNot(): String? {
        if (!Core.available(this) || !look.serviceOn()) return getString(R.string.phone_err_no_device)
        if (!settings.keyOptional() && !secrets.has(settings.keyName())) return getString(R.string.phone_no_key)
        if (settings.model().isEmpty()) return getString(R.string.phone_err_no_model)
        val st = state
        if (st == null) {
            // The core may still be starting.
            return if (down && System.currentTimeMillis() - created >= GRACE_MS) getString(R.string.phone_ask_not_ready) else ""
        }
        if (st.optJSONObject("device")?.optBoolean("ready") != true) return getString(R.string.phone_ask_not_ready)
        if (st.optJSONObject("agent")?.optBoolean("running") == true) return getString(R.string.phone_err_busy)
        if (st.optJSONObject("computer") != null) return getString(R.string.phone_err_computer)
        return null
    }

    private fun render() {
        val why = whyNot()
        val needsText = mode == Mode.TASK || (mode == Mode.SHARE && shared.isEmpty() && images.isEmpty() && !reading)
        val ready = why == null && !starting && !reading && (!needsText || input.text.isNotBlank())
        look.enable(askButton, ready)
        whyLine.say(why)
        openButton.shown(!why.isNullOrEmpty())
    }

    // What was shared.

    private fun readShared() {
        val i = intent
        val text = listOfNotNull(i.getStringExtra(Intent.EXTRA_SUBJECT), i.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString())
            .map { it.trim() }.filter { it.isNotEmpty() }.distinct().joinToString("\n")
        shared = text.take(MAX_SHARED)
        if (shared.isNotEmpty()) {
            pictures.addView(look.text(shared, Look.SMALL, look.ink2).apply {
                maxLines = 4
                ellipsize = TextUtils.TruncateAt.END
                background = look.rounded(look.groupBg, 10)
                setPadding(look.dp(12), look.dp(8), look.dp(12), look.dp(8))
                maxWidth = resources.displayMetrics.widthPixels - look.dp(64)
            })
        }
        val uris = mutableListOf<Uri>()
        if (i.action == Intent.ACTION_SEND_MULTIPLE) {
            @Suppress("DEPRECATION")
            i.getParcelableArrayListExtra<Uri>(Intent.EXTRA_STREAM)?.let { uris += it }
        } else {
            @Suppress("DEPRECATION")
            (i.getParcelableExtra<Uri>(Intent.EXTRA_STREAM))?.let { uris += it }
        }
        // Some providers do not say a stream's type: the share's own type
        // tells then. A stream that is no picture fails to decode.
        val wanted = uris.filter { (contentResolver.getType(it) ?: i.type)?.startsWith("image/") == true }
        if (wanted.size > MAX_IMAGES) {
            note.say(getString(R.string.phone_ask_first_pictures, MAX_IMAGES))
        }
        if (wanted.isEmpty()) {
            if (shared.isEmpty()) {
                note.say(getString(R.string.phone_ask_nothing))
            }
            return
        }
        reading = true
        CoreClient.background {
            var failed = false
            val read = wanted.take(MAX_IMAGES).mapNotNull { u ->
                try {
                    picture(u)
                } catch (e: Exception) {
                    Log.w(TAG, "could not read a shared picture", e)
                    failed = true
                    null
                }
            }
            main.post {
                reading = false
                for ((bmp, bytes) in read) {
                    images += "image/jpeg" to bytes
                    pictures.addView(ImageView(this).apply {
                        setImageBitmap(bmp)
                        scaleType = ImageView.ScaleType.CENTER_CROP
                        clipToOutline = true
                        background = look.rounded(look.groupBg, 10)
                    }, LinearLayout.LayoutParams(look.dp(72), look.dp(72)).apply { marginStart = look.dp(if (pictures.childCount > 0) 8 else 0) })
                }
                if (failed) {
                        note.say(getString(R.string.phone_ask_picture_failed))
                }
                render()
            }
        }
    }

    /**
     * A shared picture as the model takes it: at most MAX_SIDE pixels on its
     * longer side (more costs tokens and adds nothing), as a JPEG; and a
     * thumbnail for the card.
     */
    private fun picture(u: Uri): Pair<Bitmap, ByteArray> {
        val src = ImageDecoder.createSource(contentResolver, u)
        val bmp = ImageDecoder.decodeBitmap(src) { d, info, _ ->
            d.allocator = ImageDecoder.ALLOCATOR_SOFTWARE
            val w = info.size.width
            val h = info.size.height
            val long = max(w, h)
            if (long > MAX_SIDE) d.setTargetSize(w * MAX_SIDE / long, h * MAX_SIDE / long)
        }
        val out = ByteArrayOutputStream()
        bmp.compress(Bitmap.CompressFormat.JPEG, JPEG_QUALITY, out)
        val thumbSide = look.dp(72)
        val scale = thumbSide.toFloat() / minOf(bmp.width, bmp.height).coerceAtLeast(1)
        val thumb = Bitmap.createScaledBitmap(bmp, (bmp.width * scale).toInt().coerceAtLeast(1), (bmp.height * scale).toInt().coerceAtLeast(1), true)
        return thumb to out.toByteArray()
    }

    // Asking.

    private fun listen() {
        try {
            @Suppress("DEPRECATION")
            startActivityForResult(QuickAccess.voiceIntent(), REQ_VOICE)
        } catch (e: Exception) {
            Log.w(TAG, "no speech recognizer", e)
            note.say(getString(R.string.phone_voice_none))
        }
    }

    private fun ask() {
        if (starting || whyNot() != null) return
        val typed = input.text.toString().trim()
        val task = typed.ifEmpty {
            when (mode) {
                Mode.SCREEN -> getString(R.string.phone_ask_screen_default)
                Mode.SHARE -> getString(R.string.phone_ask_share_default)
                Mode.TASK -> return
            }
        }
        starting = true
        render()
        val id = settings.service
        val name = settings.keyName(id)
        val optional = settings.keyOptional(id)
        val app = applicationContext
        val sharedNow = shared
        val imagesNow = images.toList()
        CoreClient.background {
            // As TaskActivity: a service that needs no key starts without
            // one, unless a saved key can no longer be read.
            val key = secrets.load(name)
            if (key == null && !(optional && !secrets.has(name))) {
                main.post {
                    starting = false
                    note.say(getString(R.string.phone_err_key_lost))
                    render()
                }
                return@background
            }
            val req = settings.startRequest(task, key.orEmpty(), id)
            if (mode == Mode.SCREEN) req.put("screen", true)
            if (sharedNow.isNotEmpty()) req.put("shared", sharedNow)
            if (imagesNow.isNotEmpty()) {
                val arr = JSONArray()
                for ((mime, bytes) in imagesNow) {
                    arr.put(JSONObject().put("mime", mime).put("data", Base64.encodeToString(bytes, Base64.NO_WRAP)))
                }
                req.put("images", arr)
            }
            // The panel goes first, so that the task's first screen is the
            // app's, not Pinky's; the Working pill then shows the task.
            main.post { finish() }
            Thread.sleep(LEAVE_MS)
            val why = try {
                CoreClient.call(req)
                null
            } catch (e: CoreClient.Refused) {
                Log.w(TAG, "the core refused the task [${e.code}]: ${e.message}")
                when (e.code) {
                    "busy" -> app.getString(R.string.phone_err_busy)
                    "computer" -> app.getString(R.string.phone_err_computer)
                    "no_device" -> app.getString(R.string.phone_err_no_device)
                    else -> app.getString(R.string.phone_err_start)
                }
            } catch (e: Exception) {
                Log.w(TAG, "could not start the task", e)
                app.getString(R.string.phone_err_start)
            }
            if (why != null) main.post { Toast.makeText(app, why, Toast.LENGTH_LONG).show() }
        }
    }

    private fun openPinky() {
        look.open(Intent(this, TaskActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        finish()
    }

    companion object {
        const val EXTRA_SCREEN = "app.pinky.companion.extra.SCREEN"
        const val EXTRA_VOICE = "app.pinky.companion.extra.VOICE"
        private const val TAG = "PinkyAsk"
        private const val REQ_VOICE = 1
        private const val GRACE_MS = 3_000L
        private const val LEAVE_MS = 400L // the panel's window is gone by then (no exit animation)
        private const val MAX_SHARED = 20_000 // pilot.MaxSharedChars
        private const val MAX_IMAGES = 4 // pilot.MaxImages
        private const val MAX_SIDE = 1568 // Claude's recommended longest side
        private const val JPEG_QUALITY = 85
    }
}

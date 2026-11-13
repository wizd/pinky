package app.pinky.vdprobe

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.app.Activity
import android.app.ActivityOptions
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.pm.ActivityInfo
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.graphics.Bitmap
import android.graphics.Color
import android.graphics.Path
import android.graphics.PixelFormat
import android.hardware.display.DisplayManager
import android.hardware.display.VirtualDisplay
import android.media.ImageReader
import android.media.projection.MediaProjection
import android.media.projection.MediaProjectionManager
import android.os.Bundle
import android.os.Handler
import android.os.HandlerThread
import android.os.IBinder
import android.os.Looper
import android.util.Log
import android.view.Display
import android.view.TextureView
import android.view.WindowManager
import android.view.accessibility.AccessibilityEvent
import android.widget.TextView
import java.io.File
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

const val TAG = "VDPROBE"
const val FLAG_TRUSTED = 1 shl 7 // Display.FLAG_TRUSTED, hidden
const val FLAG_ALLOW_EMBEDDED = 0x80000000.toInt() // ActivityInfo.FLAG_ALLOW_EMBEDDED, hidden
fun log(s: String) = Log.i(TAG, s)

object State {
    var vd: VirtualDisplay? = null
    var reader: ImageReader? = null
    var mpVd: VirtualDisplay? = null
    var projection: MediaProjection? = null
    var service: ProbeService? = null
    val bg = HandlerThread("probe").apply { start() }
    val handler = Handler(bg.looper)
}

/** Accessibility service: reads windows of every display, screenshots and gestures on a display. */
class ProbeService : AccessibilityService() {
    override fun onServiceConnected() { State.service = this; log("a11y connected") }
    override fun onAccessibilityEvent(e: AccessibilityEvent?) {}
    override fun onInterrupt() {}
    override fun onDestroy() { State.service = null; super.onDestroy() }

    fun report(display: Int) {
        val all = windowsOnAllDisplays
        val ids = (0 until all.size()).map { all.keyAt(it) }
        log("a11y displays with windows: $ids")
        val ws = all.get(display)
        if (ws == null) log("a11y display $display: NOT listed")
        else for (w in ws) {
            val root = w.root
            log("a11y display $display window type=${w.type} title=${w.title} pkg=${root?.packageName} children=${root?.childCount}")
        }
        val latch = CountDownLatch(1)
        takeScreenshot(display, mainExecutor, object : TakeScreenshotCallback {
            override fun onSuccess(r: ScreenshotResult) {
                val b = Bitmap.wrapHardwareBuffer(r.hardwareBuffer, r.colorSpace)
                log("a11y takeScreenshot($display): ok ${b?.width}x${b?.height}"); r.hardwareBuffer.close(); latch.countDown()
            }
            override fun onFailure(code: Int) { log("a11y takeScreenshot($display): failed code=$code"); latch.countDown() }
        })
        State.handler.post {
            latch.await(5, TimeUnit.SECONDS)
            val p = Path().apply { moveTo(100f, 100f) }
            val g = GestureDescription.Builder().addStroke(GestureDescription.StrokeDescription(p, 0, 50)).setDisplayId(display).build()
            val ok = dispatchGesture(g, object : GestureResultCallback() {
                override fun onCompleted(d: GestureDescription?) { log("a11y gesture on $display: completed") }
                override fun onCancelled(d: GestureDescription?) { log("a11y gesture on $display: cancelled") }
            }, null)
            log("a11y dispatchGesture on $display returned $ok")
        }
    }
}

class ProjectionService : Service() {
    override fun onBind(i: Intent?): IBinder? = null
    override fun onStartCommand(intent: Intent, flags: Int, startId: Int): Int {
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel("p", "probe", NotificationManager.IMPORTANCE_LOW))
        startForeground(1, Notification.Builder(this, "p").setSmallIcon(android.R.drawable.ic_menu_view).setContentTitle("probe").build(),
            ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PROJECTION)
        val mpm = getSystemService(MediaProjectionManager::class.java)
        val data = intent.getParcelableExtra("data", Intent::class.java)!!
        val mp = mpm.getMediaProjection(intent.getIntExtra("code", 0), data)!!
        State.projection = mp
        mp.registerCallback(object : MediaProjection.Callback() { override fun onStop() { log("projection stopped") } }, State.handler)
        val r = ImageReader.newInstance(720, 1280, PixelFormat.RGBA_8888, 2)
        try {
            val vd = mp.createVirtualDisplay("pinky-probe-mp", 720, 1280, 320,
                DisplayManager.VIRTUAL_DISPLAY_FLAG_PUBLIC or DisplayManager.VIRTUAL_DISPLAY_FLAG_OWN_CONTENT_ONLY,
                r.surface, null, State.handler)
            State.mpVd = vd
            val d = vd!!.display
            log("T7 projection VD: id=${d.displayId} flags=0x${Integer.toHexString(d.flags)} trusted=${d.flags and FLAG_TRUSTED != 0} private=${d.flags and Display.FLAG_PRIVATE != 0}")
            launch(this, settingsIntent(), d.displayId, "T7 Settings on projection VD")
            launch(this, Intent(this, EmbeddedActivity::class.java), d.displayId, "T7b own allowEmbedded activity on projection VD")
        } catch (e: Exception) { log("T7 projection VD: ${e}") }
        return START_NOT_STICKY
    }
}

class EmbeddedActivity : Activity() {
    override fun onCreate(b: Bundle?) {
        super.onCreate(b)
        setContentView(TextView(this).apply { text = "EMBEDDED PROBE on display ${display?.displayId}"; textSize = 40f; setBackgroundColor(Color.YELLOW) })
        log("EmbeddedActivity created on display ${display?.displayId}")
        val target = intent.getStringExtra("then")
        if (target != null) {
            // T4: a caller already present on the display launches another app there.
            launch(this, settingsIntent(), display!!.displayId, "T4 Settings on VD from an activity on it (uid present)")
        }
    }
}

fun settingsIntent() = Intent(android.provider.Settings.ACTION_SETTINGS)

fun launch(ctx: Context, i: Intent, display: Int, what: String) {
    i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_MULTIPLE_TASK)
    val o = ActivityOptions.makeBasic().setLaunchDisplayId(display)
    try {
        ctx.startActivity(i, o.toBundle())
        log("$what: startActivity returned (no exception)")
    } catch (e: Exception) {
        log("$what: ${e.javaClass.simpleName}: ${e.message}")
    }
}

class MainActivity : Activity() {
    override fun onCreate(b: Bundle?) {
        super.onCreate(b)
        setContentView(TextView(this).apply { text = "VD probe" })
        when (intent.getStringExtra("t") ?: "basic") {
            "basic" -> basic()
            "a11y" -> State.service?.report(intent.getIntExtra("d", State.vd?.display?.displayId ?: 0)) ?: log("a11y not connected")
            "frame" -> frame()
            "mp" -> startActivityForResult(getSystemService(MediaProjectionManager::class.java).createScreenCaptureIntent(), 7)
            "overlay" -> overlay()
            "embedded" -> embeddedCount()
        }
    }

    override fun onActivityResult(req: Int, code: Int, data: Intent?) {
        log("projection consent: code=$code")
        if (code == RESULT_OK && data != null) {
            startForegroundService(Intent(this, ProjectionService::class.java).putExtra("code", code).putExtra("data", data))
        }
    }

    private fun basic() {
        val dm = getSystemService(DisplayManager::class.java)
        // T5, T6: flags an ordinary app may not ask for.
        for ((name, flag) in listOf("PUBLIC" to DisplayManager.VIRTUAL_DISPLAY_FLAG_PUBLIC, "TRUSTED" to (1 shl 10))) {
            val r = ImageReader.newInstance(720, 1280, PixelFormat.RGBA_8888, 2)
            try {
                val v = dm.createVirtualDisplay("pinky-probe-$name", 720, 1280, 320, r.surface, flag or DisplayManager.VIRTUAL_DISPLAY_FLAG_OWN_CONTENT_ONLY)
                log("T5/6 $name VD without projection: created id=${v.display.displayId} flags=0x${Integer.toHexString(v.display.flags)}")
                v.release()
            } catch (e: Exception) { log("T5/6 $name VD without projection: ${e.javaClass.simpleName}: ${e.message}") }
        }
        // T1: a private virtual display, owned by this app.
        val r = ImageReader.newInstance(720, 1280, PixelFormat.RGBA_8888, 2)
        val vd = dm.createVirtualDisplay("pinky-probe", 720, 1280, 320, r.surface, DisplayManager.VIRTUAL_DISPLAY_FLAG_OWN_CONTENT_ONLY)
        State.vd = vd; State.reader = r
        val d = vd.display
        log("T1 private VD: id=${d.displayId} flags=0x${Integer.toHexString(d.flags)} trusted=${d.flags and FLAG_TRUSTED != 0} private=${d.flags and Display.FLAG_PRIVATE != 0}")
        // T2: another app's activity.
        launch(this, settingsIntent(), d.displayId, "T2 Settings on private VD")
        launch(this, Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_APP_CALCULATOR), d.displayId, "T2b Calculator on private VD")
        // T3: our own activity that allows embedding, which then tries T4.
        launch(this, Intent(this, EmbeddedActivity::class.java).putExtra("then", "settings"), d.displayId, "T3 own allowEmbedded activity on private VD")
        // Sanity: Settings on the main display with an explicit display id.
        launch(this, settingsIntent(), Display.DEFAULT_DISPLAY, "T9 Settings on display 0")
    }

    /** Reads one frame of the private display from our own ImageReader (what a floating window would show). */
    private fun frame() {
        val img = State.reader?.acquireLatestImage() ?: run { log("frame: no image"); return }
        val p = img.planes[0]
        val bm = Bitmap.createBitmap(p.rowStride / p.pixelStride, img.height, Bitmap.Config.ARGB_8888)
        bm.copyPixelsFromBuffer(p.buffer); img.close()
        var nonBlack = 0
        for (y in 0 until bm.height step 16) for (x in 0 until bm.width step 16) if (bm.getPixel(x, y) and 0xffffff != 0) nonBlack++
        val f = File(filesDir, "frame.png"); f.outputStream().use { bm.compress(Bitmap.CompressFormat.PNG, 100, it) }
        log("frame: ${bm.width}x${bm.height} non-black samples=$nonBlack saved ${f.path}")
    }

    /** A TextureView overlay showing a second virtual display's contents: the floating window. */
    private fun overlay() {
        val wm = getSystemService(WindowManager::class.java)
        val tv = TextureView(this)
        tv.surfaceTextureListener = object : TextureView.SurfaceTextureListener {
            override fun onSurfaceTextureAvailable(st: android.graphics.SurfaceTexture, w: Int, h: Int) {
                State.vd?.surface = android.view.Surface(st)
                log("overlay: display surface switched to the floating TextureView ${w}x$h")
            }
            override fun onSurfaceTextureSizeChanged(st: android.graphics.SurfaceTexture, w: Int, h: Int) {}
            override fun onSurfaceTextureDestroyed(st: android.graphics.SurfaceTexture): Boolean = true
            override fun onSurfaceTextureUpdated(st: android.graphics.SurfaceTexture) {}
        }
        val lp = WindowManager.LayoutParams(360, 640, WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY,
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE, PixelFormat.TRANSLUCENT)
        try { wm.addView(tv, lp); log("overlay: added") } catch (e: Exception) { log("overlay: $e") }
    }

    /** How many launcher activities on this phone allow embedding. */
    private fun embeddedCount() {
        val pm = packageManager
        val acts = pm.queryIntentActivities(Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER), PackageManager.MATCH_ALL)
        var n = 0
        for (ri in acts) {
            val ai = pm.getActivityInfo(ComponentName(ri.activityInfo.packageName, ri.activityInfo.name), 0)
            val emb = ai.flags and FLAG_ALLOW_EMBEDDED != 0
            if (emb) n++
            log("launcher ${ai.packageName}/${ai.name} allowEmbedded=$emb")
        }
        log("launcher activities: ${acts.size}, allowEmbedded: $n")
    }
}

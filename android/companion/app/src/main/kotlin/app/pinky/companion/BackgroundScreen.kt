package app.pinky.companion

import java.io.EOFException
import java.io.InputStream

/**
 * The background screen (docs/notes/android-wadb.md): with the person's
 * yes, Pinky pairs with the phone's own Wireless debugging, and the core
 * runs tasks on a virtual display that only the floating window
 * (AgentWindow) shows. These are the parts the screens share, free of
 * Android so that unit tests reach them.
 */
object BackgroundScreen {

    /** The first Android whose virtual displays the feature uses: 13. */
    const val MIN_SDK = 33

    /** Where the setup stands, from the core's state.shell.status (wadb.Status). */
    enum class Status {
        OFF, // the person has not turned it on
        UNPAIRED, // on; Pinky is not paired with Wireless debugging yet
        NO_PORT, // paired; Wireless debugging is off, as far as the phone tells
        READY, // paired, and Wireless debugging is on
        CONNECTED, // Pinky holds its debugging connection now
        REFUSED, // the phone forgot Pinky's pairing
        FAILED, // the last connection failed
        ;

        companion object {
            fun of(s: String?): Status = when (s) {
                "unpaired" -> UNPAIRED
                "no_port" -> NO_PORT
                "ready" -> READY
                "connected" -> CONNECTED
                "refused" -> REFUSED
                "failed" -> FAILED
                else -> OFF
            }
        }
    }

    /** Whether the person still has to pair Pinky. */
    fun needsPairing(s: Status) = s == Status.UNPAIRED || s == Status.REFUSED

    /** One frame of the core's video command (internal/standalone/display.go). */
    class Frame(val ptsUs: Long, val config: Boolean, val keyFrame: Boolean, val data: ByteArray)

    private const val CONFIG = 1L shl 63
    private const val KEY = 1L shl 62

    /** The largest frame Pinky reads: a bad length must not take all memory. */
    const val MAX_FRAME = 8 shl 20

    /** Reads the next frame: a 12-byte header (PTS and flags, length; big-endian), then the data. */
    fun readFrame(input: InputStream): Frame {
        val h = ByteArray(12)
        readFully(input, h)
        var head = 0L
        for (i in 0 until 8) head = (head shl 8) or (h[i].toLong() and 0xff)
        var n = 0
        for (i in 8 until 12) n = (n shl 8) or (h[i].toInt() and 0xff)
        if (n < 0 || n > MAX_FRAME) throw IllegalStateException("frame of $n bytes")
        val data = ByteArray(n)
        readFully(input, data)
        return Frame(head and (CONFIG or KEY).inv(), head and CONFIG != 0L, head and KEY != 0L, data)
    }

    private fun readFully(input: InputStream, b: ByteArray) {
        var off = 0
        while (off < b.size) {
            val r = input.read(b, off, b.size - off)
            if (r < 0) throw EOFException()
            off += r
        }
    }

    /**
     * Reads one line of the control socket byte by byte, so that nothing
     * after it (the video) is taken from the stream.
     */
    fun readLine(input: InputStream): String {
        val out = java.io.ByteArrayOutputStream()
        while (true) {
            val c = input.read()
            if (c < 0) throw EOFException()
            if (c == '\n'.code) break
            out.write(c)
            if (out.size() > 1 shl 20) throw IllegalStateException("line too long")
        }
        return out.toString(Charsets.UTF_8.name())
    }

    /** A point on the floating window's picture, in the video's pixels. */
    fun toVideo(x: Float, y: Float, viewW: Int, viewH: Int, videoW: Int, videoH: Int): Pair<Int, Int> {
        if (viewW <= 0 || viewH <= 0) return 0 to 0
        val vx = (x / viewW * videoW).toInt().coerceIn(0, (videoW - 1).coerceAtLeast(0))
        val vy = (y / viewH * videoH).toInt().coerceIn(0, (videoH - 1).coerceAtLeast(0))
        return vx to vy
    }

    /** The height of a picture of width w with the video's proportions. */
    fun heightFor(w: Int, videoW: Int, videoH: Int): Int =
        if (videoW <= 0 || videoH <= 0) w * 16 / 9 else (w.toLong() * videoH / videoW).toInt()

    /** A pairing code as Wireless debugging shows it: six digits, spaces and the like dropped. */
    fun pairingCode(typed: String): String? {
        val digits = typed.filter { it.isDigit() }
        return if (digits.length == 6 && digits.length == typed.count { !it.isWhitespace() && it != '-' }) digits else null
    }
}

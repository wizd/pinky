package app.pinky.companion

import app.pinky.companion.BackgroundScreen.Status
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.EOFException
import java.nio.ByteBuffer

class BackgroundScreenTest {

    private fun frame(head: Long, data: ByteArray): ByteArray =
        ByteBuffer.allocate(12 + data.size).putLong(head).putInt(data.size).put(data).array()

    @Test
    fun readsTheCoresFrames() {
        // As internal/standalone/display.go writes them (TestWriteFrames).
        val out = ByteArrayOutputStream()
        out.write("{\"ok\":true,\"video\":{\"codec\":\"h264\",\"width\":576,\"height\":1280}}\n".toByteArray())
        out.write(frame(1L shl 63, byteArrayOf(0, 0, 0, 1, 0x67)))
        out.write(frame((1L shl 62) or 1500, byteArrayOf(0, 0, 0, 1, 0x65, 9)))
        out.write(frame(3000, byteArrayOf()))
        val input = ByteArrayInputStream(out.toByteArray())

        assertTrue(BackgroundScreen.readLine(input).contains("\"width\":576"))
        val config = BackgroundScreen.readFrame(input)
        assertTrue(config.config)
        assertFalse(config.keyFrame)
        assertEquals(0, config.ptsUs)
        assertArrayEquals(byteArrayOf(0, 0, 0, 1, 0x67), config.data)
        val key = BackgroundScreen.readFrame(input)
        assertTrue(key.keyFrame)
        assertFalse(key.config)
        assertEquals(1500, key.ptsUs)
        assertEquals(6, key.data.size)
        val empty = BackgroundScreen.readFrame(input)
        assertEquals(3000, empty.ptsUs)
        assertEquals(0, empty.data.size)
        try {
            BackgroundScreen.readFrame(input)
            fail("read past the end")
        } catch (_: EOFException) {
        }
    }

    @Test
    fun refusesHugeFrames() {
        val b = ByteBuffer.allocate(12).putLong(0).putInt(BackgroundScreen.MAX_FRAME + 1).array()
        try {
            BackgroundScreen.readFrame(ByteArrayInputStream(b))
            fail("took a frame larger than MAX_FRAME")
        } catch (_: IllegalStateException) {
        }
    }

    @Test
    fun readsAFrameInPieces() {
        val bytes = frame(7, ByteArray(100) { it.toByte() })
        // A stream that hands out three bytes at a time, like a socket may.
        val slow = object : java.io.InputStream() {
            var i = 0
            override fun read(): Int = if (i < bytes.size) bytes[i++].toInt() and 0xff else -1
            override fun read(b: ByteArray, off: Int, len: Int): Int {
                if (i >= bytes.size) return -1
                val n = minOf(3, len, bytes.size - i)
                System.arraycopy(bytes, i, b, off, n)
                i += n
                return n
            }
        }
        val f = BackgroundScreen.readFrame(slow)
        assertEquals(7, f.ptsUs)
        assertEquals(99.toByte(), f.data[99])
    }

    @Test
    fun readLineStopsAtTheNewline() {
        val input = ByteArrayInputStream("{\"ok\":false,\"code\":\"no_display\"}\nrest".toByteArray())
        assertEquals("{\"ok\":false,\"code\":\"no_display\"}", BackgroundScreen.readLine(input))
        assertEquals('r'.code, input.read())
        try {
            BackgroundScreen.readLine(ByteArrayInputStream("no newline".toByteArray()))
            fail("a line without its end")
        } catch (_: EOFException) {
        }
    }

    @Test
    fun mapsTouchesToTheVideo() {
        assertEquals(288 to 640, BackgroundScreen.toVideo(100f, 200f, 200, 400, 576, 1280))
        assertEquals(0 to 0, BackgroundScreen.toVideo(-5f, -5f, 200, 400, 576, 1280))
        assertEquals(575 to 1279, BackgroundScreen.toVideo(500f, 900f, 200, 400, 576, 1280))
        assertEquals(0 to 0, BackgroundScreen.toVideo(10f, 10f, 0, 0, 576, 1280))
    }

    @Test
    fun keepsTheVideosProportions() {
        assertEquals(400, BackgroundScreen.heightFor(180, 576, 1280))
        assertEquals(320, BackgroundScreen.heightFor(180, 0, 0)) // 16:9 before the video says
    }

    @Test
    fun takesPairingCodes() {
        assertEquals("123456", BackgroundScreen.pairingCode("123456"))
        assertEquals("123456", BackgroundScreen.pairingCode(" 123 456 "))
        assertEquals("123456", BackgroundScreen.pairingCode("123-456"))
        assertNull(BackgroundScreen.pairingCode("12345"))
        assertNull(BackgroundScreen.pairingCode("1234567"))
        assertNull(BackgroundScreen.pairingCode("12a456"))
        assertNull(BackgroundScreen.pairingCode(""))
    }

    @Test
    fun readsTheShellStatus() {
        // wadb.Status's words.
        val words = mapOf(
            "off" to Status.OFF, "unpaired" to Status.UNPAIRED, "no_port" to Status.NO_PORT, "ready" to Status.READY,
            "connected" to Status.CONNECTED, "refused" to Status.REFUSED, "failed" to Status.FAILED,
        )
        for ((w, s) in words) assertEquals(w, s, Status.of(w))
        assertEquals(Status.OFF, Status.of(null))
        assertEquals(Status.OFF, Status.of("sideways"))
        assertTrue(BackgroundScreen.needsPairing(Status.UNPAIRED))
        assertTrue(BackgroundScreen.needsPairing(Status.REFUSED))
        assertFalse(BackgroundScreen.needsPairing(Status.NO_PORT))
    }
}

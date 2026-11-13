package app.pinky.companion

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SteeringTest {

    @Test
    fun sendsOnlyTextAndOneAtATime() {
        assertTrue(Steering.canSend("tap OK instead", sending = false))
        assertFalse(Steering.canSend("", sending = false))
        assertFalse(Steering.canSend("  \n", sending = false))
        assertFalse(Steering.canSend("tap OK instead", sending = true))
    }

    @Test
    fun continuesWithTextOrWaitingMessages() {
        assertTrue(Steering.canContinue("now set an alarm", pending = 0, sending = false, computer = false))
        // Empty text sends the messages the task did not read.
        assertTrue(Steering.canContinue("", pending = 2, sending = false, computer = false))
        assertFalse(Steering.canContinue(" ", pending = 0, sending = false, computer = false))
        assertFalse(Steering.canContinue("go on", pending = 1, sending = true, computer = false))
        // Pinky on a computer holds the phone.
        assertFalse(Steering.canContinue("go on", pending = 1, sending = false, computer = true))
    }

    @Test
    fun answersTheCoresCodes() {
        assertEquals(Steering.Refusal.READ, Steering.refusal("read"))
        assertEquals(Steering.Refusal.GONE, Steering.refusal("no_message"))
        assertEquals(Steering.Refusal.ENDED, Steering.refusal("not_running"))
        assertEquals(Steering.Refusal.EMPTY, Steering.refusal("empty"))
        for (code in listOf("", "no_device", "busy", "cannot_continue", "something_new")) {
            assertEquals(code, Steering.Refusal.FAILED, Steering.refusal(code))
        }
    }

    @Test
    fun movesTextAfterWhatTheFieldHolds() {
        assertEquals("tap OK", Steering.joined("", " tap OK "))
        assertEquals("go back", Steering.joined("go back ", ""))
        assertEquals("go back tap OK", Steering.joined("go back", "tap OK"))
        assertEquals("", Steering.joined(" ", " "))
    }
}

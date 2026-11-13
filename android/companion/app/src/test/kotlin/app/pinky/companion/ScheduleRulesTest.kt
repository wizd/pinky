package app.pinky.companion

import android.view.accessibility.AccessibilityEvent
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ScheduleRulesTest {

    @Test
    fun aPhoneWithItsScreenOffOrKeyguardIsLocked() {
        assertTrue(ScheduleRules.locked(interactive = false, keyguard = false))
        assertTrue(ScheduleRules.locked(interactive = true, keyguard = true))
        assertTrue(ScheduleRules.locked(interactive = false, keyguard = true))
        assertFalse(ScheduleRules.locked(interactive = true, keyguard = false))
    }

    @Test
    fun inUseOnlyWhileUnlockedAndTouchedAMomentAgo() {
        assertTrue(ScheduleRules.inUse(true, false, 5_000))
        assertFalse(ScheduleRules.inUse(true, false, ScheduleRules.IN_USE_MS))
        assertFalse(ScheduleRules.inUse(true, false, Long.MAX_VALUE))
        // Locked is its own reason, never in use too.
        assertFalse(ScheduleRules.inUse(true, true, 5_000))
        assertFalse(ScheduleRules.inUse(false, false, 5_000))
    }

    @Test
    fun touchesAreThePersonsDoing() {
        assertTrue(ScheduleRules.touches(AccessibilityEvent.TYPE_VIEW_CLICKED))
        assertTrue(ScheduleRules.touches(AccessibilityEvent.TYPE_VIEW_SCROLLED))
        assertTrue(ScheduleRules.touches(AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED))
        assertFalse(ScheduleRules.touches(AccessibilityEvent.TYPE_NOTIFICATION_STATE_CHANGED))
        assertFalse(ScheduleRules.touches(AccessibilityEvent.TYPE_WINDOW_CONTENT_CHANGED))
        assertFalse(ScheduleRules.touches(AccessibilityEvent.TYPE_WINDOWS_CHANGED))
    }

    @Test
    fun exactAlarmOnlyWhenAllowed() {
        assertEquals(ScheduleRules.Alarm.NONE, ScheduleRules.alarm(0, true))
        assertEquals(ScheduleRules.Alarm.NONE, ScheduleRules.alarm(-1, false))
        assertEquals(ScheduleRules.Alarm.EXACT, ScheduleRules.alarm(1_800_000_000_000, true))
        assertEquals(ScheduleRules.Alarm.INEXACT, ScheduleRules.alarm(1_800_000_000_000, false))
    }

    @Test
    fun timesOfDay() {
        assertEquals("08:05", ScheduleRules.hhmm(8, 5))
        assertEquals(20 to 30, ScheduleRules.parse("20:30"))
        assertEquals(8 to 0, ScheduleRules.parse("8:00"))
        assertNull(ScheduleRules.parse("24:00"))
        assertNull(ScheduleRules.parse("08:60"))
        assertNull(ScheduleRules.parse("8"))

        assertEquals(listOf("08:00", "12:00", "20:00"), ScheduleRules.addTime(listOf("08:00", "20:00"), "12:00"))
        assertEquals(listOf("08:00"), ScheduleRules.addTime(listOf("08:00"), "08:00"))
        val full = (0 until ScheduleRules.MAX_TIMES).map { ScheduleRules.hhmm(it, 0) }
        assertEquals(full, ScheduleRules.addTime(full, "23:00"))
        assertEquals(listOf("07:00", "20:00"), ScheduleRules.changeTime(listOf("08:00", "20:00"), 0, "07:00"))
        assertEquals(listOf("20:00"), ScheduleRules.changeTime(listOf("08:00", "20:00"), 0, "20:00"))
    }

    @Test
    fun notificationIdsStayApart() {
        assertNotEquals(ScheduleRules.noticeId(1), ScheduleRules.noticeId(2))
        assertTrue(ScheduleRules.noticeId(5) in 200_000 until 300_000)
        assertTrue(ScheduleRules.needId("need-1") in 300_000 until 400_000)
        assertTrue(ScheduleRules.needId("") in 300_000 until 400_000)
    }

    @Test
    fun theEditorOffersWhatTheCoreTakes() {
        // schedule.Intervals, schedule.MaxTimes and ui/schedules.go's schedCooldowns.
        assertEquals(listOf(1, 2, 3, 4, 6, 8, 12), ScheduleRules.INTERVALS)
        assertEquals(12, ScheduleRules.MAX_TIMES)
        assertEquals(listOf(0, 60, 1440), ScheduleRules.COOLDOWNS)
        assertEquals(listOf("once", "daily", "weekdays", "weekly", "interval"), ScheduleRules.REPEATS)
    }
}

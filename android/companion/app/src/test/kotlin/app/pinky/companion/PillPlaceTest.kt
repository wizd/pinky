package app.pinky.companion

import app.pinky.companion.PillPlace.Side
import app.pinky.companion.PillPlace.Spot
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test

class PillPlaceTest {

    @Test
    fun thePillCentresOnItsAnchorAndTheDotDocksAtTheNearerEdge() {
        assertEquals(Spot(Side.CENTER, 0, 90), PillPlace.spot(0.5f, 90, 1080, collapsed = false, edge = 12))
        assertEquals(Spot(Side.CENTER, -324, 90), PillPlace.spot(0.2f, 90, 1080, collapsed = false, edge = 12))
        // The first place, at the top centre, docks right.
        assertEquals(Spot(Side.RIGHT, 12, 90), PillPlace.spot(0.5f, 90, 1080, collapsed = true, edge = 12))
        assertEquals(Spot(Side.LEFT, 12, 400), PillPlace.spot(0.3f, 400, 1080, collapsed = true, edge = 12))
        // A pill whose dot was dropped at an edge grows from there.
        assertEquals(Spot(Side.LEFT, 12, 400), PillPlace.spot(0f, 400, 1080, collapsed = false, edge = 12))
        assertEquals(Spot(Side.RIGHT, 12, 400), PillPlace.spot(1f, 400, 1080, collapsed = false, edge = 12))
        assertEquals(Spot(Side.RIGHT, 12, 400), PillPlace.spot(1f, 400, 1080, collapsed = true, edge = 12))
    }

    @Test
    fun framesStayOnTheScreen() {
        // Centred.
        assertArrayEquals(intArrayOf(340, 90, 740, 210), PillPlace.frame(Spot(Side.CENTER, 0, 90), 400, 120, 1080, 2400))
        // A pill anchored at the right edge sits flush with it.
        assertArrayEquals(intArrayOf(680, 90, 1080, 210), PillPlace.frame(Spot(Side.CENTER, 540, 90), 400, 120, 1080, 2400))
        assertArrayEquals(intArrayOf(0, 0, 400, 120), PillPlace.frame(Spot(Side.CENTER, -540, -30), 400, 120, 1080, 2400))
        // A dot at either edge.
        assertArrayEquals(intArrayOf(12, 500, 108, 596), PillPlace.frame(Spot(Side.LEFT, 12, 500), 96, 96, 1080, 2400))
        assertArrayEquals(intArrayOf(972, 500, 1068, 596), PillPlace.frame(Spot(Side.RIGHT, 12, 500), 96, 96, 1080, 2400))
        // Below the bottom, after the screen turned.
        assertArrayEquals(intArrayOf(972, 984, 1068, 1080), PillPlace.frame(Spot(Side.RIGHT, 12, 2000), 96, 96, 1080, 1080))
        // A window wider than the screen starts at its left edge.
        assertArrayEquals(intArrayOf(0, 0, 1200, 120), PillPlace.frame(Spot(Side.CENTER, 0, 0), 1200, 120, 1080, 2400))
    }

    @Test
    fun aDropSetsTheAnchor() {
        assertEquals(0.5f, PillPlace.anchorX(340, 400, 1080, collapsed = false), 0.001f)
        assertEquals(0.25f, PillPlace.anchorX(70, 400, 1080, collapsed = false), 0.001f)
        assertEquals(0f, PillPlace.anchorX(300, 96, 1080, collapsed = true), 0f)
        assertEquals(1f, PillPlace.anchorX(600, 96, 1080, collapsed = true), 0f)
        assertEquals(0.5f, PillPlace.anchorX(0, 0, 0, collapsed = false), 0f)
    }

    @Test
    fun theTopFollowsTheScreensHeightAndStaysOnIt() {
        assertEquals(98, PillPlace.top(null, 98, 120, 2400))
        assertEquals(1200, PillPlace.top(0.5f, 98, 120, 2400))
        // Turned to landscape: the same share of the shorter height.
        assertEquals(540, PillPlace.top(0.5f, 98, 120, 1080))
        assertEquals(960, PillPlace.top(0.95f, 98, 120, 1080))
        assertEquals(0, PillPlace.top(-0.1f, 98, 120, 1080))
    }
}

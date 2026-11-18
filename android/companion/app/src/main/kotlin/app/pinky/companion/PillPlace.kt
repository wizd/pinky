package app.pinky.companion

import kotlin.math.roundToInt

/**
 * Where the Working pill and its dot sit on the screen (Overlays), as plain
 * arithmetic the tests can check.
 *
 * The person's place for the pill is an anchor: the pill's centre as a
 * share of the screen's width, and its top as a share of the screen's
 * height, so that it keeps its place when the screen turns. The whole pill
 * is centred on the anchor; the dot docks at the nearer edge at the same
 * height. Both are kept inside the screen, as the window manager keeps a
 * window without FLAG_LAYOUT_NO_LIMITS.
 */
object PillPlace {

    /** The side a window's x counts from, as in its gravity. */
    enum class Side { LEFT, CENTER, RIGHT }

    /** A window's gravity and offsets, as in WindowManager.LayoutParams. */
    data class Spot(val side: Side, val x: Int, val y: Int)

    /**
     * Where the pill (or, collapsed, its dot) goes for the anchor ax and the
     * top y. A pill anchored at an edge, where a dot was dropped, grows
     * from the dot's place along that edge.
     */
    fun spot(ax: Float, y: Int, screenW: Int, collapsed: Boolean, edge: Int): Spot = when {
        collapsed && ax < 0.5f || ax <= 0f -> Spot(Side.LEFT, edge, y)
        collapsed || ax >= 1f -> Spot(Side.RIGHT, edge, y)
        else -> Spot(Side.CENTER, ((ax - 0.5f) * screenW).roundToInt(), y)
    }

    /**
     * The frame, left, top, right and bottom, a window of w by h gets at
     * spot on a screen of screenW by screenH: Gravity.apply, then kept on
     * the screen (Gravity.applyDisplay).
     */
    fun frame(spot: Spot, w: Int, h: Int, screenW: Int, screenH: Int): IntArray {
        val left = when (spot.side) {
            Side.LEFT -> spot.x
            Side.CENTER -> (screenW - w) / 2 + spot.x
            Side.RIGHT -> screenW - spot.x - w
        }.coerceIn(0, (screenW - w).coerceAtLeast(0))
        val top = spot.y.coerceIn(0, (screenH - h).coerceAtLeast(0))
        return intArrayOf(left, top, left + w, top + h)
    }

    /**
     * The anchor's x for a pill or dot dropped with its left edge at left: a
     * pill's centre, or the edge a dot keeps to.
     */
    fun anchorX(left: Int, w: Int, screenW: Int, collapsed: Boolean): Float {
        if (screenW <= 0) return 0.5f
        val centre = (left + w / 2f) / screenW
        if (collapsed) return if (centre < 0.5f) 0f else 1f
        return centre.coerceIn(0f, 1f)
    }

    /** The top for the anchor's ay (none: the first place, at fallback), kept on the screen. */
    fun top(ay: Float?, fallback: Int, h: Int, screenH: Int): Int {
        val y = if (ay == null) fallback else (ay * screenH).roundToInt()
        return y.coerceIn(0, (screenH - h).coerceAtLeast(0))
    }
}

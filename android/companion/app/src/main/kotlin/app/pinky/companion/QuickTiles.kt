package app.pinky.companion

import android.annotation.SuppressLint
import android.app.PendingIntent
import android.content.Intent
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService

/**
 * Pinky's Quick Settings tiles, which the person adds from the panel's
 * edit list.
 */

/** Opens the ask panel over the app in front, for a new task. */
class AskTileService : TileService() {

    override fun onStartListening() {
        super.onStartListening()
        qsTile?.apply {
            state = Tile.STATE_INACTIVE
            updateTile()
        }
    }

    override fun onClick() {
        super.onClick()
        val open = { open(this, QuickAccess.askIntent(this, screen = false)) }
        if (isLocked) unlockAndRun(open) else open()
    }
}

/** Turns Pinky's floating button (QuickEntry) on or off. */
class BubbleTileService : TileService() {

    override fun onStartListening() {
        super.onStartListening()
        show()
    }

    override fun onClick() {
        super.onClick()
        QuickAccess.setBubble(this, !QuickAccess.bubbleOn(this))
        show()
    }

    private fun show() {
        qsTile?.apply {
            state = if (QuickAccess.bubbleOn(this@BubbleTileService)) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE
            updateTile()
        }
    }
}

/** Opens an activity from a tile and closes the Quick Settings panel. */
@SuppressLint("StartActivityAndCollapseDeprecated")
private fun open(tile: TileService, intent: Intent) {
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
        tile.startActivityAndCollapse(
            PendingIntent.getActivity(tile, 0, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT),
        )
    } else {
        @Suppress("DEPRECATION")
        tile.startActivityAndCollapse(intent)
    }
}

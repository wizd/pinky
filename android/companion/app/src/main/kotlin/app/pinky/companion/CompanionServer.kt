package app.pinky.companion

import android.net.LocalServerSocket
import android.net.LocalSocket
import android.os.Process
import android.os.SystemClock
import android.system.Os
import android.system.OsConstants
import android.util.Log
import java.io.BufferedOutputStream
import java.io.BufferedReader
import java.io.Closeable
import java.io.IOException
import java.io.InputStreamReader
import java.util.concurrent.CopyOnWriteArraySet

/**
 * Serves the line-delimited JSON protocol on the abstract Unix socket
 * `@pinky_companion` (`@pinky_companion_dev` in the development build,
 * which may be installed beside a release). The computer reaches it with
 * `adb forward tcp:0 localabstract:pinky_companion`.
 *
 * Any app could connect to an abstract socket, so only the shell user
 * (adbd), root and this app itself are accepted.
 */
class CompanionServer(private val service: PinkyAccessibilityService) {

    val handler = RequestHandler(service, this)
    val events: EventHub get() = service.events
    private val connections = CopyOnWriteArraySet<Connection>()

    // Who drives the phone: one connection's claim. Guarded by claimLock,
    // like shown, the holder the claim events told last.
    private val claimLock = Any()
    private var claimer: Connection? = null
    private var claimTask = ""
    private var shown: Holder? = null

    /**
     * Who drives the phone: Pinky on a computer or Pinky on the phone, and
     * the task it named. implicit is an older desktop Pinky, which does not
     * claim but holds an event subscription while its agent works.
     */
    data class Holder(val by: String, val task: String, val implicit: Boolean) {
        fun write(j: Json): Json = j.field("by", by).fieldIfNotEmpty("task", task).flag("implicit", implicit)
    }

    @Volatile
    private var running = false

    @Volatile
    private var socket: LocalServerSocket? = null

    /**
     * Open connections from other apps than this one: Pinky on a computer,
     * through adb (shell or root). This app's own parts on the phone, the
     * core and its screens, are not counted.
     */
    val externalCount: Int get() = connections.count { !it.own }

    /**
     * The connections ping reports. Pinky on a computer leaves the service
     * on after a task when ping counts more than its own connection: another
     * client still uses it. This app's own connections count only while
     * on-phone mode is on, which needs the service on, so that a computer
     * leaves it alone. Once the mode is off they must not count: the core
     * stays loaded until the process ends (a Go runtime cannot be unloaded)
     * and keeps its connection, which kept the service on after every task
     * from a computer.
     */
    val pingCount: Int get() = if (service.onPhone) connections.size else externalCount

    fun start() {
        running = true
        Thread(::acceptLoop, "pinky-accept").apply {
            isDaemon = true
            start()
        }
    }

    fun stop() {
        running = false
        socket?.let { s ->
            // close() alone does not wake a thread blocked in accept() on
            // every Android version; shutting the socket down does.
            try {
                Os.shutdown(s.fileDescriptor, OsConstants.SHUT_RDWR)
            } catch (_: Exception) {
            }
            closeQuietly(s)
        }
        socket = null
        connections.forEach { it.close() }
        handler.shutdown()
    }

    fun touch() = IdleClock.touch()

    internal fun removed(c: Connection) {
        if (connections.remove(c) && !c.own) touch()
        synchronized(claimLock) {
            if (claimer === c) {
                claimer = null
                claimTask = ""
            }
            announceLocked()
        }
    }

    /** Who holds the phone now, or null when it is free. */
    val holder: Holder?
        get() = synchronized(claimLock) { holderLocked(null) }

    /**
     * Gives the phone to c for task and returns null, or returns who holds
     * it. A claim lasts until it is released or its connection ends. Claims
     * from this app's own connections take over each other's: the core that
     * reconnects keeps its claim.
     */
    fun claim(c: Connection, task: String): Holder? {
        synchronized(claimLock) {
            c.claims = true
            val mine = claimer === c || (claimer?.own == true && c.own)
            if (!mine) holderLocked(c)?.let { return it }
            claimer = c
            claimTask = task
            announceLocked()
            return null
        }
    }

    /** Ends c's claim, if it holds one. */
    fun release(c: Connection) {
        synchronized(claimLock) {
            c.claims = true
            if (claimer === c) {
                claimer = null
                claimTask = ""
            }
            announceLocked()
        }
    }

    /**
     * Ends a computer's claim for Pinky on the phone (Stop on its pill) and
     * tells the computer with stopped. False when no computer holds an
     * explicit claim: an implicit one cannot be stopped from here.
     */
    fun stopClaim(): Boolean {
        synchronized(claimLock) {
            val c = claimer ?: return false
            if (c.own) return false
            claimer = null
            claimTask = ""
            announceLocked(stopped = c)
            return true
        }
    }

    /** Called when c subscribed to events, changed its subscription or ended it. */
    internal fun subscribed(c: Connection) {
        synchronized(claimLock) { announceLocked(fresh = c) }
    }

    private fun holderLocked(except: Connection?): Holder? {
        claimer?.let { if (it !== except) return Holder(if (it.own) BY_PHONE else BY_COMPUTER, claimTask, false) }
        if (connections.any { it !== except && it.implicitClaim }) return Holder(BY_COMPUTER, "", true)
        return null
    }

    // Tells the subscribers of claim events when the holder changed; fresh,
    // a connection that just subscribed, learns it in any case.
    private fun announceLocked(fresh: Connection? = null, stopped: Connection? = null) {
        val now = holderLocked(null)
        if (now != shown || stopped != null) {
            shown = now
            events.claim(now, stopped = stopped?.subscriber)
        } else {
            fresh?.subscriber?.let { events.claim(now, only = it) }
        }
    }

    private fun acceptLoop() {
        val server = bind() ?: return
        socket = server
        if (!running) {
            closeQuietly(server)
            return
        }
        Log.i(TAG, "listening on @$SOCKET_NAME")
        try {
            while (running) {
                val s = try {
                    server.accept()
                } catch (e: IOException) {
                    if (running) Log.w(TAG, "accept failed", e)
                    break
                }
                if (!running) {
                    closeQuietly(s)
                    break
                }
                accept(s)
            }
        } finally {
            closeQuietly(server)
            Log.i(TAG, "socket closed")
        }
    }

    private fun bind(): LocalServerSocket? {
        // A previous instance may still hold the name for a moment after a
        // quick off/on cycle.
        for (attempt in 1..50) {
            if (!running) return null
            try {
                return LocalServerSocket(SOCKET_NAME)
            } catch (e: IOException) {
                if (attempt == 1) Log.w(TAG, "bind @$SOCKET_NAME failed, retrying: $e")
                Thread.sleep(100)
            }
        }
        Log.e(TAG, "could not bind @$SOCKET_NAME")
        return null
    }

    private fun accept(s: LocalSocket) {
        val uid = try {
            s.peerCredentials.uid
        } catch (_: IOException) {
            -1
        }
        if (uid != SHELL_UID && uid != ROOT_UID && uid != Process.myUid()) {
            Log.w(TAG, "rejected a connection from uid $uid")
            try {
                s.outputStream.write(FORBIDDEN)
            } catch (_: IOException) {
            }
            closeQuietly(s)
            return
        }
        val c = Connection(s, this, uid == Process.myUid())
        connections.add(c)
        if (!c.own) touch()
        Thread(c::run, "pinky-conn").apply {
            isDaemon = true
            start()
        }
    }

    companion object {
        private const val TAG = "PinkyServer"
        val SOCKET_NAME: String = BuildConfig.SOCKET_NAME

        // Holder.by: who claimed the phone.
        const val BY_PHONE = "phone"
        const val BY_COMPUTER = "computer"

        // android.os.Process.SHELL_UID and ROOT_UID are hidden APIs.
        private const val SHELL_UID = 2000
        private const val ROOT_UID = 0

        private val FORBIDDEN =
            "{\"ok\":false,\"error\":{\"code\":\"forbidden\",\"message\":\"only adb may connect\"}}\n".toByteArray()

        fun closeQuietly(c: Closeable?) {
            try {
                c?.close()
            } catch (_: IOException) {
            }
        }
    }
}

/**
 * One client connection. Requests run one at a time; events may interleave.
 * own is true for this app's own parts (the core and Pinky's screens), false
 * for adb.
 */
class Connection(private val socket: LocalSocket, private val server: CompanionServer, val own: Boolean) {
    private val out = BufferedOutputStream(socket.outputStream, 64 * 1024)
    private val writeLock = Any()

    @Volatile
    private var closed = false

    @Volatile
    var subscriber: EventHub.Subscriber? = null
        private set

    /** Set once the connection sends claim: it knows about claims. */
    @Volatile
    var claims = false

    /**
     * An older desktop Pinky does not claim, but holds an event subscription
     * while its agent works: a subscription from a computer that neither
     * asked for claim events nor sent claim counts as a claim.
     */
    val implicitClaim: Boolean
        get() = !own && !claims && subscriber?.let { EventHub.KIND_CLAIM !in it.kinds } == true

    fun run() {
        try {
            val reader = BufferedReader(InputStreamReader(socket.inputStream, Charsets.UTF_8), 64 * 1024)
            while (!closed) {
                val line = reader.readLine() ?: break
                if (line.isBlank()) continue
                if (!own) server.touch()
                val reply = server.handler.handle(line, this)
                send(reply.line)
                reply.after?.invoke()
            }
        } catch (e: IOException) {
            if (!closed) Log.d("PinkyConn", "connection ended: $e")
        } finally {
            close()
        }
    }

    /**
     * Replaces this connection's event subscription; an empty set
     * unsubscribes. A display limits it to that display's events.
     */
    fun subscribe(kinds: Set<String>, display: Int?): EventHub.Subscriber? {
        val hub = server.events
        subscriber?.let { hub.unsubscribe(it) }
        val s = if (kinds.isEmpty()) null else hub.subscribe(this, kinds, display)
        subscriber = s
        // Queued before the reply goes out, the claim event follows it.
        server.subscribed(this)
        return s
    }

    @Throws(IOException::class)
    fun send(line: String) {
        val bytes = line.toByteArray(Charsets.UTF_8)
        synchronized(writeLock) {
            out.write(bytes)
            out.write('\n'.code)
            out.flush()
        }
    }

    fun close() {
        if (closed) return
        closed = true
        subscriber?.let { server.events.unsubscribe(it) }
        subscriber = null
        // close() alone neither wakes this connection's reader nor tells the
        // client on every Android version: the core then kept a connection
        // to a stopped service (Android rebinds the service for every
        // uiautomator dump) and learnt it only when its next claim failed,
        // so the person's Start failed once. Shutting the socket down ends
        // it on both sides at once.
        try {
            Os.shutdown(socket.fileDescriptor, OsConstants.SHUT_RDWR)
        } catch (_: Exception) {
        }
        CompanionServer.closeQuietly(socket)
        server.removed(this)
    }
}

/**
 * When a client last connected, sent a request or went away, for the idle
 * watchdog. It belongs to the process rather than to a service instance:
 * Android unbinds the service and binds it again for every uiautomator
 * dump, and that must not restart the count. Elapsed realtime, so that time
 * the phone spent asleep counts too.
 */
object IdleClock {
    @Volatile
    private var last = SystemClock.elapsedRealtime()

    fun touch() {
        last = SystemClock.elapsedRealtime()
    }

    fun idleMs(): Long = SystemClock.elapsedRealtime() - last
}

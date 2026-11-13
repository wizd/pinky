import Foundation
import UserNotifications

/// The Go core (libpinkycore.a, built by ../build.sh): Pinky's gateway,
/// rules, approvals, history and agent, in whichever process hosts it.
enum PinkyCore {
    private static let lock = NSLock()
    private static var started = false

    static var isStarted: Bool {
        lock.lock()
        defer { lock.unlock() }
        return started
    }

    /// Starts the core in this process; see ios/core for the keys of
    /// config. A second start changes nothing, except that a core without
    /// a pairing connects with the one it brings (paired on the phone).
    @discardableResult
    static func start(_ config: [String: Any]) -> [String: Any] {
        lock.lock()
        defer { lock.unlock() }
        var config = config
        config["self"] = Pinky.app
        config["data_dir"] = dataDir()
        config["version"] = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""
        let json = String(data: try! JSONSerialization.data(withJSONObject: config), encoding: .utf8)!
        let out = decode(Data(withC(json) { PinkyStart($0) }.utf8))
        if out["ok"] as? Bool == true, !started {
            started = true
            DarwinNote.observe(Pinky.stopNote) { _, _, _, _, _ in PinkyStop() }
            DarwinNote.observe(Pinky.uiOnNote) { _, _, _, _, _ in PinkyUIActive(1) }
            DarwinNote.observe(Pinky.uiOffNote) { _, _, _, _, _ in PinkyUIActive(0) }
            DarwinNote.observe(Pinky.computerOnNote) { _, _, _, _, _ in PinkyComputer() }
        }
        return out
    }

    /// One request; it may block, so call it off the main thread.
    static func call(data: Data) -> Data {
        guard isStarted else { return Data(#"{"ok":false,"error":"Pinky's core is not running"}"#.utf8) }
        return Data(withC(String(data: data, encoding: .utf8) ?? "{}") { PinkyCall($0) }.utf8)
    }

    /// Starts listening for this iPhone to pair from Settings (iOS 27 and
    /// later, ios/core/pair.go): the port, and the service name and TXT
    /// record to advertise.
    static func pairStart(name: String) -> [String: Any] {
        let json = String(data: try! JSONSerialization.data(withJSONObject: ["name": name]), encoding: .utf8)!
        return decode(Data(withC(json) { PinkyPairStart($0) }.utf8))
    }

    /// What happens next in the pairing; it blocks, so call it off the
    /// main thread.
    static func pairNext() -> [String: Any] {
        guard let r = PinkyPairNext() else { return ["stopped": true] }
        defer { PinkyFree(r) }
        return decode(Data(String(cString: r).utf8))
    }

    static func pairStop() { PinkyPairStop() }

    static func decode(_ data: Data) -> [String: Any] {
        (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? ["ok": false, "error": "bad reply"]
    }

    private static func withC(_ s: String, _ f: (UnsafeMutablePointer<CChar>) -> UnsafeMutablePointer<CChar>?) -> String {
        let p = strdup(s)!
        defer { free(p) }
        guard let r = f(p) else { return "{}" }
        defer { PinkyFree(r) }
        return String(cString: r)
    }

    /// History and rules, in this process's own container.
    private static func dataDir() -> String {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first!
        let dir = base.appendingPathComponent("Pinky", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.path
    }
}

/// Turns the core's state into local notifications in the process that
/// hosts it, so they come while Pinky's app is not running: an approval to
/// answer, a task that ended. It long-polls, so it wakes only when
/// something changes. The notifications carry no actions: nothing can be
/// approved from a banner, only in Pinky's app, where the core refuses
/// input.
final class Notifier: @unchecked Sendable {
    private var seen = Set<String>()
    private var running = false

    func start() {
        Thread.detachNewThread { [self] in loop() }
    }

    private func loop() {
        var rev = 0
        while true {
            let data = PinkyCore.call(data: try! JSONSerialization.data(withJSONObject: ["cmd": "wait", "rev": rev, "timeout_ms": 55000]))
            guard let reply = try? JSONDecoder().decode(CoreReply.self, from: data), reply.ok, let st = reply.state else {
                Thread.sleep(forTimeInterval: 2)
                continue
            }
            rev = reply.rev ?? rev
            notify(st)
        }
    }

    private func notify(_ st: CoreState) {
        var open = Set<String>()
        for n in st.needs {
            open.insert(n.id)
            if seen.insert(n.id).inserted { post(id: n.id, title: L.asks, body: n.title + "\n" + L.openToAnswer, urgent: true) }
        }
        // Answered or expired: its banner would ask for nothing.
        let gone = seen.subtracting(open)
        if !gone.isEmpty {
            UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: gone.map { "pinky-" + $0 })
        }
        seen = open
        let now = st.agent?.running ?? false
        if running, !now, let a = st.agent {
            // How it ended as the title, the task and why as the body.
            let e = L.end(a)
            let body = e.reason.isEmpty ? a.task : a.task + "\n" + e.reason
            post(id: "end-\(Int(Date.now.timeIntervalSince1970))", title: e.title, body: body, urgent: false)
        }
        running = now
    }

    private func post(id: String, title: String, body: String, urgent: Bool) {
        let c = UNMutableNotificationContent()
        c.title = title
        c.body = body
        c.sound = .default
        if urgent { c.interruptionLevel = .timeSensitive }
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "pinky-" + id, content: c, trigger: nil))
    }
}

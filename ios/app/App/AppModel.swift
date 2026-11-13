import ActivityKit
import LocalAuthentication
import os
import SwiftUI
#if !NO_TUNNEL
import NetworkExtension
#endif

/// Pinky's app: the connection to this iPhone, the state of the core, and
/// what the person does with it.
@MainActor
final class AppModel: ObservableObject {
    /// The core's state, its revision, and why the phone is not ready.
    @Published private(set) var state: CoreState?
    @Published private(set) var reason: String?
    /// The VPN that reflects the phone's own connections: Pinky's own
    /// packet tunnel, or LocalDevVPN in a build without one.
    @Published private(set) var vpn = VPN.off
    /// A refused start or decide, in words, until the next action.
    @Published var problem: String?
    /// The five latest tasks that ended, for the task screen's Activity.
    @Published private(set) var recent: [PastTask] = []
    /// What the model used in the last 30 days, newest first, as the core
    /// last said (pilot.UsageRow).
    @Published private(set) var usage: [UsageRow] = []
    @Published private(set) var paired = Pairing.load() != nil
    /// Pairing from this iPhone itself (iOS 27), while it runs.
    @Published private(set) var pairStep = PhonePairing.Step.idle

    enum VPN { case off, connecting, on }

    private var rev = 0
    private var active = true
    private var loop: Task<Void, Never>?
    private let audio = SilentAudio()
    private let syslog = Logger(subsystem: Pinky.app, category: "app")
    private var addr: String?
    private var notifier: Notifier?
    private lazy var phonePairing = PhonePairing { [weak self] step, p in self?.pairChanged(step, p) }

    /// Where the core runs: in the packet-tunnel extension, which iOS keeps
    /// running while other apps are in front, or, in a build without one,
    /// in this app, which background audio keeps running during a task.
    #if NO_TUNNEL
    let inApp = true
    #else
    let inApp = false
    #endif

    /// A debug build's canned scenario (Demo): no core, no VPN.
    #if DEBUG
    let demo = Demo.scenario != nil
    #else
    let demo = false
    #endif

    init() {
        #if DEBUG
        if demo {
            paired = Demo.paired
            vpn = Demo.paired ? .on : .off
            state = Demo.state
            Task { await loadRecent() }
            return
        }
        #endif
        #if !NO_TUNNEL
        NotificationCenter.default.addObserver(forName: .NEVPNStatusDidChange, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in self?.vpnChanged() }
        }
        Task { await loadManager() }
        #else
        Task { await startInApp() }
        #endif
        listen()
    }

    // MARK: Pairing

    /// Starts pairing from this iPhone: Pinky waits for the person to pick
    /// it in Settings › Privacy & Security › Developer Mode.
    func pairOnPhone() {
        problem = nil
        phonePairing.start()
    }

    func stopPairing() {
        phonePairing.stop()
        pairStep = .idle
        keepAlive(isBusy)
    }

    private func pairChanged(_ step: PhonePairing.Step, _ p: Pairing?) {
        // A phone that looks at the advertisement and leaves is no failed try.
        if case .retry = step, pairStep == .waiting { return }
        pairStep = step
        keepAlive(isBusy)
        if let p { pairedOnPhone(p) }
    }

    /// Keeps the pairing made on the phone and connects with it: the VPN
    /// starts, or restarts so that the extension's core gets it; without a
    /// tunnel the core in the app takes it.
    private func pairedOnPhone(_ p: Pairing) {
        p.save()
        paired = true
        syslog.log("pinky paired on the phone")
        #if NO_TUNNEL
        Task {
            if PinkyCore.isStarted {
                let out = PinkyCore.start(["pairing": p.pairing, "udid": p.udid])
                if out["ok"] as? Bool != true { problem = out["error"] as? String }
            } else {
                await startInApp()
            }
        }
        #else
        if vpn == .off {
            Task { await connect() }
        } else {
            reconnect = true
            disconnect()
        }
        #endif
    }

    // MARK: Connection

    /// The address the core dials: remotepairingd through the reflector, at
    /// the port it advertises on the loopback interface (49152 is only the
    /// fallback).
    private func coreAddress() async -> String {
        if let addr { return addr }
        let found = await PairingPortFinder.find()
        found.notes.forEach { syslog.debug("pinky bonjour: \($0, privacy: .public)") }
        let a = "10.7.0.1:\(found.port ?? 49152)"
        syslog.log("pinky remotepairingd at \(a, privacy: .public)\(found.port == nil ? " (fallback)" : "", privacy: .public)")
        addr = a
        return a
    }

    #if !NO_TUNNEL
    private var manager: NETunnelProviderManager?
    /// Turn the VPN on again once it is off: the pairing changed.
    private var reconnect = false
    /// The VPN was on since it last went down, to tell when it does.
    private var wasOn = false
    /// The person turned the VPN off here.
    private var turnedOff = false
    /// When the app last turned the VPN on again by itself.
    private var revived: Date?

    private func loadManager() async {
        manager = try? await NETunnelProviderManager.loadAllFromPreferences().first
        vpnChanged()
        if manager != nil && vpn == .off && paired { await connect() }
    }

    /// Turns the VPN on: saves its configuration (iOS asks the person to
    /// allow it the first time) and starts the tunnel with the core in it.
    func connect() async {
        problem = nil
        do {
            let m = manager ?? NETunnelProviderManager()
            let proto = NETunnelProviderProtocol()
            proto.providerBundleIdentifier = Pinky.tunnel
            proto.serverAddress = String(localized: "This iPhone", comment: "The VPN's server, in Settings › VPN")
            m.protocolConfiguration = proto
            m.localizedDescription = "Pinky"
            m.isEnabled = true
            try await m.saveToPreferences()
            try await m.loadFromPreferences()
            manager = m
            var opts: [String: NSObject] = ["core": NSNumber(value: true), "addr": await coreAddress() as NSString]
            if let p = Pairing.load() {
                opts["pairing"] = p.pairing as NSString
                opts["udid"] = p.udid as NSString
            }
            #if DEBUG
            // Test runs may try another tunnel address: -iface 10.7.1.1 -mask 255.255.255.0
            for k in ["iface", "mask"] {
                if let v = UserDefaults.standard.string(forKey: k) { opts[k] = v as NSString }
            }
            // ... and have the core log its memory every N seconds: -pinkyHeartbeat 1
            let beat = UserDefaults.standard.integer(forKey: "pinkyHeartbeat")
            if beat > 0 { opts["heartbeat"] = NSNumber(value: beat) }
            #endif
            try m.connection.startVPNTunnel(options: opts)
        } catch {
            problem = error.localizedDescription
        }
    }

    func disconnect() {
        turnedOff = true
        manager?.connection.stopVPNTunnel()
    }

    /// The VPN went down while it was on and nobody here turned it off: the
    /// extension died (memory, a crash) or iOS stopped it. With an error,
    /// the app turns it on again once in ten minutes and says why; without
    /// one, the person turned it off in Settings › VPN, which stands.
    private func vpnWentDown() {
        manager?.connection.fetchLastDisconnectError { [weak self] err in
            Task { @MainActor in self?.wentDown(err) }
        }
    }

    private func wentDown(_ err: Error?) {
        guard let err, vpn == .off else { return }
        let why = err.localizedDescription
        syslog.log("pinky vpn went down: \(why, privacy: .public)")
        if let r = revived, Date.now.timeIntervalSince(r) < 600 {
            problem = L.vpnDown(why)
            return
        }
        revived = .now
        Task {
            await connect()
            if problem == nil { problem = L.vpnRevived(why) }
        }
    }

    private func vpnChanged() {
        vpn = switch manager?.connection.status ?? .invalid {
        case .connected: .on
        case .connecting, .reasserting: .connecting
        default: .off
        }
        if vpn == .on { wasOn = true }
        // Disconnected, not disconnecting: iOS has the reason only then.
        if manager?.connection.status == .disconnected {
            if reconnect {
                reconnect = false
                Task { await connect() }
            } else if wasOn && !turnedOff {
                vpnWentDown()
            }
            wasOn = false
            turnedOff = false
        }
        guard vpn == .on else { return }
        // The core in the extension that just started has not heard that
        // Pinky's app is in front.
        if active { DarwinNote.post(Pinky.uiOnNote) }
        listen()
    }
    #else
    /// Without a tunnel of its own the core runs here, and LocalDevVPN
    /// reflects the packets.
    func connect() async { await startInApp() }

    func disconnect() {}

    private func startInApp() async {
        guard !PinkyCore.isStarted else { return }
        let p = Pairing.load()
        let a = await coreAddress()
        let out = PinkyCore.start(["host": "app", "addr": a, "pairing": p?.pairing ?? "", "udid": p?.udid ?? ""])
        if out["ok"] as? Bool != true {
            problem = out["error"] as? String
            return
        }
        PinkyUIActive(active ? 1 : 0)
        vpn = .on
        let n = Notifier()
        n.start()
        notifier = n
        listen()
    }
    #endif

    /// One request to the core, wherever it runs.
    func call(_ req: [String: Any]) async -> CoreReply {
        #if DEBUG
        if demo { return Demo.reply(req) }
        #endif
        guard let out = await send(req) else { return CoreReply(ok: false, error: L.reason("vpn_off"), code: "no_device") }
        return (try? JSONDecoder().decode(CoreReply.self, from: out)) ?? CoreReply(ok: false, error: "no reply from Pinky's core")
    }

    /// A request's answer as the core wrote it; nil when the VPN that
    /// holds the core is off.
    private func send(_ req: [String: Any]) async -> Data? {
        let data = try! JSONSerialization.data(withJSONObject: req)
        if inApp {
            return await Task.detached { PinkyCore.call(data: data) }.value
        }
        #if NO_TUNNEL
        return Data()
        #else
        guard vpn == .on, let session = manager?.connection as? NETunnelProviderSession else { return nil }
        return await withCheckedContinuation { cont in
            do {
                try session.sendProviderMessage(data) { cont.resume(returning: $0 ?? Data()) }
            } catch {
                cont.resume(returning: Data())
            }
        }
        #endif
    }

    // MARK: State

    /// Keeps state current: a long poll that answers as soon as the core's
    /// state changes, so the app wakes only when there is news.
    private func listen() {
        guard loop == nil else { return }
        loop = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                let r = await self.call(["cmd": "wait", "rev": self.rev, "timeout_ms": 20000])
                guard r.ok, let st = r.state else {
                    try? await Task.sleep(for: .seconds(2))
                    continue
                }
                self.rev = r.rev ?? 0
                self.update(st, reason: r.reason)
            }
        }
    }

    private func update(_ st: CoreState, reason: String?) {
        let was = state
        state = st
        self.reason = reason
        keepAlive(isBusy)
        updateActivity(st)
        if was == nil || (was?.agent?.running == true && st.agent?.running != true) {
            Task {
                await loadRecent()
                await loadUsage()
            }
        }
        #if DEBUG
        debugRun(st)
        #endif
    }

    #if DEBUG
    /// A debug build starts a task from launch arguments once the phone is
    /// ready (README.md): -pinkyTask, and -service (or -provider), -model
    /// and -baseURL, which override the settings for this run only
    /// (TaskSettings); the key comes in the launch environment
    /// (PINKY_MODEL_KEY) and stays in memory.
    private var debugTask = UserDefaults.standard.string(forKey: "pinkyTask")

    private func debugRun(_ st: CoreState) {
        guard let t = debugTask, st.device.ready, st.agent?.running != true, st.needs.isEmpty else { return }
        debugTask = nil
        let key = ProcessInfo.processInfo.environment["PINKY_MODEL_KEY"]
        Task { await startTask(t, key: key) }
    }
    #endif

    /// Background audio keeps the app running while a task runs or waits,
    /// so the Live Activity follows it to the end. A build without the
    /// tunnel needs it for the core itself.
    /// Pairing from the phone keeps it running too, from the start, since
    /// the person goes to Settings for it.
    private func keepAlive(_ busy: Bool) {
        audio.set((busy && !active) || pairing)
    }

    private var isBusy: Bool {
        state.map { ($0.agent?.running ?? false) || !$0.needs.isEmpty } ?? false
    }

    private var pairing: Bool {
        switch pairStep {
        case .waiting, .code, .wrongCode, .retry: true
        case .idle, .failed, .paired: false
        }
    }

    // MARK: Foreground and background

    /// Tells the core whether Pinky's own app is in front: it refuses input
    /// while it is, so an agent cannot tap Approve or Stop.
    func phaseChanged(_ p: ScenePhase) {
        if demo { return }
        active = p != .background
        DarwinNote.post(active ? Pinky.uiOnNote : Pinky.uiOffNote)
        if inApp && PinkyCore.isStarted { PinkyUIActive(active ? 1 : 0) }
        keepAlive(isBusy)
        if p == .active {
            #if !NO_TUNNEL
            vpnChanged()
            #endif
            listen()
            if let st = state { updateActivity(st) }
        }
    }

    // MARK: Tasks

    /// Starts a task; `shared` is text or a link to go with it (a
    /// shortcut's, NewTaskIntent).
    func startTask(_ text: String, shared: String = "", key: String? = nil) async {
        problem = nil
        let key = key ?? TaskSettings.key(TaskSettings.service) ?? ""
        // iOS starts a Live Activity only for the app in front, and the core
        // takes Pinky off the screen before the agent's first step.
        startActivity(text)
        var req = TaskSettings.request
        req["cmd"] = "start"
        req["task"] = text
        req["key"] = key
        req["max_steps"] = TaskSettings.maxSteps
        req["max_minutes"] = TaskSettings.maxMinutes
        req["max_cost"] = TaskSettings.maxCost
        if !shared.isEmpty { req["shared"] = shared }
        let r = await call(req)
        if !r.ok {
            endActivities(nil)
            problem = L.refused(r.code, r.error)
        }
    }

    /// What became of a message to Pinky, for the task screen.
    enum Sent: Equatable {
        case ok
        /// The task ended meanwhile: the text belongs in Continue.
        case ended
        /// The model read it already: it can no longer change.
        case read
        /// Withdrawn already.
        case gone
        /// In words, for the screen.
        case failed(String)
    }

    /// Sends the person's message to the running task: the model reads it
    /// at its next step.
    func steer(_ text: String) async -> Sent {
        sent(await call(["cmd": "steer", "text": text]))
    }

    /// Changes a message the model has not read yet; empty text withdraws it.
    func editMessage(_ id: String, _ text: String) async -> Sent {
        sent(await call(["cmd": "steer", "id": id, "text": text]))
    }

    func withdraw(_ id: String) async -> Sent {
        sent(await call(["cmd": "withdraw", "id": id]))
    }

    private func sent(_ r: CoreReply) -> Sent {
        if r.ok { return .ok }
        switch r.code {
        case "not_running": return .ended
        case "read": return .read
        case "no_message": return .gone
        default: return .failed(L.notSent(r.error))
        }
    }

    /// Continues the task that ended with the person's new message (empty
    /// sends the messages left unread); its limits count anew. The key goes
    /// with it as with start. nil when the core took it, else why not.
    func continueTask(_ text: String) async -> String? {
        problem = nil
        // The key of the service the task runs on, which Settings may have
        // changed since.
        // A core of before names only the API, the name keys had then.
        let a = state?.agent
        let name = a?.key_name ?? a?.provider ?? TaskSettings.service.keyName
        var key = Keychain.get(Keychain.keyAccount(name)) ?? ""
        #if DEBUG
        // A task a debug build started from launch arguments has its key there.
        if key.isEmpty { key = ProcessInfo.processInfo.environment["PINKY_MODEL_KEY"] ?? "" }
        #endif
        startActivity(state?.agent?.task ?? text)
        let r = await call(["cmd": "continue", "text": text, "key": key])
        if r.ok { return nil }
        endActivities(nil)
        return L.continueRefused(r.code, r.error)
    }

    func stop() async {
        DarwinNote.post(Pinky.stopNote)
        _ = await call(["cmd": "stop"])
    }

    func dismiss() async {
        _ = await call(["cmd": "dismiss"])
        endActivities(nil)
    }

    /// Answers a need. Allowing asks for Face ID or the passcode first: an
    /// injected touch cannot pass that, on top of the core refusing input
    /// while Pinky is in front.
    func decide(_ n: Need, _ decision: String) async {
        problem = nil
        if decision != "deny" {
            let ctx = LAContext()
            let ok = (try? await ctx.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: n.title)) ?? false
            guard ok else { return }
        }
        let r = await call(["cmd": "decide", "need": n.id, "decision": decision])
        if !r.ok { problem = L.refused(r.code, r.error) }
    }

    func setPreset(_ p: Preset) async {
        let r = await call(["cmd": "preset", "preset": p.rawValue])
        if !r.ok { problem = L.refused(r.code, r.error) }
    }

    // MARK: The model

    /// What a check came to: the model's answer, or why the core could
    /// not check (the VPN is off, say).
    enum CheckOutcome: Equatable { case result(CheckResult), failed(String) }

    private struct CheckReply: Decodable {
        var ok: Bool
        var error: String?
        var code: String?
        var check: CheckResult?
    }

    private struct UsageReply: Decodable {
        var ok: Bool
        var usage: [UsageRow]?
    }

    /// Checks the model of the settings with one tiny request
    /// (pilot.Check), with the service's key; the core does not keep it.
    /// Takes up to a minute.
    func check() async -> CheckOutcome {
        var req = TaskSettings.request
        req["cmd"] = "check"
        req["key"] = TaskSettings.key(TaskSettings.service) ?? ""
        #if DEBUG
        if demo {
            try? await Task.sleep(for: .seconds(1))
            return .result(CheckResult(ok: true, ms: 1240))
        }
        #endif
        guard let out = await send(req) else { return .failed(L.reason("vpn_off")) }
        guard let r = try? JSONDecoder().decode(CheckReply.self, from: out) else { return .failed("no reply from Pinky's core") }
        if r.ok, let c = r.check { return .result(c) }
        return .failed(L.refused(r.code, r.error))
    }

    /// Reads what the model used in the last 30 days.
    func loadUsage() async {
        #if DEBUG
        if demo {
            usage = Demo.usage
            return
        }
        #endif
        guard let out = await send(["cmd": "usage", "since": UsageRow.day(back: 29)]),
              let r = try? JSONDecoder().decode(UsageReply.self, from: out), r.ok
        else { return }
        usage = r.usage ?? []
    }

    // MARK: Memory

    /// A notes command: the notes after it, or why the core said no.
    func memory(_ req: [String: Any]) async -> Result<[Memory], MemoryRefusal> {
        let r = await call(req)
        if r.ok { return .success(r.memories ?? []) }
        return .failure(MemoryRefusal(code: r.code))
    }

    struct MemoryRefusal: Error { var code: String? }

    /// Keeps or drops the note the task suggested at index i; nil when it
    /// went through, else why not.
    func decideNote(_ i: Int, keep: Bool) async -> String? {
        let r = await call(["cmd": keep ? "keep" : "drop", "index": i])
        return r.ok ? nil : L.memoryError(r.code)
    }

    // MARK: Activity

    func loadRecent() async {
        if let page = await history(before: nil, limit: 5) { recent = page.tasks }
    }

    /// A page of Activity, newest first: the tasks that ended before a
    /// time (Unix ms), and whether there are more; nil when the core did
    /// not answer.
    func history(before: Int64?, limit: Int = 20) async -> (tasks: [PastTask], more: Bool)? {
        let r = await call(["cmd": "history", "before_ms": before ?? 0, "limit": limit])
        guard r.ok else { return nil }
        return (r.history ?? [], r.more ?? false)
    }

    /// One task of Activity with its steps; gone when it is no longer
    /// there.
    enum PastResult { case task(PastTask), gone, failed }

    func pastTask(_ id: String) async -> PastResult {
        let r = await call(["cmd": "history", "id": id])
        if r.ok, let t = r.history?.first { return .task(t) }
        return r.code == "gone" ? .gone : .failed
    }

    // MARK: Live Activity

    private func startActivity(_ text: String) {
        endActivities(nil)
        guard ActivityAuthorizationInfo().areActivitiesEnabled else { return }
        _ = try? Activity.request(
            attributes: PinkyActivityAttributes(task: text),
            content: ActivityContent(state: .init(started: .now, line: "", waiting: false, ended: ""), staleDate: nil))
    }

    /// Follows the task: its last step, whether it waits for the person,
    /// and its end, which the activity shows for a few seconds. With no
    /// task running, an activity left from before (the app was not running
    /// when its task ended) ends now.
    private func updateActivity(_ st: CoreState) {
        let acts = Activity<PinkyActivityAttributes>.activities
        guard !acts.isEmpty else { return }
        guard let a = st.agent, a.running else {
            endActivities(st.agent.map { L.end($0).title })
            return
        }
        let waiting = st.needs.first
        let line = waiting?.title ?? st.task?.steps?.last?.text ?? ""
        for act in acts {
            let s = PinkyActivityAttributes.ContentState(started: act.content.state.started, line: line,
                                                         waiting: waiting != nil, ended: "")
            if s != act.content.state { Task { await act.update(ActivityContent(state: s, staleDate: nil)) } }
        }
    }

    private func endActivities(_ ended: String?) {
        for act in Activity<PinkyActivityAttributes>.activities {
            var s = act.content.state
            s.ended = ended ?? ""
            s.waiting = false
            Task { await act.end(ActivityContent(state: s, staleDate: nil), dismissalPolicy: ended == nil ? .immediate : .after(.now + 8)) }
        }
    }
}

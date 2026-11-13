import dnssd
import Foundation
import os
import UserNotifications

/// Pairing Pinky with this iPhone from the iPhone itself, iOS 27 and later.
/// The core listens (ios/core/pair.go); this advertises its port as a host
/// the phone may pair with, and the person picks Pinky in Settings ›
/// Privacy & Security › Developer Mode and types the code Pinky shows. The
/// code also comes as a notification, since the person is in Settings then.
final class PhonePairing: @unchecked Sendable {
    /// Where the pairing is, for the app's first screen.
    enum Step: Equatable {
        case idle
        /// Advertised; the phone has not asked for a code yet.
        case waiting
        /// The code to type on the phone.
        case code(String)
        /// The code typed did not match; the phone asks again with a new one.
        case wrongCode
        /// A try that failed for another reason; still listening.
        case retry(String)
        /// It could not start.
        case failed(String)
        case paired
    }

    /// Pairing from the phone needs iOS 27. A debug build shows its screens
    /// on older systems too, for a Simulator: -pinkyPairOnPhone YES.
    static var available: Bool {
        #if DEBUG
        if UserDefaults.standard.bool(forKey: "pinkyPairOnPhone") { return true }
        #endif
        return ProcessInfo.processInfo.isOperatingSystemAtLeast(OperatingSystemVersion(majorVersion: 27, minorVersion: 0, patchVersion: 0))
    }

    private static let codeNote = "pinky-pairing-code"
    /// What happens in a pairing, for the device's syslog: the steps and
    /// the errors, never the code.
    private static let log = Logger(subsystem: Pinky.app, category: "pairing")

    private let lock = NSLock()
    private var ref: DNSServiceRef?
    private var run = 0
    private let changed: @MainActor (Step, Pairing?) -> Void

    /// changed hears each step on the main actor, with the pairing to keep
    /// once it is paired.
    init(changed: @escaping @MainActor (Step, Pairing?) -> Void) {
        self.changed = changed
    }

    func start() {
        stop()
        let out = PinkyCore.pairStart(name: "Pinky")
        guard out["ok"] as? Bool == true, let port = out["port"] as? Int, let name = out["identifier"] as? String,
              let txt = out["txt"] as? [String: String]
        else {
            Self.log.error("pinky pairing: could not listen: \(out["error"] as? String ?? "?", privacy: .public)")
            report(.failed(out["error"] as? String ?? L.pairingFailed), nil)
            return
        }
        guard advertise(name: name, port: UInt16(port), txt: txt) else {
            Self.log.error("pinky pairing: could not advertise")
            PinkyCore.pairStop()
            report(.failed(L.pairingFailed), nil)
            return
        }
        lock.lock()
        run += 1
        let mine = run
        lock.unlock()
        Self.log.log("pinky pairing: listening on \(port, privacy: .public)")
        report(.waiting, nil)
        Thread.detachNewThread { [self] in follow(mine) }
    }

    func stop() {
        lock.lock()
        run += 1
        if let r = ref { DNSServiceRefDeallocate(r) }
        ref = nil
        lock.unlock()
        PinkyCore.pairStop()
        UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: [Self.codeNote])
    }

    /// Reads the core's pairing events until it pairs or stops.
    private func follow(_ mine: Int) {
        while true {
            let ev = PinkyCore.pairNext()
            lock.lock()
            let current = run == mine
            lock.unlock()
            guard current, ev["stopped"] as? Bool != true else { return }
            if let code = ev["pin"] as? String {
                Self.log.log("pinky pairing: the phone asked for a code")
                notify(code)
                report(.code(code), nil)
            } else if let err = ev["error"] as? String {
                Self.log.error("pinky pairing: a try failed: \(err, privacy: .public)")
                UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: [Self.codeNote])
                report(err == "wrong_pin" ? .wrongCode : .retry(err), nil)
            } else if ev["paired"] as? Bool == true, let p = ev["pairing"] as? String {
                let pairing = Pairing(udid: ev["udid"] as? String ?? "", pairing: p)
                Self.log.log("pinky pairing: paired; the phone sent \(pairing.udid.isEmpty ? "no" : "a", privacy: .public) remotepairing_udid")
                stop()
                report(.paired, pairing)
                return
            }
        }
    }

    private func report(_ step: Step, _ pairing: Pairing?) {
        let f = changed
        Task { @MainActor in f(step, pairing) }
    }

    /// Advertises the pairable host through the system's Bonjour, which
    /// needs no multicast entitlement (Info.plist lists the service type).
    private func advertise(name: String, port: UInt16, txt: [String: String]) -> Bool {
        var record = Data()
        for (k, v) in txt.sorted(by: { $0.key < $1.key }) {
            let entry = Data("\(k)=\(v)".utf8)
            guard entry.count < 256 else { continue }
            record.append(UInt8(entry.count))
            record.append(entry)
        }
        var r: DNSServiceRef?
        let err = record.withUnsafeBytes { buf in
            DNSServiceRegister(&r, 0, 0, name, rppairService, nil, nil, port.bigEndian,
                               UInt16(buf.count), buf.baseAddress, nil, nil)
        }
        guard err == kDNSServiceErr_NoError, let r else {
            Self.log.error("pinky pairing: DNSServiceRegister: \(err, privacy: .public)")
            return false
        }
        lock.lock()
        ref = r
        lock.unlock()
        return true
    }

    /// The code, as a notification the person sees in Settings.
    private func notify(_ code: String) {
        let c = UNMutableNotificationContent()
        c.title = L.pairingCodeTitle
        c.body = L.pairingCodeBody(code)
        c.sound = .default
        c.interruptionLevel = .timeSensitive
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: Self.codeNote, content: c, trigger: nil))
    }
}

/// rppair.PairableService.
private let rppairService = "_remotepairing-pairable-host._tcp"

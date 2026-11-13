import Foundation
import Network

/// Finds the port remotepairingd takes this phone's own connections on.
/// remotepairingd advertises _remotepairing._tcp on each interface it
/// listens on. An app on the phone dials the port of the loopback
/// interface's advertisement through the reflector; StikDebug does the same.
/// The network's port, 49152, is only the fallback.
final class PairingPortFinder: @unchecked Sendable {
    private let queue = DispatchQueue(label: "cc.diygod.pinky.pairing-port")
    private var browser: NWBrowser?
    private var conns: [String: NWConnection] = [:]
    private var loopbackPort: UInt16?
    private var anyPort: UInt16?
    private var notes: [String] = []
    private var cont: CheckedContinuation<Void, Never>?
    private var done = false

    /// The port, if the search found one, and what it saw, for the log.
    static func find(timeout: Double = 8) async -> (port: UInt16?, notes: [String]) {
        let f = PairingPortFinder()
        await withCheckedContinuation { c in
            f.queue.async {
                f.cont = c
                f.start()
                f.queue.asyncAfter(deadline: .now() + timeout) { f.finish("timed out") }
            }
        }
        return f.queue.sync { (f.loopbackPort ?? f.anyPort, f.notes) }
    }

    private func start() {
        let params = NWParameters.tcp
        params.includePeerToPeer = true
        let b = NWBrowser(for: .bonjour(type: "_remotepairing._tcp", domain: "local."), using: params)
        browser = b
        b.stateUpdateHandler = { [weak self] state in
            switch state {
            case .failed(let e): self?.finish("browse failed: \(e)")
            case .waiting(let e): self?.notes.append("browse waiting: \(e)")
            default: break
            }
        }
        b.browseResultsChangedHandler = { [weak self] results, _ in
            for r in results { self?.resolve(r) }
        }
        b.start(queue: queue)
    }

    /// Dials one advertisement over the interface it came on and reads the
    /// port the connection resolved to.
    private func resolve(_ r: NWBrowser.Result) {
        guard !done, case .service(let name, _, _, _) = r.endpoint else { return }
        let ifaces = r.interfaces.map(\.name)
        let key = "\(name) on \(ifaces.joined(separator: ","))"
        guard conns[key] == nil else { return }
        let loopback = ifaces.contains { $0.hasPrefix("lo") }
        notes.append("found \(key)")
        let params = NWParameters.tcp
        params.includePeerToPeer = true
        if let i = r.interfaces.first { params.requiredInterface = i }
        let c = NWConnection(to: r.endpoint, using: params)
        conns[key] = c
        c.stateUpdateHandler = { [weak self, weak c] state in
            guard let self, let c, !self.done else { return }
            switch state {
            case .ready, .waiting:
                guard case .hostPort(let host, let port) = c.currentPath?.remoteEndpoint, port.rawValue != 0 else { return }
                self.notes.append("\(key) is \(host):\(port.rawValue) (\(state))")
                if loopback {
                    self.loopbackPort = port.rawValue
                    self.finish("found the loopback port")
                } else if self.anyPort == nil {
                    self.anyPort = port.rawValue
                }
                c.cancel()
            case .failed(let e):
                self.notes.append("\(key) failed: \(e)")
            default:
                break
            }
        }
        c.start(queue: queue)
    }

    private func finish(_ why: String) {
        guard !done else { return }
        done = true
        notes.append(why)
        browser?.cancel()
        conns.values.forEach { $0.cancel() }
        cont?.resume()
        cont = nil
    }
}

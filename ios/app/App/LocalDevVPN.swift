import Darwin
import Foundation

/// LocalDevVPN, the App Store app whose VPN turns the phone's connections
/// to 10.7.0.1 back to the phone itself: the lite build, which has no
/// packet tunnel of its own, reaches this iPhone through it. Desktop Pinky
/// tells the person the same app and addresses after installing the lite
/// build (internal/driver/ios, LocalDevVPNStore).
enum LocalDevVPN {
    static let store = URL(string: "https://apps.apple.com/app/localdevvpn/id6755608044")!
    /// What to set in LocalDevVPN's Settings › Network Configuration. Its
    /// default Tunnel IP, 10.7.1.1/32, gets no answer from the phone.
    static let tunnelIP = "10.7.0.2/30"
    static let deviceIP = "10.7.0.1/32"
    /// The phone's own address in the tunnel set up so.
    private static let phoneAddress = "10.7.0.2"

    enum Status: Equatable {
        /// No tunnel: LocalDevVPN is off, or not installed.
        case off
        /// A tunnel is up with another address in 10.7.0.0/16 than Pinky
        /// needs: LocalDevVPN's Tunnel IP is still its default, say.
        case otherAddress(String)
        /// A tunnel is up with the phone at 10.7.0.2.
        case on
    }

    /// Whether LocalDevVPN's tunnel is up, by the interfaces: one that is up
    /// and holds an IPv4 address in 10.7.0.0/16, as the core looks for it
    /// (ios/core, pinky_tunnel_index). getifaddrs, as there: the sandbox
    /// lets an app read the interfaces but not the routes. Cheap enough to
    /// call every few seconds.
    static func status() -> Status {
        var list: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&list) == 0, let first = list else { return .off }
        defer { freeifaddrs(list) }
        var other: String?
        for i in sequence(first: first, next: { $0.pointee.ifa_next }) {
            guard i.pointee.ifa_flags & UInt32(IFF_UP) != 0,
                  let sa = i.pointee.ifa_addr, sa.pointee.sa_family == UInt8(AF_INET)
            else { continue }
            let a = sa.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { UInt32(bigEndian: $0.pointee.sin_addr.s_addr) }
            guard a >> 16 == (10 << 8 | 7) else { continue }
            let s = "10.7.\((a >> 8) & 0xff).\(a & 0xff)"
            if s == phoneAddress { return .on }
            other = other ?? s
        }
        return other.map { .otherAddress($0) } ?? .off
    }

    /// Why the phone is not ready, in the lite build: the core's reason
    /// (onphone.Reason*) put in LocalDevVPN's terms where it is about the
    /// VPN, else as the full build says it.
    static func reason(_ r: String?) -> String {
        switch r {
        case "vpn_off":
            String(localized: "LocalDevVPN is off. Turn it on in LocalDevVPN to let Pinky reach this iPhone.",
                   comment: "Phone not ready, in the build that relies on the App Store app LocalDevVPN")
        case "failed":
            String(localized: "Pinky cannot reach this iPhone. Check that LocalDevVPN is connected, with Tunnel IP \(tunnelIP).",
                   comment: "Phone not ready, in the build that relies on LocalDevVPN; Tunnel IP is LocalDevVPN's setting, the argument an address")
        default:
            L.reason(r)
        }
    }
}

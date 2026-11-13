import Foundation
import UserNotifications

/// The provisioning profile the app is signed with, which runs out: after
/// 7 days with a free Apple ID, a year with a paid developer account. iOS
/// then no longer opens the app, so the app warns two days ahead, on its
/// first screen and with a notification.
enum Profile {
    /// When the profile runs out; nil without one (the Simulator) or when
    /// it cannot be read.
    static let expires: Date? = {
        guard let url = Bundle.main.url(forResource: "embedded", withExtension: "mobileprovision"),
              let data = try? Data(contentsOf: url)
        else { return nil }
        return expiration(of: data)
    }()

    /// How long ahead the app warns, as desktop Pinky does for its hosts.
    static let warnAhead: TimeInterval = 2 * 24 * 3600

    /// The date to warn about, when it is near.
    static func endsSoon(_ now: Date = .now) -> Date? {
        guard let e = expires, e.timeIntervalSince(now) < warnAhead else { return nil }
        return e
    }

    /// The property list inside the profile's signed message: its
    /// signature is not checked, only ExpirationDate is read.
    static func expiration(of data: Data) -> Date? {
        guard let start = data.range(of: Data("<?xml".utf8)),
              let end = data.range(of: Data("</plist>".utf8), in: start.lowerBound..<data.endIndex),
              let plist = try? PropertyListSerialization.propertyList(from: data[start.lowerBound..<end.upperBound], format: nil)
                as? [String: Any]
        else { return nil }
        return plist["ExpirationDate"] as? Date
    }

    private static let note = "pinky-profile"

    /// Schedules the notification for two days ahead, in place of the one
    /// an earlier start scheduled (the app may have been installed again
    /// with a new profile meanwhile).
    static func remind() {
        let center = UNUserNotificationCenter.current()
        center.removePendingNotificationRequests(withIdentifiers: [note])
        guard let e = expires else { return }
        let wait = e.addingTimeInterval(-warnAhead).timeIntervalSinceNow
        guard wait > 60 else { return } // already near: the first screen says so
        let c = UNMutableNotificationContent()
        c.title = L.profileTitle
        c.body = L.profileEnds(e)
        c.sound = .default
        center.add(UNNotificationRequest(identifier: note, content: c,
                                         trigger: UNTimeIntervalNotificationTrigger(timeInterval: wait, repeats: false)))
    }
}

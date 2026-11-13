import Foundation

/// The ids and Darwin notification names the app and its extensions share.
/// The app's bundle id is the builder's choice (a free Apple ID cannot use
/// Pinky's own), so every name derives from it.
enum Pinky {
    /// The app's bundle id, also from inside an extension (<app>.tunnel,
    /// <app>.activity).
    static let app: String = {
        let id = Bundle.main.bundleIdentifier ?? "cc.diygod.pinky"
        for suffix in [".tunnel", ".activity"] where id.hasSuffix(suffix) {
            return String(id.dropLast(suffix.count))
        }
        return id
    }()

    static var tunnel: String { app + ".tunnel" }

    /// Stop the task at once (the Live Activity's Stop).
    static var stopNote: String { app + ".stop" }
    /// Pinky's app came to the front, or left it.
    static var uiOnNote: String { app + ".ui.on" }
    static var uiOffNote: String { app + ".ui.off" }
    /// The app hosts WebDriverAgent for Pinky on a computer (TestHost),
    /// said every few seconds while it does.
    static var computerOnNote: String { app + ".computer.on" }
}

/// Darwin notifications: instant, payload-free signals between the app and
/// whichever process hosts the core.
enum DarwinNote {
    static func post(_ name: String) {
        CFNotificationCenterPostNotification(
            CFNotificationCenterGetDarwinNotifyCenter(), CFNotificationName(name as CFString), nil, nil, true)
    }

    static func observe(_ name: String, _ callback: CFNotificationCallback) {
        CFNotificationCenterAddObserver(
            CFNotificationCenterGetDarwinNotifyCenter(), nil, callback, name as CFString, nil, .deliverImmediately)
    }
}

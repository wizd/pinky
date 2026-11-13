import Foundation
import os
import SwiftUI

/// Test mode: Pinky's app hosting WebDriverAgent for Pinky on a computer
/// (docs/notes/iphone-app.md, "Hosting WebDriverAgent").
///
/// Desktop Pinky builds the app with WebDriverAgent's UI test bundle in
/// PlugIns and starts the test with the app as its runner, as Xcode's
/// XCTRunner.app would be: testmanagerd launches the app with the test's
/// environment (XCTestSessionIdentifier, XCTestBundlePath), and the app
/// then does what XCTRunner does: it loads XCTest from the Developer Disk
/// Image and hands the main thread's run loop to it. WebDriverAgent runs in
/// the app's process until the computer ends the test, which kills it.
///
/// Meanwhile the app runs no task of its own and starts no core: it shows
/// that the computer is using the phone (HostingView), and tells the core,
/// when one runs in the packet tunnel, every few seconds, so that the core
/// starts no task while the computer holds the phone (ios/onphone,
/// claims.go).
enum TestHost {
    /// Whether testmanagerd launched the app for a test.
    static let active: Bool = {
        let env = ProcessInfo.processInfo.environment
        return env["XCTestSessionIdentifier"] != nil || env["XCTestBundlePath"] != nil
            || env["XCTestConfigurationFilePath"] != nil
    }()

    private static let log = Logger(subsystem: Pinky.app, category: "testhost")
    private static var started = false
    private static var beat: Timer?

    /// How often the app tells the core the computer still holds the
    /// phone; the core lets go 15 s after the last word.
    static let every: TimeInterval = 5

    /// Loads XCTest and starts the test once the app has finished
    /// launching, and tells the core about the computer from now on.
    @MainActor
    static func start() {
        guard active, !started else { return }
        started = true
        DarwinNote.post(Pinky.computerOnNote)
        beat = Timer.scheduledTimer(withTimeInterval: every, repeats: true) { _ in
            DarwinNote.post(Pinky.computerOnNote)
        }
        // XCTest takes over the main run loop: give SwiftUI its first
        // frame before. A run loop block, not the main queue: XCTest never
        // returns, and from inside a main-queue block the main queue
        // could run nothing else, so WebDriverAgent answered /status but
        // hung on /session, which waits for the main queue.
        RunLoop.main.perform(inModes: [.default]) { runXCTest() }
    }

    /// Where the Developer Disk Image puts XCTest: /System/Developer from
    /// iOS 17, /Developer before.
    private static let xctestPaths = [
        "/System/Developer/Library/Frameworks/XCTest.framework/XCTest",
        "/Developer/Library/Frameworks/XCTest.framework/XCTest",
    ]

    private static func runXCTest() {
        for path in xctestPaths {
            guard let h = dlopen(path, RTLD_NOW | RTLD_GLOBAL) else {
                log.debug("pinky testhost: no XCTest at \(path, privacy: .public)")
                continue
            }
            guard let sym = dlsym(h, "_XCTestMain") else {
                log.error("pinky testhost: XCTest at \(path, privacy: .public) has no _XCTestMain")
                return
            }
            log.log("pinky testhost: running the test with XCTest at \(path, privacy: .public)")
            // _XCTestMain takes the test configuration; nil makes XCTest
            // read it from the environment testmanagerd set, as XCTRunner
            // does. Called without one, it took whatever the first
            // argument register held (the logger just used) and aborted.
            typealias Main = @convention(c) (UnsafeRawPointer?) -> Void
            unsafeBitCast(sym, to: Main.self)(nil)
            return
        }
        log.error("pinky testhost: XCTest not found; is the Developer Disk Image mounted?")
    }
}

/// What the app shows while it hosts WebDriverAgent for the computer.
struct HostingView: View {
    var body: some View {
        VStack(spacing: 16) {
            Image(systemName: "desktopcomputer")
                .font(.system(size: 48))
                .foregroundStyle(.tint)
            Text("Pinky on your computer is using this iPhone")
                .font(.title3.weight(.semibold))
                .multilineTextAlignment(.center)
            Text("You can switch to other apps; Pinky keeps working from your computer. Tasks on this iPhone wait until the computer is done with it.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .padding(32)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

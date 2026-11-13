import ActivityKit
import AppIntents
import Foundation

/// The Live Activity that shows while Pinky works: the task, its last step
/// or what it waits for, how long it has run, and Stop.
struct PinkyActivityAttributes: ActivityAttributes {
    struct ContentState: Codable, Hashable {
        var started: Date
        /// The last step, or the title of what Pinky waits for.
        var line: String
        /// Pinky waits for the person: an approval or a handoff.
        var waiting: Bool
        /// How the task ended, for the few seconds the activity stays;
        /// empty while it runs.
        var ended: String
    }

    var task: String
}

/// Stop, from the Live Activity. It runs in the app's process, which iOS
/// starts in the background if needed; the core, wherever it runs, hears
/// the Darwin notification at once.
struct StopIntent: LiveActivityIntent {
    static var title: LocalizedStringResource = "Stop Pinky"
    static var description = IntentDescription("Stops the task Pinky is doing on this iPhone.")

    func perform() async throws -> some IntentResult {
        DarwinNote.post(Pinky.stopNote)
        #if !WIDGET_EXTENSION
        if PinkyCore.isStarted { PinkyStop() }
        #endif
        let ended = String(localized: "Stopped", comment: "Live Activity: the person stopped the task")
        for a in Activity<PinkyActivityAttributes>.activities {
            var s = a.content.state
            s.ended = ended
            s.waiting = false
            await a.end(ActivityContent(state: s, staleDate: nil), dismissalPolicy: .after(.now + 5))
        }
        return .result()
    }
}

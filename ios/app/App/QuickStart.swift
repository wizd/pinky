import AppIntents
import Foundation

/// A task that Shortcuts or Siri handed to Pinky (NewTaskIntent), waiting
/// in the task box for the person to start it.
struct Draft: Equatable {
    var task: String
    /// Text or a link the shortcut passed along, as on Android's Share to
    /// Pinky: it goes with the task (the core's `shared`).
    var shared: String
}

/// Where NewTaskIntent leaves its draft for the task screen.
@MainActor
final class Drafts: ObservableObject {
    static let shared = Drafts()
    @Published var pending: Draft?
}

/// "New task in Pinky" for Shortcuts and Siri: opens Pinky with the task
/// in its box, and with what the shortcut passed along (the share sheet's
/// text or link, for one). The person reads it and starts it; a shortcut
/// never starts a task on its own, as automations run without anyone
/// looking.
///
/// Why not a Share extension: only the app can talk to the core in the
/// tunnel (provider messages belong to the containing app), and with no
/// App Group (AltStore appends the team id to its id) an extension has no
/// way to hand the app what was shared. A shortcut that receives the share
/// sheet's input and runs this intent does the same through the app.
struct NewTaskIntent: AppIntent {
    static let title: LocalizedStringResource = "New Task in Pinky"
    static let description = IntentDescription(
        "Opens Pinky with a task in its box, and with text or a link to go with it. You start the task in Pinky.")
    static let openAppWhenRun = true

    @Parameter(title: "Task", requestValueDialog: "What should Pinky do?")
    var task: String?

    @Parameter(title: "With", description: "Text or a link for Pinky to use in the task.", inputOptions: String.IntentInputOptions(multiline: true))
    var shared: String?

    static var parameterSummary: some ParameterSummary {
        Summary("New task in Pinky: \(\.$task)") {
            \.$shared
        }
    }

    @MainActor
    func perform() async throws -> some IntentResult {
        let shared = String((shared ?? "").trimmingCharacters(in: .whitespacesAndNewlines).prefix(Self.maxShared))
        Drafts.shared.pending = Draft(task: (task ?? "").trimmingCharacters(in: .whitespacesAndNewlines), shared: shared)
        return .result()
    }

    /// pilot.MaxSharedChars.
    static let maxShared = 20_000
}

struct PinkyShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: NewTaskIntent(),
            phrases: ["New task in \(.applicationName)", "Ask \(.applicationName)"],
            shortTitle: "New Task",
            systemImageName: "text.cursor")
    }
}

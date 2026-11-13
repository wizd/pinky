import Foundation

/// A reply of the core (ios/onphone's package comment has the commands).
struct CoreReply: Decodable {
    var ok: Bool
    var error: String?
    var code: String?
    var rev: Int?
    var state: CoreState?
    var reason: String?
    var history: [PastTask]?
    var more: Bool?
    /// steer: the new message's id.
    var id: String?
    /// The notes' commands answer with the notes as they are now.
    var memories: [Memory]?
}

/// What Pinky's screens show (internal/standalone.State).
struct CoreState: Decodable, Equatable {
    var device: DeviceState
    var preset: Int
    var computer: ComputerState?
    var agent: AgentState?
    var task: TaskState?
    var needs: [Need]
}

/// Pinky on a computer while it holds the phone: no task starts meanwhile
/// (ios/onphone, claims.go).
struct ComputerState: Decodable, Equatable {
    var task: String?
    var stoppable: Bool
}

struct DeviceState: Decodable, Equatable {
    var ready: Bool
    var name: String?
    var status: String?
    var line: String?
    var detail: String?
}

struct AgentState: Decodable, Equatable {
    var task: String
    var model: String
    /// The service the task's model is at; continue needs its key.
    var provider: String?
    /// The task's model service, the name its key is saved under
    /// (pilot.KeyName) and whether it needs one: the key continue sends.
    var service: String?
    var key_name: String?
    var key_optional: Bool?
    var running: Bool
    var end: String?
    var summary: String?
    var steps: Int
    var cost: Double
    var tokens: Int
    var seconds: Int
    /// Cost is in US dollars; without it the model's prices are unknown
    /// and the cost limit is token_limit tokens.
    var priced: Bool?
    /// The task's limits, so that the screens can say which one ended it.
    var max_steps: Int?
    var max_minutes: Int?
    var max_cost: Double?
    var token_limit: Int?
    /// The person's messages the model has not read yet, oldest first:
    /// while the task runs they can still be edited or withdrawn; once it
    /// ended they wait for continue.
    var pending: [PendingMessage]?
    /// The task that ended can go on (continue), and how often it did.
    var continuable: Bool?
    var continues: Int?
    /// Notes the task suggested, for the person to keep or drop.
    var suggested: [Suggestion]?
}

/// A message of the person's to Pinky that the model has not read yet
/// (standalone.Message).
struct PendingMessage: Decodable, Equatable, Identifiable {
    var id: String
    var text: String
    var ms: Int64
}

/// A note Pinky keeps on this iPhone, never shared with a computer or
/// another phone (standalone.MemoryState).
struct Memory: Decodable, Equatable, Identifiable {
    var id: String
    /// said: the person's own; learned: how to do something in an app.
    var kind: String
    var text: String
    var app: String?

    // pilot.MaxMemories and pilot.MaxMemoryChars.
    static let most = 50
    static let maxChars = 200
}

/// A note a task suggested, until the person decides (standalone.SuggestionState).
struct Suggestion: Decodable, Equatable, Hashable {
    var kind: String
    var text: String
    var app: String?
    var kept: Bool?
    var dropped: Bool?
}

struct TaskState: Decodable, Equatable {
    var title: String
    var state: String
    var steps: [Step]?
}

struct Step: Decodable, Equatable, Hashable {
    var text: String
    var status: String
    var ms: Int
}

/// An approval or a handoff the agent waits for.
struct Need: Decodable, Equatable, Identifiable {
    var id: String
    var kind: String
    var title: String
    var detail: String?
    var app: String?
    var rule: String?
    var deadline_ms: Int64?
    var allow_task: Bool?

    var deadline: Date? { deadline_ms.map { Date(timeIntervalSince1970: Double($0) / 1000) } }
}

/// A task that ended, as Activity shows it (standalone.PastTask).
struct PastTask: Decodable, Equatable, Identifiable {
    var id: String
    var title: String
    var state: String
    /// done, stopped, failed or not_started.
    var outcome: String
    var started_ms: Int64
    var ended_ms: Int64?
    /// How the agent ended it, when the core kept a record.
    var agent: AgentState?
    /// Only in the answer for one task.
    var steps: [Step]?

    var started: Date { Date(timeIntervalSince1970: Double(started_ms) / 1000) }
}

/// When agents ask the person first (policy.Preset).
enum Preset: Int, CaseIterable, Identifiable {
    case commits = 1, payments = 2, every = 3
    var id: Int { rawValue }
}

import Foundation

/// A model service Settings offers (internal/pilot.Service): the API it
/// speaks, its address and the models to suggest. The list comes from
/// ModelServices.json, which `go generate ./internal/pilot` writes from
/// internal/pilot/services.go, so that Settings has it while the core may
/// not run.
struct ModelService: Decodable, Identifiable, Hashable {
    struct Model: Decodable, Hashable {
        var id: String
        var name: String?
        /// The model as the person reads it.
        var label: String { name.flatMap { $0.isEmpty ? nil : $0 } ?? id }
    }

    var id: String
    var name: String
    /// china or global to tell two entries of one service apart; empty
    /// when it has one.
    var region: String?
    /// anthropic or openai.
    var api: String
    /// Empty for the custom entries and for Anthropic, whose address is
    /// the API's default.
    var base_url: String
    /// The default first; empty when the service names none.
    var models: [Model]?
    var key_optional: Bool?
    var custom: Bool?
    var local: Bool?
    /// The starts of the model names whose prices Pinky knows here.
    var priced_models: [String]?

    var suggested: [Model] { models ?? [] }
    var keyOptional: Bool { key_optional ?? false }
    var isCustom: Bool { custom ?? false }
    var isLocal: Bool { local ?? false }

    /// The service as Settings names it, in the language shown.
    var label: String {
        switch id {
        case ModelService.customOpenAI: return String(localized: "OpenAI-compatible API")
        case ModelService.customAnthropic: return String(localized: "Anthropic-compatible API")
        default: break
        }
        switch region ?? "" {
        case "china":
            let r = String(localized: "China", comment: "A service's address for mainland China, after its name in Settings")
            return String(localized: "\(name) (\(r))", comment: "A service and its region in Settings: Kimi (China)")
        case "global":
            let r = String(localized: "International", comment: "A service's international address, after its name in Settings")
            return String(localized: "\(name) (\(r))", comment: "A service and its region in Settings: Kimi (China)")
        default:
            return name
        }
    }

    /// The name its API key is saved under (pilot.KeyName): its id, or the
    /// API's name for the two custom entries, as all keys were before
    /// there were services.
    var keyName: String { isCustom ? api : id }

    /// The model used when none is chosen: the first suggested, else Claude
    /// Opus for an Anthropic API that is no local server.
    var defaultModel: String {
        if let m = suggested.first { return m.id }
        return api == TaskSettings.anthropic && !isLocal ? TaskSettings.defaultModel : ""
    }

    /// Whether Pinky knows the prices of a model here.
    func priced(_ model: String) -> Bool {
        !model.isEmpty && (priced_models ?? []).contains { model.hasPrefix($0) }
    }

    /// A model as the person reads it: the name the service gives it, or
    /// its id.
    func modelName(_ model: String) -> String {
        suggested.first { $0.id == model }?.label ?? model
    }

    static let customOpenAI = "openai-compatible"
    static let customAnthropic = "anthropic-compatible"

    private struct File: Decodable { var services: [ModelService] }

    /// The services, in Settings' order.
    static let all: [ModelService] = {
        guard let url = Bundle.main.url(forResource: "ModelServices", withExtension: "json"),
              let data = try? Data(contentsOf: url),
              let f = try? JSONDecoder().decode(File.self, from: data), !f.services.isEmpty
        else {
            // Never in a build that bundles the list; keeps Settings working.
            return [
                ModelService(id: customAnthropic, name: "Anthropic-compatible", api: TaskSettings.anthropic, base_url: "", custom: true),
                ModelService(id: customOpenAI, name: "OpenAI-compatible", api: TaskSettings.openai, base_url: "", custom: true),
            ]
        }
        return f.services
    }()

    static func byID(_ id: String) -> ModelService? { all.first { $0.id == id } }

    /// The service of settings saved before there were services
    /// (pilot.ServiceFor): Anthropic's or OpenAI's own at the API's default
    /// address, else the custom entry for that API.
    static func serviceFor(provider: String, baseURL: String) -> String {
        let base = TaskSettings.cleanURL(baseURL)
        if provider.trimmingCharacters(in: .whitespaces).lowercased() == TaskSettings.openai {
            if (base.isEmpty || base == "https://api.openai.com/v1") && byID(TaskSettings.openai) != nil {
                return TaskSettings.openai
            }
            return customOpenAI
        }
        return base.isEmpty ? TaskSettings.anthropic : customAnthropic
    }
}

/// How a model answered a check (pilot.CheckResult).
struct CheckResult: Decodable, Equatable {
    var ok: Bool
    /// One of no_model, no_key, bad_key, no_credit, not_found, busy,
    /// rejected, no_tools, unreachable, bad_answer; empty when ok.
    var problem: String?
    /// The service's own words, or the network's; never the key.
    var detail: String?
    /// The HTTP status the service answered with, 0 for none.
    var status: Int?
    var ms: Int64?

    /// How the check went, in the language shown.
    var text: String {
        switch problem ?? "" {
        case "":
            let s = String(format: "%.1f", locale: Locale(identifier: "en_US_POSIX"), Double(ms ?? 0) / 1000)
            return String(localized: "The model answered and used a tool, in \(s) s.", comment: "A check of the model went well; seconds")
        case "no_model": return String(localized: "Choose a model first.")
        case "no_key": return String(localized: "Add an API key first.")
        case "bad_key": return String(localized: "The service didn't accept the API key. Check that it is a key of this service.")
        case "no_credit": return String(localized: "The service says the account has no credit left. Top it up on the service's site.")
        case "not_found": return String(localized: "The service doesn't know this model or address. Check the model's name and the API address.")
        case "busy": return String(localized: "The service is busy or limited the request. Try again in a moment.")
        case "no_tools": return String(localized: "The model answered but didn't use a tool. Pinky works through tools, so choose a model that uses them.")
        case "unreachable": return String(localized: "Pinky couldn't reach the API address. Check it, and that the service or server is running.")
        case "bad_answer": return String(localized: "The address answered, but not as an API Pinky knows. Check the API address.")
        default: return String(localized: "The service refused the request.")
        }
    }

    /// What the service said of a failed check, with its HTTP status; nil
    /// when it said nothing worth showing.
    var said: String? {
        let d = detail ?? "", st = status ?? 0
        if ok || d.isEmpty || (problem == "no_tools" && st == 0) { return nil }
        let what = st != 0 ? "HTTP \(st) · \(d)" : d
        return String(localized: "The service said: \(what)", comment: "Under a failed check: the service's own words")
    }
}

/// What one model of one service used on one day (pilot.UsageRow).
struct UsageRow: Decodable, Equatable {
    var day: String
    var service: String
    var host: String?
    var model: String
    var requests: Int
    var input: Int64
    var output: Int64
    var cache_read: Int64?
    var cache_write: Int64?
    var cost: Double?
    var unpriced: Int64?

    /// Every token the row counts, sent and received.
    var tokens: Int64 { input + output + (cache_read ?? 0) + (cache_write ?? 0) }

    /// The service and model: "DeepSeek · deepseek-flash".
    var who: String {
        let svc = ModelService.byID(service)
        var w = host ?? ""
        if let svc, w.isEmpty || !svc.isCustom { w = svc.label }
        if w.isEmpty { w = service }
        return w + " · " + (svc?.modelName(model) ?? model)
    }

    /// The day as a date, in local time.
    var date: Date? { UsageRow.dayFormat.date(from: day) }

    static let dayFormat: DateFormatter = {
        let f = DateFormatter()
        f.calendar = Calendar(identifier: .gregorian)
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = .current
        f.dateFormat = "yyyy-MM-dd"
        return f
    }()

    /// The day `back` days before today, as rows name days.
    static func day(back: Int) -> String {
        let d = Calendar.current.date(byAdding: .day, value: -back, to: .now) ?? .now
        return dayFormat.string(from: d)
    }

    /// The total of the rows from a day on.
    struct Total { var tokens: Int64 = 0, cost = 0.0, unpriced: Int64 = 0, requests = 0 }

    static func total(_ rows: [UsageRow], since: String) -> Total {
        var t = Total()
        for r in rows where r.day >= since {
            t.tokens += r.tokens
            t.cost += r.cost ?? 0
            t.unpriced += r.unpriced ?? 0
            t.requests += r.requests
        }
        return t
    }
}

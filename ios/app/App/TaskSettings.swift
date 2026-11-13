import Foundation

/// What a task starts with: the service, its model and address, and the
/// limits, kept in UserDefaults (the keys stay in the Keychain). The same
/// choices and defaults as the Android app's TaskSettings and desktop
/// Pinky's (ui/run.go, PilotConfig).
///
/// Each service keeps its own model ("model.<id>") and address
/// ("baseURL.<id>"; empty follows the service's own). Settings saved
/// before there were services ("provider", "model", "baseURL") still
/// count until the person chooses again: they name the service, and the
/// model and address of their API's entries.
enum TaskSettings {
    static let anthropic = "anthropic"
    static let openai = "openai"
    static let defaultModel = "claude-opus-5-5"

    static let stepChoices = [25, 50, 100, 200, 500]
    static let minuteChoices = [5, 10, 20, 30, 60]
    static let costChoices = [0.5, 1.0, 2.0, 5.0, 10.0]

    static let defaultSteps = 100
    static let defaultMinutes = 20
    static let defaultCost = 2.0

    private static var d: UserDefaults { .standard }

    // MARK: Settings of before there were services

    private static var legacyProvider: String {
        (d.string(forKey: "provider") ?? anthropic).trimmingCharacters(in: .whitespaces).lowercased() == openai ? openai : anthropic
    }
    private static var legacyModel: String { d.string(forKey: "model") ?? "" }
    private static var legacyURL: String { cleanURL(d.string(forKey: "baseURL") ?? "") }

    /// Launch arguments that override the settings for one run (a debug
    /// build's -pinkyTask, README.md): -service, or -provider and -baseURL
    /// for the service of before; -model; -baseURL.
    private static var launch: [String: Any] { d.volatileDomain(forName: UserDefaults.argumentDomain) }

    // MARK: The service

    /// The id of the service chosen: the one saved, else the one the
    /// settings of before name (pilot.ServiceFor). One this version does
    /// not know falls back to those as well.
    static var serviceID: String {
        let args = launch
        if let s = args["service"] as? String, ModelService.byID(s) != nil { return s }
        if args["provider"] != nil || args["baseURL"] != nil {
            return ModelService.serviceFor(provider: args["provider"] as? String ?? legacyProvider,
                                           baseURL: args["baseURL"] as? String ?? "")
        }
        if let s = d.string(forKey: "service"), ModelService.byID(s) != nil { return s }
        return ModelService.serviceFor(provider: legacyProvider, baseURL: legacyURL)
    }

    static var service: ModelService {
        ModelService.byID(serviceID) ?? ModelService.byID(ModelService.customAnthropic) ?? ModelService.all[0]
    }

    static func setService(_ id: String) { d.set(id, forKey: "service") }

    // MARK: Model and address, per service

    /// The model saved for a service: its own, else the one saved for its
    /// API before there were services; nil when neither is.
    static func savedModel(_ svc: ModelService) -> String? {
        if let m = d.string(forKey: "model." + svc.id) { return m }
        switch svc.id {
        case anthropic, ModelService.customAnthropic:
            return legacyProvider == anthropic ? legacyModel : nil
        case openai, ModelService.customOpenAI:
            return legacyProvider == openai ? legacyModel : nil
        default:
            return nil
        }
    }

    /// The model a service uses: the one saved, else its default.
    static func model(_ svc: ModelService) -> String {
        let m = (savedModel(svc) ?? "").trimmingCharacters(in: .whitespaces)
        return m.isEmpty ? svc.defaultModel : m
    }

    static func setModel(_ svc: ModelService, _ model: String) {
        d.set(model.trimmingCharacters(in: .whitespacesAndNewlines), forKey: "model." + svc.id)
    }

    /// The address the person set for a service; empty follows the
    /// service's own. Only the custom entries take the address of before.
    static func savedURL(_ svc: ModelService) -> String {
        if let u = d.string(forKey: "baseURL." + svc.id) { return u }
        if svc.isCustom && svc.api == legacyProvider { return legacyURL }
        return ""
    }

    /// Saves an address; the service's own is saved as empty, so that
    /// the service's address is followed when it changes.
    static func setURL(_ svc: ModelService, _ url: String) {
        let u = cleanURL(url)
        d.set(u == svc.base_url ? "" : u, forKey: "baseURL." + svc.id)
    }

    /// The model a task uses.
    static var model: String {
        if let m = launch["model"] as? String, !m.trimmingCharacters(in: .whitespaces).isEmpty {
            return m.trimmingCharacters(in: .whitespaces)
        }
        return model(service)
    }

    /// The address a task sends: the one set, or empty for the service's.
    static var baseURL: String {
        if let u = launch["baseURL"] as? String { return cleanURL(u) }
        return savedURL(service)
    }

    // MARK: Key

    /// The Keychain account of a service's API key.
    static func keyAccount(_ svc: ModelService) -> String { Keychain.keyAccount(svc.keyName) }

    static func key(_ svc: ModelService) -> String? {
        let k = Keychain.get(keyAccount(svc)) ?? ""
        return k.isEmpty ? nil : k
    }

    /// Whether a task may start as far as the key goes: one is saved, or
    /// the service needs none.
    static var keyReady: Bool { service.keyOptional || key(service) != nil }

    /// What the core's start and check commands take of the settings
    /// (standalone.StartRequest), without the key.
    static var request: [String: Any] {
        let s = service
        return ["service": s.id, "provider": s.api, "model": model, "base_url": baseURL, "no_memory": !useMemory]
    }

    // MARK: What the task box says

    /// The model as the person reads it, with its service unless that is
    /// Anthropic or one the person typed (ui/ask.go, modelLine).
    static var modelLabel: String {
        let s = service, m = model
        let name = s.modelName(m)
        if s.isCustom || s.id == anthropic || m.isEmpty { return name }
        return name + " · " + s.label
    }

    /// Whether Pinky knows the model's prices, and so counts the cost
    /// limit in dollars.
    static var isPriced: Bool { service.priced(model) }

    static var maxSteps: Int { positive(d.integer(forKey: "maxSteps")) ?? defaultSteps }
    static var maxMinutes: Int { positive(d.integer(forKey: "maxMinutes")) ?? defaultMinutes }
    static var maxCost: Double {
        let v = d.double(forKey: "maxCost")
        return v > 0 ? v : defaultCost
    }

    /// The limit the task box shows: "Up to US$2.00 a task".
    static var limitLine: String {
        let l = L.costLimit(priced: isPriced, maxCost: maxCost)
        return String(localized: "Up to \(l) a task", comment: "Under the task box: the cost limit of a task")
    }

    /// An address as it is kept: trimmed, without a trailing slash.
    static func cleanURL(_ v: String) -> String {
        var u = v.trimmingCharacters(in: .whitespacesAndNewlines)
        while u.hasSuffix("/") { u.removeLast() }
        return u
    }

    /// Whether an address may be saved: empty for the default, else http
    /// or https.
    static func urlOK(_ v: String) -> Bool {
        let u = cleanURL(v)
        return u.isEmpty || u.hasPrefix("https://") || u.hasPrefix("http://")
    }

    /// Whether tasks read the notes and suggest new ones (MemoryView).
    static var useMemory: Bool { d.object(forKey: "useMemory") as? Bool ?? true }

    private static func positive(_ n: Int) -> Int? { n > 0 ? n : nil }
}

extension L {
    /// Tokens and, when some had known prices, what they cost:
    /// "41,000 tokens · US$0.012" (ui/ask.go, amountText).
    static func amount(tokens: Int64, cost: Double, unpriced: Int64) -> String {
        var s = L.tokens(Int(tokens))
        if unpriced < tokens {
            let n = String(format: cost < 1 ? "%.3f" : "%.2f", locale: Locale(identifier: "en_US_POSIX"), cost)
            s += " · " + String(localized: "US$\(n)", comment: "An amount in US dollars; the number has two decimals")
        }
        return s
    }

    /// "3 requests", with the plural the language needs.
    static func requests(_ n: Int) -> String {
        String(localized: "\(n) requests", comment: "How many requests a model got on a day, in Settings › Usage")
    }

    static func amount(_ t: UsageRow.Total) -> String { amount(tokens: t.tokens, cost: t.cost, unpriced: t.unpriced) }
}

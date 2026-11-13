import SwiftUI

/// Settings, in the sections and words the Android app has too: the AI
/// model (the service, its key, model and address, as desktop Pinky's Ask
/// Pinky has them, ui/ask.go), its check, the limits of a task, usage,
/// when Pinky asks, this iPhone, and about.
struct SettingsView: View {
    @EnvironmentObject var m: AppModel
    @Environment(\.dismiss) private var dismiss
    /// The service chosen; empty until the person chooses, when the
    /// settings of before name it (TaskSettings.serviceID).
    @AppStorage("service") private var storedService = ""
    @AppStorage("maxSteps") private var maxSteps = TaskSettings.defaultSteps
    @AppStorage("maxMinutes") private var maxMinutes = TaskSettings.defaultMinutes
    @AppStorage("maxCost") private var maxCost = TaskSettings.defaultCost
    @State private var key = ""
    @State private var hasKey = false
    /// The service's model as saved, and the one being typed.
    @State private var model = ""
    @State private var modelText = ""
    @State private var typingModel = false
    /// The address set for the service (empty follows its own), as typed.
    @State private var address = ""
    @State private var check: Check?

    /// The last check: of which model, and how it went (nil while it runs).
    private struct Check: Equatable {
        var of: String
        var outcome: AppModel.CheckOutcome?
    }

    var body: some View {
        NavigationStack {
            Form {
                modelSection
                checkSection
                limitsSection
                Section {
                    NavigationLink("Memory") { MemoryView() }
                } footer: {
                    Text("What Pinky remembers between tasks")
                }
                usageSection
                askSection
                phoneSection
                Section("About") {
                    LabeledContent("Version", value: version)
                }
            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") {
                        saveKey(check: false)
                        saveModelText(check: false)
                        saveAddress(check: false)
                        dismiss()
                    }
                }
            }
            .onAppear {
                load()
                Task { await m.loadUsage() }
            }
            .onChange(of: storedService) { _, _ in
                key = ""
                load()
            }
        }
        .tint(.pinkyAccent)
    }

    private var service: ModelService {
        _ = storedService
        return TaskSettings.service
    }

    /// Reads the service's key, model and address.
    private func load() {
        let s = service
        hasKey = TaskSettings.key(s) != nil
        model = TaskSettings.model(s)
        modelText = model
        typingModel = s.suggested.isEmpty
        address = TaskSettings.savedURL(s)
    }

    // MARK: AI model

    private var modelSection: some View {
        let s = service
        return Section {
            Picker("Service", selection: Binding(get: { s.id }, set: { storedService = $0 })) {
                ForEach(ModelService.all) { Text(verbatim: $0.label).tag($0.id) }
            }
            if hasKey {
                LabeledContent("API key") { Text("Saved").foregroundStyle(Color.pinkyGreen) }
            }
            SecureField(hasKey ? "Change the API key" : "Paste the API key", text: $key)
                .autocorrectionDisabled().textInputAutocapitalization(.never)
                .onSubmit { saveKey(check: true) }
            if hasKey {
                Button("Remove the API key", role: .destructive) {
                    Keychain.set(TaskSettings.keyAccount(s), "")
                    hasKey = false
                    check = nil
                }
            }
            if !s.suggested.isEmpty {
                Picker("Model", selection: modelChoice) {
                    ForEach(modelChoices(s), id: \.id) { Text(verbatim: $0.label).tag($0.id) }
                    Text("Other…").tag(Self.other)
                }
            }
            if typingModel {
                LabeledContent("Model") {
                    TextField("Model name", text: $modelText)
                        .multilineTextAlignment(.trailing)
                        .autocorrectionDisabled().textInputAutocapitalization(.never)
                        .onSubmit { saveModelText(check: true) }
                }
            }
            LabeledContent("API address") {
                TextField(s.base_url.isEmpty ? String(localized: "Default") : s.base_url, text: $address)
                    .multilineTextAlignment(.trailing)
                    .autocorrectionDisabled().textInputAutocapitalization(.never).keyboardType(.URL)
                    .onSubmit { saveAddress(check: true) }
            }
            if !TaskSettings.urlOK(address) {
                Text("Enter an address that starts with https:// or http://.").font(.footnote).foregroundStyle(.red)
            }
        } header: {
            Text("AI model")
        } footer: {
            VStack(alignment: .leading, spacing: 6) {
                Text("Who runs the model. Each service keeps its own API key, model and address.")
                if s.keyOptional {
                    Text("Only if you set one up on the server. The key is kept in this iPhone's Keychain and sent only to the service.")
                } else {
                    Text("The key is kept in this iPhone's Keychain and sent only to the service.")
                }
                Text(modelDesc(s))
                Text(addressDesc(s))
            }
        }
    }

    /// The tag of Other…, which no model has.
    private static let other = "\u{0}other"

    /// The model chosen among the suggested ones, or Other… while one is
    /// typed.
    private var modelChoice: Binding<String> {
        Binding(get: { typingModel ? Self.other : model }, set: { v in
            if v == Self.other {
                typingModel = true
                modelText = model
                return
            }
            typingModel = false
            saveModel(v, check: true)
        })
    }

    /// The models offered, with the one chosen before when it is not among
    /// them.
    private func modelChoices(_ s: ModelService) -> [ModelService.Model] {
        let list = s.suggested
        return model.isEmpty || list.contains { $0.id == model } ? list : list + [.init(id: model)]
    }

    private func modelDesc(_ s: ModelService) -> LocalizedStringKey {
        if s.id == TaskSettings.anthropic { return "Opus is the most capable; Sonnet and Haiku cost less." }
        if !s.suggested.isEmpty {
            return "Models that read images and use tools, as Pinky needs. Choose Other… for another model of the service."
        }
        if s.isLocal { return "The name of a model the server runs. It must read images and use tools." }
        return "The model's name as the service gives it. It must read images and use tools."
    }

    private func addressDesc(_ s: ModelService) -> LocalizedStringKey {
        if s.id == ModelService.customAnthropic {
            return "Leave it for Anthropic's API, or give the address of a service compatible with it."
        }
        if s.id == ModelService.customOpenAI {
            return "The address of the service's OpenAI-compatible API, such as https://…/v1. OpenAI's own by default."
        }
        if s.isLocal {
            return "The server's address. On iPhone, give the address of the computer that runs it on your network, such as http://192.168.1.10:11434/v1."
        }
        return "The service's own address by default. Change it only to go through another address, such as a proxy."
    }

    // MARK: Check

    /// What a check is of: the service, address and model.
    private var checkSig: String {
        let s = service
        _ = (model, address, hasKey)
        return [s.id, TaskSettings.savedURL(s), TaskSettings.model(s)].joined(separator: "\n")
    }

    private var checkSection: some View {
        let sig = checkSig
        let c = check?.of == sig ? check : nil
        return Section {
            if let c, c.outcome == nil {
                Label { Text("Checking…").foregroundStyle(.secondary) } icon: { ProgressView() }
            } else {
                Button(c == nil ? "Check" : "Check Again") { runCheck() }
            }
            if let o = c?.outcome {
                switch o {
                case .result(let r) where r.ok:
                    Label { Text(verbatim: r.text) } icon: {
                        Image(systemName: "checkmark.circle.fill").foregroundStyle(Color.pinkyGreen)
                    }
                    .font(.callout)
                case .result(let r):
                    VStack(alignment: .leading, spacing: 4) {
                        Text(verbatim: r.text).foregroundStyle(.red)
                        if let said = r.said {
                            Text(verbatim: said).font(.footnote).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                    }
                    .font(.callout)
                case .failed(let why):
                    Text(verbatim: why).font(.callout).foregroundStyle(.red)
                }
            }
        } footer: {
            Text("Sends the model one tiny request to see that it answers and uses tools. Pinky does it when you save a key, model or address.")
        }
    }

    /// Checks the model of the settings as saved, off the main thread; a
    /// check that ends after the settings changed again is dropped.
    private func runCheck() {
        let sig = checkSig
        check = Check(of: sig)
        Task {
            let o = await m.check()
            guard check?.of == sig else { return }
            check = Check(of: sig, outcome: o)
            if case .result(let r) = o, r.problem == "no_key" || r.problem == "bad_key" {
                hasKey = TaskSettings.key(service) != nil
            }
            await m.loadUsage() // the check used a few tokens
        }
    }

    // MARK: Limits

    private var limitsSection: some View {
        Section {
            Picker("Steps per task", selection: $maxSteps) {
                ForEach(TaskSettings.stepChoices, id: \.self) { Text(verbatim: L.steps($0)).tag($0) }
            }
            Picker("Time per task", selection: $maxMinutes) {
                ForEach(TaskSettings.minuteChoices, id: \.self) { Text(verbatim: L.minutes($0)).tag($0) }
            }
            Picker("Cost per task", selection: $maxCost) {
                ForEach(TaskSettings.costChoices, id: \.self) { c in
                    Text(verbatim: L.costLimit(priced: priced, maxCost: c)).tag(c)
                }
            }
        } header: {
            Text("Limits")
        } footer: {
            VStack(alignment: .leading, spacing: 6) {
                Text("Each action on the phone is a step. Time spent waiting for you doesn't count.")
                if priced {
                    Text("Counted from what the service reports, at the prices it publishes.")
                } else {
                    Text("Pinky doesn't know this model's prices, so it counts a million tokens as US$1.")
                }
            }
        }
    }

    private var priced: Bool { service.priced(model) }

    // MARK: Usage

    @ViewBuilder private var usageSection: some View {
        let rows = m.usage
        Section {
            usageRow("Today", rows, since: UsageRow.day(back: 0))
            usageRow("Last 7 days", rows, since: UsageRow.day(back: 6))
            usageRow("Last 30 days", rows, since: UsageRow.day(back: 29))
        } header: {
            Text("Usage")
        } footer: {
            Text("The tokens Pinky's own agent used, as the services reported them, and what they cost where Pinky knows the prices. Kept on this iPhone only.")
        }
        let days = usageDays(rows)
        if !days.isEmpty {
            Section {
                ForEach(days, id: \.self) { day in
                    VStack(alignment: .leading, spacing: 6) {
                        Text(verbatim: UsageRow.dayFormat.date(from: day).map { L.day($0) } ?? day).font(.subheadline.weight(.medium))
                        ForEach(Array(rows.filter { $0.day == day }.enumerated()), id: \.offset) { _, r in
                            VStack(alignment: .leading, spacing: 1) {
                                Text(verbatim: r.who).font(.footnote).lineLimit(1)
                                Text(verbatim: L.requests(r.requests) + " · "
                                    + L.amount(tokens: r.tokens, cost: r.cost ?? 0, unpriced: r.unpriced ?? 0))
                                    .font(.footnote.monospacedDigit()).foregroundStyle(.secondary)
                            }
                        }
                    }
                    .padding(.vertical, 2)
                }
            }
        }
    }

    private func usageRow(_ title: LocalizedStringKey, _ rows: [UsageRow], since: String) -> some View {
        let t = UsageRow.total(rows, since: since)
        return LabeledContent(title) {
            if t.tokens > 0 {
                Text(verbatim: L.amount(t)).monospacedDigit()
            } else {
                Text("Nothing yet.")
            }
        }
    }

    /// The last days with usage, newest first: at most 7.
    private func usageDays(_ rows: [UsageRow]) -> [String] {
        var days: [String] = []
        for r in rows where days.last != r.day { days.append(r.day) }
        return Array(days.prefix(7))
    }

    // MARK: When Pinky asks

    private var askSection: some View {
        Section {
            ForEach(Preset.ordered, id: \.self) { p in
                Button { Task { await m.setPreset(p) } } label: {
                    HStack(alignment: .firstTextBaseline) {
                        VStack(alignment: .leading, spacing: 3) {
                            Text(p.title).foregroundStyle(.primary)
                            Text(p.detail).font(.footnote).foregroundStyle(.secondary)
                        }
                        Spacer()
                        if preset == p {
                            Image(systemName: "checkmark").fontWeight(.semibold).foregroundStyle(Color.pinkyAccent)
                        }
                    }
                }
                .disabled(m.state == nil)
            }
        } header: {
            Text("When should Pinky ask you?")
        } footer: {
            if m.state == nil { Text("Turn on Pinky's VPN to change this.") }
        }
    }

    private var preset: Preset? { m.state.flatMap { Preset(rawValue: $0.preset) } }

    // MARK: This iPhone

    private var phoneSection: some View {
        Section {
            LabeledContent("VPN", value: vpnText)
            LabeledContent("Pinky's core runs in", value: m.inApp ? String(localized: "This app") : String(localized: "The VPN"))
            if !m.inApp {
                if m.vpn == .off {
                    Button("Turn on") { Task { await m.connect() } }
                } else {
                    Button("Turn off", role: .destructive) { m.disconnect() }
                }
            }
        } header: {
            Text("This iPhone")
        } footer: {
            Text("Pinky reaches this iPhone through a VPN that never leaves the phone.")
        }
    }

    private var vpnText: String {
        switch m.vpn {
        case .on: String(localized: "On")
        case .connecting: String(localized: "Turning on…")
        case .off: String(localized: "Off")
        }
    }

    private var version: String {
        let i = Bundle.main.infoDictionary
        let v = i?["CFBundleShortVersionString"] as? String ?? ""
        let b = i?["CFBundleVersion"] as? String ?? ""
        return b.isEmpty || b == v ? v : "\(v) (\(b))"
    }

    private func saveKey(check: Bool) {
        let k = key.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !k.isEmpty else { return }
        hasKey = Keychain.set(TaskSettings.keyAccount(service), k)
        key = ""
        if check && hasKey { runCheck() }
    }

    private func saveModel(_ v: String, check: Bool) {
        let s = service
        TaskSettings.setModel(s, v)
        model = TaskSettings.model(s)
        modelText = model
        if check { runCheck() }
    }

    private func saveModelText(check: Bool) {
        guard typingModel else { return }
        saveModel(modelText, check: check)
    }

    private func saveAddress(check: Bool) {
        let s = service
        guard TaskSettings.urlOK(address) else { return }
        let before = TaskSettings.savedURL(s)
        TaskSettings.setURL(s, address)
        address = TaskSettings.savedURL(s)
        if check && address != before { runCheck() }
    }
}

extension Preset {
    /// The choices in the order Settings lists them: the default first.
    static let ordered: [Preset] = [.commits, .payments, .every]

    var title: LocalizedStringKey {
        switch self {
        case .commits: "Ask before payments, messages and deletions"
        case .payments: "Ask before payments only"
        case .every: "Ask before every action"
        }
    }

    var detail: LocalizedStringKey {
        switch self {
        case .commits: "Also asks before calls, account changes and installing apps."
        case .payments: "Messages and deletions go through without asking."
        case .every: "Slower, but you see every tap first."
        }
    }
}

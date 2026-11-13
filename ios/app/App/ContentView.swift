import SwiftUI

/// Pinky's task screen, in the structure the Android app has too
/// (docs/notes/phone-apps-ux.md): one status card (set up, not ready,
/// computer, running or ended), what Pinky asks, the task box, the task's
/// steps and the latest Activity; Activity and Settings in the toolbar.
struct ContentView: View {
    @EnvironmentObject var m: AppModel
    // Read here so that the screen follows what Settings changes; the
    // model and address are kept per service, so closing Settings counts
    // as a change too (settingsRev).
    @AppStorage("service") private var service = ""
    @AppStorage("maxCost") private var maxCost = 0.0
    @State private var settingsRev = 0
    @State private var text = ""
    /// The message for Pinky while a task runs, or to continue it.
    @State private var message = ""
    @State private var showSettings = false
    @State private var showActivity = false
    /// Keychain is no observable store: the screen reads it again when
    /// Settings closes, which is where the key is typed.
    @State private var hasKey = false
    @FocusState private var typing: Bool
    @StateObject private var dictation = Dictation()
    @ObservedObject private var drafts = Drafts.shared
    /// What a shortcut passed along with the task (NewTaskIntent).
    @State private var shared = ""
    /// The box's text when the mic started: what is heard goes after it.
    @State private var beforeVoice = ""

    var body: some View {
        NavigationStack {
            List {
                if let e = Profile.endsSoon() {
                    Section {
                        Label(L.profileEnds(e), systemImage: "clock.badge.exclamationmark").foregroundStyle(Color.pinkyAmber)
                    }
                }
                statusCard
                if let a = messagesFor { MessagesSection(agent: a, text: $message) }
                ForEach(m.state?.needs ?? []) { NeedCard(need: $0) }
                if ready && setUp && !running { taskBox }
                if let p = m.problem {
                    Section { Label(p, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red) }
                }
                if let steps = m.state?.task?.steps, !steps.isEmpty {
                    Section("Steps") {
                        ForEach(Array(steps.enumerated()), id: \.offset) { _, s in StepRow(step: s) }
                    }
                }
                if !m.recent.isEmpty { activity }
            }
            .navigationTitle(Text(verbatim: "Pinky"))
            .modifier(TitleWithMark())
            .toolbar {
                ToolbarItemGroup(placement: .topBarTrailing) {
                    Button { showActivity = true } label: { Label("Activity", systemImage: "clock.arrow.circlepath") }
                    Button { showSettings = true } label: { Label("Settings", systemImage: "gearshape") }
                }
            }
            .navigationDestination(isPresented: $showActivity) { ActivityView() }
            .sheet(isPresented: $showSettings, onDismiss: settingsClosed) { SettingsView().environmentObject(m) }
            .onAppear {
                readKey()
                Task { await m.loadUsage() }
            }
            .onAppear(perform: takeDraft)
            .onChange(of: drafts.pending) { _, _ in takeDraft() }
            .animation(.default, value: m.state)
        }
        .tint(.pinkyAccent)
    }

    // MARK: State

    private var running: Bool { m.state?.agent?.running == true }
    private var ready: Bool { m.vpn == .on && m.state?.device.ready == true }
    private var setUp: Bool { hasKey && !TaskSettings.model.isEmpty }
    private var vpnOn: Bool { m.vpn == .on }

    /// The task the person can send messages to: the running one under its
    /// card, or the one that ended and can go on under the ended card.
    private var messagesFor: AgentState? {
        guard m.paired, vpnOn, let st = m.state, st.device.ready, let a = st.agent else { return nil }
        if a.running { return a }
        return setUp && st.computer == nil && a.continuable == true ? a : nil
    }

    private func settingsClosed() {
        readKey()
        settingsRev += 1
    }

    /// Whether the service's key is saved, or the service needs none.
    private func readKey() {
        _ = service
        hasKey = TaskSettings.keyReady
        #if DEBUG
        if m.demo { hasKey = Demo.hasKey }
        #endif
    }

    // MARK: The status card

    /// At most one card, the first state that holds.
    @ViewBuilder private var statusCard: some View {
        if !m.paired || !vpnOn || (!setUp && !running) {
            setupCard
        } else if let st = m.state, !st.device.ready {
            notReadyCard(st.device)
        } else if let c = m.state?.computer, !running {
            computerCard(c)
        } else if let a = m.state?.agent, a.running {
            RunningCard(agent: a, waiting: m.state?.needs.first)
        } else if let a = m.state?.agent {
            EndedCard(agent: a)
        }
    }

    private var setupCard: some View {
        Section {
            VStack(alignment: .leading, spacing: 6) {
                Text("Set up Pinky").font(.title3.bold())
                Text("Pinky can do simple tasks on your iPhone by itself, with an AI model you choose.")
                    .font(.callout).foregroundStyle(.secondary)
            }
            .padding(.vertical, 4)
            SetupStep(number: 1, title: "Pair Pinky with this iPhone", done: m.paired, now: !m.paired) {
                if PhonePairing.available {
                    PairOnPhone()
                } else {
                    Text("Before iOS 27, Pinky pairs with this iPhone from a Mac with Pinky, once.")
                        .font(.callout).foregroundStyle(.secondary)
                }
            }
            SetupStep(number: 2, title: "Turn on Pinky's VPN", done: m.paired && vpnOn, now: m.paired && !vpnOn) {
                Text("Pinky reaches this iPhone through a VPN that never leaves the phone.")
                    .font(.callout).foregroundStyle(.secondary)
                if m.inApp {
                    Text("Turn on LocalDevVPN, then come back.").font(.callout).foregroundStyle(.secondary)
                } else {
                    Button(m.vpn == .connecting ? LocalizedStringKey("Turning on…") : "Turn on") { Task { await m.connect() } }
                        .buttonStyle(.borderedProminent)
                        .disabled(m.vpn == .connecting)
                }
            }
            SetupStep(number: 3, title: "Choose the AI model and add its API key", done: setUp,
                      now: m.paired && vpnOn && !setUp) {
                Text("Pinky thinks with a model you choose and pays for: Claude through Anthropic's API, or another one through a service compatible with OpenAI's.")
                    .font(.callout).foregroundStyle(.secondary)
                Button(hasKey ? LocalizedStringKey("Choose a model") : "Add an API key") { showSettings = true }
                    .buttonStyle(.borderedProminent)
            }
        }
        .listRowBackground(Color.pinkyAccent.opacity(0.08))
    }

    private func notReadyCard(_ d: DeviceState) -> some View {
        Section {
            if m.reason == nil || m.reason == "connecting" {
                Label { Text("Starting Pinky…").font(.headline) } icon: { ProgressView() }
            } else {
                Label { Text("Pinky can't reach this iPhone").font(.headline) } icon: {
                    Image(systemName: "iphone.slash").foregroundStyle(Color.pinkyAmber)
                }
                Text(L.reason(m.reason)).font(.callout)
                if let detail = d.detail, !detail.isEmpty, m.reason == "failed" {
                    Text(detail).font(.footnote).foregroundStyle(.secondary)
                }
            }
        }
    }

    private func computerCard(_ c: ComputerState) -> some View {
        Section {
            Label { Text("Pinky on your computer is using this iPhone").font(.headline) } icon: {
                Image(systemName: "desktopcomputer").foregroundStyle(Color.pinkyAccent)
            }
            if let t = c.task, !t.isEmpty { Text(t).font(.callout) }
            Text("You can start a task here when it's done, or once you choose Pause connection for this iPhone there.")
                .font(.footnote).foregroundStyle(.secondary)
        }
        .listRowBackground(Color.pinkyAccent.opacity(0.08))
    }

    // MARK: The task box

    private var taskBox: some View {
        Section {
            TextField("What should Pinky do on this iPhone?", text: $text, axis: .vertical)
                .lineLimit(3...8)
                .focused($typing)
                .padding(.vertical, 4)
            if !shared.isEmpty {
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: "paperclip").foregroundStyle(.secondary)
                    Text(verbatim: shared).font(.footnote).foregroundStyle(.secondary).lineLimit(3)
                    Spacer(minLength: 0)
                    Button { shared = "" } label: { Image(systemName: "xmark.circle.fill").foregroundStyle(.tertiary) }
                        .buttonStyle(.borderless)
                        .accessibilityLabel(Text("Remove"))
                }
            }
            if let p = dictation.problem {
                Text(p).font(.footnote).foregroundStyle(.red)
            }
            HStack(alignment: .center) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: modelLabel).font(.footnote.weight(.medium)).foregroundStyle(.secondary)
                    Text(verbatim: limitLine).font(.footnote).foregroundStyle(.tertiary)
                    if let today = usageToday {
                        Text(verbatim: today).font(.footnote.monospacedDigit()).foregroundStyle(.tertiary)
                    }
                }
                Spacer()
                if Dictation.available || dictation.listening {
                    Button {
                        if dictation.listening {
                            dictation.stop()
                        } else {
                            typing = false
                            beforeVoice = text.trimmingCharacters(in: .whitespacesAndNewlines)
                            Task {
                                await dictation.start { heard in
                                    text = beforeVoice.isEmpty ? heard : beforeVoice + " " + heard
                                }
                            }
                        }
                    } label: {
                        Image(systemName: dictation.listening ? "stop.circle.fill" : "mic")
                            .font(.title3)
                            .symbolEffect(.pulse, isActive: dictation.listening)
                    }
                    .buttonStyle(.borderless)
                    .accessibilityLabel(dictation.listening ? Text("Stop listening") : Text("Speak"))
                }
                Button {
                    dictation.stop()
                    let t = text
                    let with = shared
                    typing = false
                    Task {
                        await m.startTask(t, shared: with)
                        if m.problem == nil {
                            text = ""
                            shared = ""
                        }
                    }
                } label: {
                    Text("Start").frame(minWidth: 56)
                }
                .buttonStyle(.borderedProminent)
                .disabled(text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || m.state?.computer != nil)
            }
        } footer: {
            if m.state?.agent == nil {
                Text("Pinky leaves this app and works in the other apps. You can watch it, answer when it asks, and stop it at any time.")
            }
        }
    }

    /// Puts a shortcut's task into the box (NewTaskIntent). A task the
    /// person was typing gives way only to a shortcut's own words.
    private func takeDraft() {
        guard let d = drafts.pending else { return }
        drafts.pending = nil
        if !d.task.isEmpty { text = d.task }
        shared = d.shared
        if !d.task.isEmpty || !d.shared.isEmpty { typing = d.task.isEmpty }
    }

    // Read with the stored settings, so that SwiftUI redraws when they change.
    private var modelLabel: String {
        _ = (service, settingsRev)
        return TaskSettings.modelLabel
    }

    private var limitLine: String {
        _ = (service, maxCost, settingsRev)
        return TaskSettings.limitLine
    }

    /// What the model used today, "Today: 41,000 tokens · US$0.012"; nil
    /// when nothing.
    private var usageToday: String? {
        let t = UsageRow.total(m.usage, since: UsageRow.day(back: 0))
        guard t.tokens > 0 else { return nil }
        return String(localized: "Today: \(L.amount(t))", comment: "Under the task box: what the model used today")
    }

    // MARK: Activity

    private var activity: some View {
        Section {
            ForEach(m.recent) { t in
                NavigationLink { PastTaskView(task: t) } label: { PastTaskRow(task: t) }
            }
            Button("Show all") { showActivity = true }
        } header: {
            Text("Activity")
        }
    }
}

/// A task while it runs: what it does, what it used, what waits for the
/// person, and Stop.
struct RunningCard: View {
    @EnvironmentObject var m: AppModel
    let agent: AgentState
    let waiting: Need?

    var body: some View {
        Section {
            HStack {
                Label {
                    Text("Pinky is working on it").font(.headline)
                } icon: {
                    Image(systemName: "circle.fill").font(.system(size: 10)).foregroundStyle(Color.pinkyGreen)
                        .symbolEffect(.pulse, options: .repeating)
                }
                Spacer()
                Button(role: .destructive) { Task { await m.stop() } } label: { Text("Stop") }
                    .buttonStyle(.bordered)
                    .tint(.red)
            }
            VStack(alignment: .leading, spacing: 6) {
                Text(agent.task).font(.body)
                Text(verbatim: L.usage(agent)).font(.footnote.monospacedDigit()).foregroundStyle(.secondary)
                if let w = waiting {
                    Text("Waiting for you: \(w.title)").font(.callout.weight(.semibold)).foregroundStyle(Color.pinkyAmber)
                }
            }
            .padding(.vertical, 2)
        }
        .listRowBackground(Color.pinkyAccent.opacity(0.08))
    }
}

/// How the last task ended, until the person dismisses it.
struct EndedCard: View {
    @EnvironmentObject var m: AppModel
    let agent: AgentState

    var body: some View {
        let e = L.end(agent)
        Section {
            HStack {
                Label { Text(verbatim: e.title).font(.headline).foregroundStyle(e.tone.color) } icon: {
                    Image(systemName: e.tone.symbol).foregroundStyle(e.tone.color)
                }
                Spacer()
                Button("Dismiss") { Task { await m.dismiss() } }
                    .buttonStyle(.bordered)
            }
            VStack(alignment: .leading, spacing: 6) {
                if !e.reason.isEmpty { Text(e.reason).font(.body) }
                Text(agent.task).font(.callout).foregroundStyle(.secondary).lineLimit(3)
                Text(verbatim: L.usage(agent)).font(.footnote.monospacedDigit()).foregroundStyle(.secondary)
            }
            .padding(.vertical, 2)
            if let s = agent.suggested, !s.isEmpty { SuggestionsView(list: s) }
        }
        .listRowBackground(e.tone.color.opacity(0.08))
    }
}

/// Pairing Pinky with this iPhone from the iPhone itself (iOS 27): the
/// steps in Settings and the code to type there.
struct PairOnPhone: View {
    @EnvironmentObject var m: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Pinky needs to pair with this iPhone once. You do it in Settings, with a code Pinky shows here.")
                .font(.callout).foregroundStyle(.secondary)
            switch m.pairStep {
            case .idle, .paired:
                Button("Pair") { m.pairOnPhone() }.buttonStyle(.borderedProminent)
            case .failed(let why):
                Text(why).font(.callout).foregroundStyle(.red)
                Button("Try again") { m.pairOnPhone() }.buttonStyle(.borderedProminent)
            case .waiting, .code, .wrongCode, .retry:
                VStack(alignment: .leading, spacing: 6) {
                    Text("1. Open Settings › Privacy & Security › Developer Mode.")
                    Text("2. Choose to pair with “Pinky”.")
                    Text("3. Type the code below. It also comes as a notification.")
                }
                .font(.callout)
                switch m.pairStep {
                case .code(let code):
                    Text(verbatim: code)
                        .font(.system(.largeTitle, design: .monospaced).bold())
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity)
                        .accessibilityLabel(Text("Pairing code \(code)"))
                case .wrongCode:
                    Text("The code did not match. Pair again from Settings: Pinky shows a new code.").foregroundStyle(.red)
                case .retry(let why):
                    Text("Pairing did not finish. Try again from Settings.").foregroundStyle(.red)
                    Text(verbatim: why).font(.footnote).foregroundStyle(.secondary)
                default:
                    Label("Waiting for Settings…", systemImage: "hourglass")
                        .foregroundStyle(.secondary)
                }
                Button("Cancel", role: .cancel) { m.stopPairing() }
            }
        }
    }
}

/// An approval or a handoff the agent waits for.
struct NeedCard: View {
    @EnvironmentObject var m: AppModel
    let need: Need

    var body: some View {
        Section {
            VStack(alignment: .leading, spacing: 6) {
                Text(need.title).font(.headline)
                if let d = need.detail, !d.isEmpty { Text(d).font(.callout) }
                if let r = need.rule, !r.isEmpty {
                    Label { Text("Rule: \(r)") } icon: { Image(systemName: "checkmark.shield") }
                        .font(.footnote).foregroundStyle(.secondary)
                }
                if let dl = need.deadline {
                    Label { Text("No answer by \(dl.formatted(date: .omitted, time: .shortened)) denies it") } icon: {
                        Image(systemName: "timer")
                    }
                    .font(.footnote).foregroundStyle(.secondary)
                }
                if need.kind == "handoff" {
                    Text("Do it yourself in the app, then come back here.").font(.footnote).foregroundStyle(.secondary)
                }
            }
            .padding(.vertical, 2)
            switch need.kind {
            case "approval":
                ViewThatFits(in: .horizontal) {
                    HStack { approvalButtons }
                    VStack(alignment: .leading) { approvalButtons }
                }
                .buttonStyle(.bordered)
            case "handoff":
                HStack {
                    Button("Decline", role: .destructive) { Task { await m.decide(need, "deny") } }
                    Spacer()
                    Button("Done") { Task { await m.decide(need, "allow_once") } }
                        .buttonStyle(.borderedProminent)
                }
                .buttonStyle(.bordered)
            default:
                EmptyView()
            }
        } header: {
            Label("Pinky asks", systemImage: "hand.raised.fill").foregroundStyle(Color.pinkyAmber)
        }
        .listRowBackground(Color.pinkyAmber.opacity(0.08))
    }

    @ViewBuilder private var approvalButtons: some View {
        Button("Deny", role: .destructive) { Task { await m.decide(need, "deny") } }
        Spacer(minLength: 0)
        if need.allow_task == true {
            Button("Allow for this task") { Task { await m.decide(need, "allow_task") } }
        }
        Button { Task { await m.decide(need, "allow_once") } } label: {
            Label("Allow", systemImage: "faceid")
        }
        .buttonStyle(.borderedProminent)
    }
}

/// One step of a task.
struct StepRow: View {
    let step: Step

    var body: some View {
        Label {
            Text(step.text).font(.callout)
        } icon: {
            Image(systemName: icon).foregroundStyle(color)
        }
    }

    /// What the person told Pinky (gateway.PersonSaid).
    private var said: Bool { step.text.hasPrefix("You said: ") }

    private var icon: String {
        if said { return "text.bubble" }
        return switch step.status {
        case "approval_pending": "hand.raised"
        case "running", "pending", "": "circle.dotted"
        case "approval_denied", "blocked_by_policy", "failed", "error": "xmark.circle"
        case "user": "person.circle"
        default: "checkmark.circle"
        }
    }

    private var color: Color {
        if said { return .pinkyAccent }
        return switch step.status {
        case "approval_pending": .pinkyAmber
        case "approval_denied", "blocked_by_policy", "failed", "error": .red
        case "running", "pending", "", "user": .secondary
        default: .pinkyGreen
        }
    }
}

import SwiftUI

/// The person's messages to Pinky, under the running or the ended card.
/// While the task runs: the messages the model has not read yet, each with
/// Edit and Withdraw, and a field that sends a new one, which the model
/// reads at its next step. Once it ended and can go on: the messages left
/// unread, and a field that continues the task with a new message.
struct MessagesSection: View {
    @EnvironmentObject var m: AppModel
    let agent: AgentState
    /// The field's text, kept by the task screen: when the task ends before
    /// a message is sent, it stays for Continue.
    @Binding var text: String
    @State private var editing: String?
    @State private var draft = ""
    /// The message being saved or withdrawn: it leaving the pending list
    /// is no sign that the model read it.
    @State private var changing: String?
    @State private var sending = false
    @State private var problem: String?
    @FocusState private var typing: Bool

    private var running: Bool { agent.running }
    private var pending: [PendingMessage] { agent.pending ?? [] }
    private var trimmed: String { text.trimmingCharacters(in: .whitespacesAndNewlines) }
    private var canSend: Bool { !sending && (!trimmed.isEmpty || (!running && !pending.isEmpty)) }

    var body: some View {
        Section {
            if !running && !pending.isEmpty {
                Text("Pinky didn't read these before the task ended. Continue to send them.")
                    .font(.footnote).foregroundStyle(.secondary)
            }
            ForEach(pending) { row($0) }
            field
            if let p = problem {
                Label(p, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red)
            }
        }
        .onChange(of: pending.map(\.id)) { _, ids in
            // The model read the message while the person was editing it.
            if let e = editing, e != changing, !ids.contains(e) {
                editing = nil
                if running { problem = L.alreadyRead }
            }
        }
        .onChange(of: running) {
            editing = nil
            problem = nil
        }
    }

    // MARK: A message waiting to be read

    @ViewBuilder private func row(_ msg: PendingMessage) -> some View {
        if running && editing == msg.id {
            VStack(alignment: .leading, spacing: 8) {
                TextField("Message for Pinky", text: $draft, axis: .vertical)
                    .lineLimit(1...6)
                HStack {
                    Spacer()
                    Button("Cancel") { editing = nil }
                        .buttonStyle(.bordered)
                    Button("Save") { save(msg.id) }
                        .buttonStyle(.borderedProminent)
                        .disabled(changing != nil)
                }
                .controlSize(.small)
            }
            .padding(.vertical, 2)
        } else {
            Label {
                VStack(alignment: .leading, spacing: 4) {
                    Text(verbatim: msg.text).font(.callout)
                    if running {
                        HStack(spacing: 16) {
                            Text("Pinky reads this at its next step").font(.footnote).foregroundStyle(.secondary)
                            Spacer(minLength: 0)
                            Button("Edit") {
                                problem = nil
                                draft = msg.text
                                editing = msg.id
                            }
                            Button("Withdraw", role: .destructive) { withdraw(msg.id) }
                        }
                        .font(.footnote)
                        .buttonStyle(.borderless)
                        .disabled(changing != nil)
                    }
                }
            } icon: {
                Image(systemName: "ellipsis.bubble").foregroundStyle(.secondary)
            }
            .padding(.vertical, 2)
        }
    }

    // MARK: The field

    private var field: some View {
        HStack(alignment: .center, spacing: 8) {
            TextField(running ? LocalizedStringKey("Add or change instructions…") : "Tell Pinky what to do next…", text: $text)
                .focused($typing)
                .submitLabel(running ? .send : .continue)
                .onSubmit(send)
                .accessibilityLabel(running ? Text("Message for Pinky") : Text("Next message for Pinky"))
            Button(action: send) {
                Text(running ? LocalizedStringKey("Send") : "Continue")
            }
            .buttonStyle(.borderedProminent)
            .disabled(!canSend)
        }
        .padding(.vertical, 2)
    }

    private func send() {
        guard canSend else { return }
        let t = trimmed
        let toTask = running
        problem = nil
        sending = true
        text = ""
        Task {
            defer { sending = false }
            if toTask {
                switch await m.steer(t) {
                case .ok, .read, .gone:
                    break
                case .ended:
                    // The task ended meanwhile: the text waits for Continue.
                    restore(t)
                case .failed(let why):
                    restore(t)
                    problem = why
                }
            } else if let why = await m.continueTask(t) {
                restore(t)
                problem = why
            } else {
                typing = false
            }
        }
    }

    /// Puts unsent text back into the field, unless the person typed anew.
    private func restore(_ t: String) {
        if text.isEmpty { text = t }
    }

    private func save(_ id: String) {
        let t = draft
        problem = nil
        changing = id
        Task {
            defer { changing = nil }
            switch await m.editMessage(id, t) {
            case .ok, .gone, .ended:
                editing = nil
            case .read:
                editing = nil
                problem = L.alreadyRead
            case .failed(let why):
                problem = why
            }
        }
    }

    private func withdraw(_ id: String) {
        problem = nil
        changing = id
        Task {
            defer { changing = nil }
            switch await m.withdraw(id) {
            case .ok, .gone, .ended:
                break
            case .read:
                problem = L.alreadyRead
            case .failed(let why):
                problem = why
            }
        }
    }
}

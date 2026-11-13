import SwiftUI

/// Pinky's memory on this iPhone, as desktop Pinky has it in Settings ›
/// Agents › Memory and the Android app in Settings › Memory: whether tasks
/// use it, and every note, to add, change and delete. The notes are the
/// core's (memory.json in its data folder), never the computer's.
struct MemoryView: View {
    @EnvironmentObject var m: AppModel
    @AppStorage("useMemory") private var useMemory = true
    @State private var notes: [Memory] = []
    @State private var problem: String?
    @State private var editing: Editing?
    @State private var confirmAll = false

    /// The note being written: a new one, or one to change.
    enum Editing: Identifiable {
        case add
        case edit(Memory)
        var id: String {
            if case .edit(let n) = self { return n.id }
            return "+"
        }
    }

    var body: some View {
        Form {
            Section {
                Toggle("Use memory", isOn: $useMemory)
            } header: {
                Text("What Pinky remembers for its tasks on this iPhone: what you told it, and ways of doing things you said yes to after a task. Nothing is kept without your yes, and never passwords, codes, card or ID numbers, or addresses. Pinky on a computer keeps its own.")
                    .textCase(nil).font(.footnote).padding(.bottom, 8)
            } footer: {
                Text("Tasks read these notes and may suggest new ones. Turned off, the notes stay until you delete them.")
            }
            Section {
                ForEach(notes) { n in
                    Button { editing = .edit(n) } label: {
                        VStack(alignment: .leading, spacing: 3) {
                            Text(verbatim: n.text).foregroundStyle(.primary)
                            Text(verbatim: L.memorySource(kind: n.kind, app: n.app)).font(.footnote).foregroundStyle(.secondary)
                        }
                    }
                    .swipeActions {
                        Button("Delete", role: .destructive) { Task { await run(["cmd": "memory_delete", "id": n.id]) } }
                    }
                    .contextMenu {
                        Button("Edit") { editing = .edit(n) }
                        Button("Delete", role: .destructive) { Task { await run(["cmd": "memory_delete", "id": n.id]) } }
                    }
                }
                Button("Add…") { editing = .add }
                    .disabled(problem != nil || notes.count >= Memory.most)
                if !notes.isEmpty {
                    Button("Delete All", role: .destructive) { confirmAll = true }
                }
            } header: {
                Text(String(localized: "memory.notes", defaultValue: "Remembered", comment: "Memory: the notes' heading"))
            } footer: {
                if let problem {
                    Text(verbatim: problem).foregroundStyle(.red)
                } else if notes.isEmpty {
                    Text("Nothing yet. After a task, Pinky may suggest something to remember; you decide.")
                }
            }
        }
        .navigationTitle("Memory")
        .navigationBarTitleDisplayMode(.inline)
        .task { await run(["cmd": "memories"]) }
        .sheet(item: $editing) { e in
            NoteEditor(editing: e) { text in
                switch e {
                case .add: await save(["cmd": "memory_add", "text": text])
                case .edit(let n): await save(["cmd": "memory_edit", "id": n.id, "text": text])
                }
            }
        }
        .confirmationDialog("Delete all of Pinky's memory?", isPresented: $confirmAll, titleVisibility: .visible) {
            Button("Delete All", role: .destructive) { Task { await run(["cmd": "memory_delete_all"]) } }
        } message: {
            Text("Every note on this iPhone is deleted. Pinky on a computer keeps its own.")
        }
    }

    /// Reads or changes the notes; the reply holds them as they are now.
    private func run(_ req: [String: Any]) async {
        switch await m.memory(req) {
        case .success(let list):
            notes = list
            problem = nil
        case .failure(let r):
            problem = r.code == "no_device" ? L.refused("no_device", nil)
                : String(localized: "Couldn't read the memory. Pinky may still be starting.", comment: "Memory: the notes could not be read")
        }
    }

    /// Adds or changes a note: nil when it was saved, else why not.
    private func save(_ req: [String: Any]) async -> String? {
        switch await m.memory(req) {
        case .success(let list):
            notes = list
            problem = nil
            return nil
        case .failure(let r):
            return L.memoryError(r.code)
        }
    }
}

/// A note being written, with why the core did not save it.
private struct NoteEditor: View {
    let editing: MemoryView.Editing
    let save: (String) async -> String?
    @Environment(\.dismiss) private var dismiss
    @State private var text = ""
    @State private var error: String?
    @State private var saving = false
    @FocusState private var focused: Bool

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("For example: Prefers window seats", text: $text, axis: .vertical)
                        .lineLimit(2...6)
                        .focused($focused)
                        .onChange(of: text) { error = nil }
                } footer: {
                    if let error { Text(verbatim: error).foregroundStyle(.red) }
                }
            }
            .navigationTitle(isNew ? LocalizedStringKey("Add…") : LocalizedStringKey("Edit"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") {
                        saving = true
                        Task {
                            error = await save(text)
                            saving = false
                            if error == nil { dismiss() }
                        }
                    }
                    .disabled(saving || text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
            .onAppear {
                if case .edit(let n) = editing { text = n.text }
                focused = true
            }
        }
        .presentationDetents([.medium])
    }

    private var isNew: Bool {
        if case .add = editing { return true }
        return false
    }
}

/// The ended task's question: whether to remember each note it suggested,
/// as desktop Pinky and the Android app ask under how a task ended.
struct SuggestionsView: View {
    @EnvironmentObject var m: AppModel
    let list: [Suggestion]
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Remember for later tasks?").font(.subheadline.weight(.semibold))
            ForEach(Array(list.enumerated()), id: \.offset) { i, s in
                VStack(alignment: .leading, spacing: 4) {
                    Text(verbatim: s.text).font(.callout).textSelection(.enabled)
                    HStack {
                        Text(verbatim: L.memorySource(kind: s.kind, app: s.app)).font(.footnote).foregroundStyle(.secondary)
                        Spacer()
                        if s.kept == true {
                            Text("Remembered").font(.footnote).foregroundStyle(Color.pinkyGreen)
                        } else if s.dropped == true {
                            Text("Not remembered").font(.footnote).foregroundStyle(.secondary)
                        } else {
                            Button("Don't") { decide(i, keep: false) }.buttonStyle(.bordered)
                            Button("Remember") { decide(i, keep: true) }.buttonStyle(.borderedProminent)
                        }
                    }
                    .controlSize(.small)
                }
            }
            if let error { Text(verbatim: error).font(.footnote).foregroundStyle(.red) }
        }
        .padding(.vertical, 4)
        .onChange(of: list) { error = nil }
    }

    private func decide(_ i: Int, keep: Bool) {
        Task { error = await m.decideNote(i, keep: keep) }
    }
}

import SwiftUI

/// The tasks that ended, newest first, by day, 20 at a time: Activity, as
/// the Android app and desktop Pinky have it.
struct ActivityView: View {
    @EnvironmentObject var m: AppModel
    @State private var tasks: [PastTask] = []
    @State private var more = false
    @State private var loading = false
    @State private var failed = false
    @State private var loaded = false

    var body: some View {
        List {
            ForEach(days, id: \.day) { d in
                Section(d.day) {
                    ForEach(d.tasks) { t in
                        NavigationLink { PastTaskView(task: t) } label: { PastTaskRow(task: t, showDay: false) }
                    }
                }
            }
            if more {
                Section {
                    Button { Task { await load(after: tasks.last) } } label: {
                        HStack {
                            Text("Show more")
                            if loading { Spacer(); ProgressView() }
                        }
                    }
                    .disabled(loading)
                }
            }
        }
        .overlay {
            if loaded && tasks.isEmpty {
                ContentUnavailableView {
                    Label(failed ? LocalizedStringKey("Couldn't read Activity.") : "No finished tasks yet.", systemImage: "clock.arrow.circlepath")
                }
            }
        }
        .navigationTitle("Activity")
        .navigationBarTitleDisplayMode(.inline)
        .task { if !loaded { await load(after: nil) } }
        .refreshable { await load(after: nil) }
    }

    /// The tasks by the day they started, in order.
    private var days: [(day: String, tasks: [PastTask])] {
        var out: [(day: String, tasks: [PastTask])] = []
        for t in tasks {
            let d = L.day(t.started)
            if out.last?.day == d { out[out.count - 1].tasks.append(t) } else { out.append((d, [t])) }
        }
        return out
    }

    private func load(after last: PastTask?) async {
        loading = true
        defer { loading = false; loaded = true }
        guard let page = await m.history(before: last?.started_ms) else {
            failed = last == nil
            return
        }
        failed = false
        tasks = last == nil ? page.tasks : tasks + page.tasks.filter { t in !tasks.contains { $0.id == t.id } }
        more = page.more
    }
}

/// A task in Activity: its words, how it ended in a word in its color,
/// when, and what it used.
struct PastTaskRow: View {
    let task: PastTask
    var showDay = true

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(task.title).lineLimit(2)
            // How it ended and when on one line, what it used on the next:
            // on one line they no longer fit a phone's width.
            HStack(spacing: 6) {
                Text(verbatim: L.outcome(task.outcome))
                    .foregroundStyle(L.tone(outcome: task.outcome).color)
                    .fontWeight(.medium)
                Text(verbatim: "·")
                Text(verbatim: when)
            }
            .lineLimit(1)
            .font(.footnote.monospacedDigit())
            .foregroundStyle(.secondary)
            if let a = task.agent {
                Text(verbatim: L.usage(a))
                    .lineLimit(1)
                    .font(.footnote.monospacedDigit())
                    .foregroundStyle(.tertiary)
            }
        }
        .padding(.vertical, 2)
    }

    private var when: String {
        let time = task.started.formatted(date: .omitted, time: .shortened)
        return showDay ? L.day(task.started, short: true) + " " + time : time
    }
}

/// One task of Activity: its words, how it ended and its steps.
struct PastTaskView: View {
    @EnvironmentObject var m: AppModel
    let task: PastTask
    @State private var full: PastTask?
    @State private var gone = false

    var body: some View {
        let t = full ?? task
        List {
            Section {
                Text(t.agent?.task ?? t.title).font(.body)
                Text(verbatim: L.day(t.started) + " " + t.started.formatted(date: .omitted, time: .shortened))
                    .font(.footnote).foregroundStyle(.secondary)
            }
            Section {
                if let a = t.agent {
                    let e = L.end(a)
                    Label { Text(verbatim: e.title).font(.headline).foregroundStyle(e.tone.color) } icon: {
                        Image(systemName: e.tone.symbol).foregroundStyle(e.tone.color)
                    }
                    if !e.reason.isEmpty { Text(e.reason) }
                    Text(verbatim: L.usage(a)).font(.footnote.monospacedDigit()).foregroundStyle(.secondary)
                } else {
                    let tone = L.tone(outcome: t.outcome)
                    Label { Text(verbatim: L.outcome(t.outcome)).font(.headline).foregroundStyle(tone.color) } icon: {
                        Image(systemName: tone.symbol).foregroundStyle(tone.color)
                    }
                }
            }
            if gone {
                Section { Text("This task is no longer in Activity.").foregroundStyle(.secondary) }
            } else if let steps = full?.steps, !steps.isEmpty {
                Section("Steps") {
                    ForEach(Array(steps.enumerated()), id: \.offset) { _, s in StepRow(step: s) }
                }
            } else if full == nil {
                Section { ProgressView().frame(maxWidth: .infinity) }
            }
        }
        .navigationTitle("Activity")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            switch await m.pastTask(task.id) {
            case .task(let t): full = t
            case .gone: gone = true
            case .failed: full = task
            }
        }
    }
}

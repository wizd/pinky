import Foundation

/// Words the app and its extensions build in code, from the string catalog
/// (Localizable.xcstrings). SwiftUI's literal texts come from the same
/// catalog by themselves.
enum L {
    /// "3 steps", with the plural the language needs.
    static func steps(_ n: Int) -> String {
        String(localized: "\(n) steps", comment: "How many steps a task took")
    }

    static var asks: String { String(localized: "Pinky asks", comment: "Notification title: an approval waits") }
    static var openToAnswer: String {
        String(localized: "Open Pinky to answer.", comment: "Notification body, after the approval's title")
    }

    static var pairingFailed: String {
        String(localized: "Pinky could not start pairing. Check that Wi-Fi is on and Pinky may use the local network.",
               comment: "Pairing on the phone could not start")
    }
    static var pairingCodeTitle: String {
        String(localized: "Pinky's pairing code", comment: "Notification title while pairing on the phone")
    }
    /// The notification with the code to type in Settings.
    static func pairingCodeBody(_ code: String) -> String {
        String(localized: "Type \(code) on the iPhone to pair it with Pinky.", comment: "Notification body while pairing on the phone")
    }

    /// The VPN went down and the app turned it on again.
    static func vpnRevived(_ why: String) -> String {
        String(localized: "Pinky's VPN stopped (\(why)), so Pinky turned it on again.", comment: "The VPN went down by itself; the reason from iOS")
    }
    /// The VPN went down again soon after: the person turns it on.
    static func vpnDown(_ why: String) -> String {
        String(localized: "Pinky's VPN stopped again (\(why)). Turn it on when you want Pinky to work.", comment: "The VPN went down by itself twice; the reason from iOS")
    }

    static var profileTitle: String {
        String(localized: "Pinky stops opening soon", comment: "Notification title: the app's signature runs out in two days")
    }
    /// The app's signature runs out on date.
    static func profileEnds(_ date: Date) -> String {
        let d = date.formatted(date: .abbreviated, time: .shortened)
        return String(localized: "Pinky stops opening on this iPhone on \(d), when its signature runs out. Install it again from Pinky on your computer before then.",
                      comment: "The app's provisioning profile runs out; the date and time")
    }

    // MARK: Amounts and times, as desktop Pinky and the Android app say them

    /// An amount in US dollars: US$0.05.
    static func dollars(_ v: Double) -> String {
        let n = String(format: "%.2f", locale: Locale(identifier: "en_US_POSIX"), v)
        return String(localized: "US$\(n)", comment: "An amount in US dollars; the number has two decimals")
    }

    /// A duration: 42 s, 3 min 05 s, 1 h 02 min.
    static func elapsed(_ seconds: Int) -> String {
        let s = max(0, seconds)
        if s < 60 { return String(localized: "\(s) s", comment: "A duration in seconds") }
        if s < 3600 {
            return String(localized: "\(s / 60) min \(two(s % 60)) s", comment: "A duration in minutes and seconds")
        }
        return String(localized: "\(s / 3600) h \(two(s % 3600 / 60)) min", comment: "A duration in hours and minutes")
    }

    static func minutes(_ n: Int) -> String {
        String(localized: "\(n) minutes", comment: "A number of minutes, as a limit")
    }

    static func tokens(_ n: Int) -> String {
        String(localized: "\(n.formatted()) tokens", comment: "A number of tokens a model read and wrote")
    }

    /// A task's cost limit: in dollars for a model whose prices Pinky
    /// knows, else in tokens.
    static func costLimit(priced: Bool, maxCost: Double) -> String {
        priced ? dollars(maxCost) : tokens(Int(maxCost * 1_000_000))
    }

    /// What a task used: 4 steps · US$0.05 · 9 s.
    static func usage(_ a: AgentState) -> String {
        var parts = [steps(a.steps)]
        if a.priced == true {
            parts.append(dollars(a.cost))
        } else if a.tokens > 0 {
            parts.append(tokens(a.tokens))
        }
        parts.append(elapsed(a.seconds))
        return parts.joined(separator: " · ")
    }

    // MARK: How a task ended

    /// How an ended task reads: green, grey or amber.
    enum Tone { case done, stopped, failed }

    /// How a task ended (pilot.End): a title, why (which may be empty), and
    /// its tone.
    static func end(_ a: AgentState) -> (title: String, reason: String, tone: Tone) {
        let summary = a.summary ?? ""
        switch a.end ?? "" {
        case "done":
            return (outcome("done"), summary, .done)
        case "stopped":
            return (outcome("stopped"), "", .stopped)
        case "steps":
            let n = a.max_steps ?? 0
            return (outcome("stopped"), String(localized: "Reached the limit of \(n) steps.", comment: "Why a task stopped"), .stopped)
        case "time":
            let n = a.max_minutes ?? 0
            return (outcome("stopped"), String(localized: "Reached the limit of \(n) minutes.", comment: "Why a task stopped"), .stopped)
        case "cost":
            let limit = costLimit(priced: a.priced == true, maxCost: a.max_cost ?? 0)
            return (outcome("stopped"), String(localized: "Reached the limit of \(limit).", comment: "Why a task stopped: the cost limit, in dollars or tokens"), .stopped)
        case "refused":
            return (outcome("failed"), String(localized: "The model declined this task.", comment: "Why a task did not finish"), .failed)
        case "no_key":
            return (outcome("not_started"), String(localized: "No API key is saved. Add one in Settings.", comment: "Why a task did not start"), .failed)
        case "bad_key":
            return (outcome("not_started"), String(localized: "The service didn't accept the API key. Check it in Settings.", comment: "Why a task did not start"), .failed)
        default:
            return (outcome("failed"), summary, .failed)
        }
    }

    /// How a task ended, in one line.
    static func endLine(_ a: AgentState) -> String {
        let e = end(a)
        if e.reason.isEmpty { return e.title }
        return String(localized: "\(e.title). \(e.reason)", comment: "How a task ended, then why")
    }

    /// How a task in Activity ended, in a word (standalone.PastTask's outcome).
    static func outcome(_ o: String) -> String {
        switch o {
        // A key of its own: the button Done has other words in Chinese.
        case "done": String(localized: "end.done", defaultValue: "Done", comment: "A task ended: done")
        case "stopped": String(localized: "Stopped", comment: "A task ended: the person stopped it, or it reached a limit")
        case "not_started": String(localized: "Couldn't start", comment: "A task ended: it never started (the API key)")
        default: String(localized: "Couldn't finish", comment: "A task ended: failed or declined")
        }
    }

    static func tone(outcome o: String) -> Tone {
        switch o {
        case "done": .done
        case "stopped": .stopped
        default: .failed
        }
    }

    /// The day of a time, as Activity heads its days: Today, Yesterday,
    /// else its date, with the weekday this year and with the year before.
    /// short leaves the weekday out, for a row.
    static func day(_ d: Date, short: Bool = false) -> String {
        let cal = Calendar.current
        if cal.isDateInToday(d) { return String(localized: "Today", comment: "A day in Activity") }
        if cal.isDateInYesterday(d) { return String(localized: "Yesterday", comment: "A day in Activity") }
        let thisYear = cal.isDate(d, equalTo: .now, toGranularity: .year)
        switch (short, thisYear) {
        case (true, true): return d.formatted(.dateTime.month(.abbreviated).day())
        case (true, false): return d.formatted(.dateTime.year().month(.abbreviated).day())
        case (false, true): return d.formatted(.dateTime.weekday(.wide).month(.wide).day())
        case (false, false): return d.formatted(.dateTime.year().month(.wide).day())
        }
    }

    private static func two(_ n: Int) -> String { String(format: "%02d", n) }

    /// What the person must do while the phone is not ready, by the core's
    /// reason (onphone.Reason*).
    static func reason(_ r: String?) -> String {
        switch r {
        case "vpn_off": String(localized: "Pinky's VPN is off. Turn it on to let Pinky reach this iPhone.", comment: "Phone not ready")
        case "not_paired": String(localized: "Pinky is not paired with this iPhone yet.", comment: "Phone not ready")
        case "unpaired": String(localized: "This iPhone no longer trusts Pinky's pairing. Pair again.", comment: "Phone not ready")
        case "failed": String(localized: "Pinky cannot reach this iPhone.", comment: "Phone not ready")
        default: String(localized: "Connecting to this iPhone…", comment: "Phone not ready")
        }
    }

    // MARK: The person's messages to Pinky (steer, withdraw, continue)

    /// A message to Pinky the core did not take.
    static func notSent(_ error: String?) -> String {
        let why = error ?? String(localized: "Something went wrong.", comment: "Unknown error")
        return String(localized: "Couldn't send: \(why)", comment: "A message to Pinky failed; the reason")
    }

    static var alreadyRead: String {
        String(localized: "Pinky has already read it.", comment: "A message to Pinky can no longer be edited or withdrawn")
    }

    /// A continue the core refused, by its code: as a start for the codes
    /// they share.
    static func continueRefused(_ code: String?, _ error: String?) -> String {
        switch code {
        case "no_device", "no_key", "no_model", "busy", "computer": refused(code, error)
        default: notSent(error)
        }
    }

    static var voiceNotAllowed: String {
        String(localized: "Pinky may not use speech recognition or the microphone. You can allow them in Settings › Privacy & Security.",
               comment: "Voice input in the task box: the person declined a permission")
    }
    static var voiceUnavailable: String {
        String(localized: "Speech recognition is not available now.", comment: "Voice input in the task box could not start")
    }

    // MARK: Memory

    /// Where a note came from: the person, or a task in an app.
    static func memorySource(kind: String, app: String?) -> String {
        guard kind == "learned" else { return String(localized: "From you", comment: "Memory: a note the person said or wrote") }
        if let app, !app.isEmpty {
            return String(localized: "Learned in \(app)", comment: "Memory: a note Pinky learned on a task; the app's name")
        }
        return String(localized: "Learned on a task", comment: "Memory: a note Pinky learned on a task")
    }

    /// Why the core kept no note, by its code (standalone's CodeMemory*).
    static func memoryError(_ code: String?) -> String {
        switch code {
        case "memory_sensitive":
            String(localized: "Memory never keeps passwords, codes, card, account, ID or phone numbers, email or street addresses.", comment: "Memory: a note was not saved")
        case "memory_long":
            String(localized: "A note has at most \(Memory.maxChars) characters.", comment: "Memory: a note was not saved; the most characters")
        case "memory_full":
            String(localized: "Memory keeps at most \(Memory.most) notes. Delete one first.", comment: "Memory: a note was not saved; the most notes")
        default:
            String(localized: "Couldn't save the note.", comment: "Memory: a note was not saved")
        }
    }

    /// A start or decide the core refused, by its code.
    static func refused(_ code: String?, _ error: String?) -> String {
        switch code {
        case "no_device": String(localized: "Pinky is not connected to this iPhone yet.", comment: "Start refused")
        case "no_key": String(localized: "Add the model's API key in Settings first.", comment: "Start refused")
        case "no_task": String(localized: "Type a task first.", comment: "Start refused")
        case "no_model": String(localized: "Choose a model in Settings first.", comment: "Start refused")
        case "busy": String(localized: "Pinky is already working on a task.", comment: "Start refused")
        case "shared": String(localized: "What came with the task is too long for Pinky.", comment: "Start refused: the text a shortcut passed along is too large")
        case "computer": String(localized: "Pinky on your computer is using this iPhone. Start the task when it is done, or once you choose Pause connection for this iPhone there.", comment: "Start refused")
        case "gone": String(localized: "This was already answered, or the time to answer ran out.", comment: "Decide refused")
        default: error ?? String(localized: "Something went wrong.", comment: "Unknown error")
        }
    }
}

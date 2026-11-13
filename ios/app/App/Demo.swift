#if DEBUG
import Foundation

/// Canned states for looking at the screens in the Simulator, which runs
/// no packet tunnel and so never gets a core of its own: a debug build
/// launched with -pinkyDemo <scenario> shows that scenario and talks to no
/// core (ios/README.md). Scenarios: setup (unpaired), model (the last setup
/// step), ready, running, asks, ended, computer, steer (running, with a
/// message the model read and one it has not), continue (stopped at a
/// limit with a message left unread, so it can go on).
enum Demo {
    static let scenario = UserDefaults.standard.string(forKey: "pinkyDemo")

    /// Whether the scenario has the model's key: all but the setup ones.
    static var hasKey: Bool { !["setup", "model"].contains(scenario ?? "") }
    static var paired: Bool { scenario != "setup" }

    private static let zh = Locale.preferredLanguages.first?.hasPrefix("zh") == true
    private static func w(_ en: String, _ zh: String) -> String { Demo.zh ? zh : en }

    private static let now = Int64(Date.now.timeIntervalSince1970 * 1000)
    private static let minute: Int64 = 60_000

    static var state: CoreState? {
        guard let s = scenario, s != "setup" else { return nil }
        var st = CoreState(device: DeviceState(ready: true, name: "iPhone"), preset: 1, computer: nil, agent: nil,
                           task: nil, needs: [])
        let task = w("Work out 128 × 46 in Calculator and tell me the result",
                     "用计算器算一下 128 × 46，告诉我结果")
        let steps = [
            Step(text: w("Open Calculator", "打开计算器"), status: "settled", ms: 900),
            Step(text: w("Tap “1”, “2”, “8”", "点按“1”“2”“8”"), status: "settled", ms: 1400),
            Step(text: w("Tap “×”", "点按“×”"), status: "settled", ms: 500),
            Step(text: w("Tap “4”, “6”", "点按“4”“6”"), status: "settled", ms: 800),
            Step(text: w("Tap “=”", "点按“=”"), status: s == "ended" ? "settled" : "running", ms: 0),
        ]
        var agent = AgentState(task: task, model: "claude-opus-5-5", running: true, end: nil, summary: nil,
                               steps: 5, cost: 0.06, tokens: 41_000, seconds: 23, priced: true,
                               max_steps: 100, max_minutes: 20, max_cost: 2, token_limit: 0)
        let said = Step(text: w("You said: “Use the calculator's history too”", "You said: “也看看计算器的历史记录”"),
                        status: "settled", ms: 0)
        let unread = PendingMessage(id: "m2", text: w("Then copy the result", "然后把结果复制下来"), ms: now - 5000)
        switch s {
        case "running":
            st.agent = agent
            st.task = TaskState(title: task, state: "working", steps: steps)
        case "steer":
            agent.pending = [unread]
            st.agent = agent
            st.task = TaskState(title: task, state: "working", steps: Array(steps.prefix(2)) + [said] + steps.dropFirst(2))
        case "continue":
            agent.running = false
            agent.end = "steps"
            agent.max_steps = 5
            agent.pending = [unread]
            agent.continuable = true
            st.agent = agent
            st.task = TaskState(title: task, state: "stopped", steps: Array(steps.prefix(2)) + [said] + steps.dropFirst(2))
        case "asks":
            let pay = w("Send the message to Mom", "给妈妈发送这条消息")
            agent.task = w("Tell Mom I'll be home by seven", "告诉妈妈我七点前到家")
            agent.steps = 7
            st.agent = agent
            st.task = TaskState(title: agent.task, state: "working", steps: [
                Step(text: w("Open Messages", "打开信息"), status: "settled", ms: 900),
                Step(text: w("Open the chat with Mom", "打开和妈妈的对话"), status: "settled", ms: 1100),
                Step(text: w("Type “I'll be home by seven”", "输入“我七点前到家”"), status: "settled", ms: 1300),
                Step(text: w("Waiting for you: tap “Send”", "等你处理：点按“发送”"), status: "approval_pending", ms: 0),
            ])
            st.needs = [Need(id: "demo", kind: "approval", title: pay,
                             detail: w("Messages · “I'll be home by seven”", "信息 ·“我七点前到家”"),
                             app: "Messages", rule: w("Sending messages", "发送消息"),
                             deadline_ms: now + 5 * minute, allow_task: true)]
        case "ended":
            agent.running = false
            agent.end = "done"
            agent.steps = 6
            agent.seconds = 31
            agent.summary = w("128 × 46 = 5,888. Calculator shows the result.", "128 × 46 = 5888，计算器上显示了结果。")
            agent.suggested = [
                Suggestion(kind: "learned", text: w("Clear with AC before a new sum", "新算式前先按 AC 清零"), app: w("Calculator", "计算器")),
            ]
            st.agent = agent
            st.task = TaskState(title: task, state: "done", steps: steps)
        case "computer":
            st.computer = ComputerState(task: w("Check the weather for tomorrow", "查一下明天的天气"), stoppable: false)
        default:
            break
        }
        return st
    }

    /// The core's answer to a request, from the canned Activity.
    static func reply(_ req: [String: Any]) -> CoreReply {
        switch req["cmd"] as? String {
        case "history":
            if let id = req["id"] as? String {
                guard var t = history.first(where: { $0.id == id }) else { return CoreReply(ok: false, code: "gone") }
                t.steps = [
                    Step(text: w("Open Calculator", "打开计算器"), status: "settled", ms: 900),
                    Step(text: w("Tap “=”", "点按“=”"), status: "settled", ms: 500),
                ]
                return CoreReply(ok: true, history: [t])
            }
            let before = req["before_ms"] as? Int64 ?? 0
            let limit = req["limit"] as? Int ?? 20
            let list = history.filter { before == 0 || $0.started_ms < before }
            return CoreReply(ok: true, history: Array(list.prefix(limit)), more: list.count > limit)
        case "steer" where req["id"] == nil:
            // The canned state does not change: the message shows nowhere.
            return CoreReply(ok: true, id: "demo-m")
        case "memories":
            return CoreReply(ok: true, memories: [
                Memory(id: "1", kind: "said", text: w("Prefers window seats", "喜欢靠窗的座位")),
                Memory(id: "2", kind: "learned", text: w("Orders are under 「我的」, then 「全部订单」", "订单在「我的」›「全部订单」"), app: w("Meituan", "美团")),
            ])
        default:
            return CoreReply(ok: true)
        }
    }

    /// What the model used: today, yesterday and five days ago.
    static var usage: [UsageRow] {
        guard hasKey else { return [] }
        return [
            UsageRow(day: UsageRow.day(back: 0), service: "anthropic", model: "claude-opus-5-5", requests: 9,
                     input: 36_500, output: 1_900, cache_read: 2_600, cost: 0.2153),
            UsageRow(day: UsageRow.day(back: 1), service: "anthropic", model: "claude-opus-5-5", requests: 14,
                     input: 58_200, output: 3_100, cost: 0.3685),
            UsageRow(day: UsageRow.day(back: 1), service: "deepseek", model: "deepseek-flash", requests: 6,
                     input: 22_400, output: 900, cost: 0.0068),
            UsageRow(day: UsageRow.day(back: 5), service: "ollama", model: "qwen3-vl", requests: 3,
                     input: 9_800, output: 400, unpriced: 10_200),
        ]
    }

    private static var history: [PastTask] {
        let rows: [(String, String, String, Int64, Int, Double, Int)] = [
            (w("Work out 12 × 34 in Calculator", "用计算器算 12 × 34"), "done", "done", 12, 6, 0.05, 19),
            (w("Set a timer for 25 minutes", "设一个 25 分钟的计时器"), "done", "done", 95, 5, 0.04, 14),
            (w("Turn on Do Not Disturb until tomorrow morning", "开启勿扰模式到明天早上"), "stopped", "stopped", 240, 3, 0.02, 9),
            (w("Add an alarm for 7:30 on weekdays", "添加工作日 7:30 的闹钟"), "done", "done", 1500, 8, 0.07, 27),
            (w("Find the photo of the receipt from Friday", "找到周五那张收据的照片"), "failed", "failed", 1600, 12, 0.11, 48),
            (w("Check how much storage is left", "看看手机还剩多少存储空间"), "done", "done", 4400, 4, 0.03, 11),
            (w("Convert 250 US dollars to euros", "把 250 美元换算成欧元"), "done", "done", 4500, 5, 0.04, 16),
        ]
        return rows.enumerated().map { i, r in
            let start = now - r.3 * minute
            let a = AgentState(task: r.0, model: "claude-opus-5-5", running: false, end: r.2,
                               summary: nil, steps: r.4, cost: r.5, tokens: 0, seconds: r.6, priced: true,
                               max_steps: 100, max_minutes: 20, max_cost: 2, token_limit: 0)
            return PastTask(id: "demo-\(i)", title: r.0, state: r.1, outcome: r.2, started_ms: start,
                            ended_ms: start + Int64(r.6) * 1000, agent: a)
        }
    }
}
#endif

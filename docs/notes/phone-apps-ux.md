# Pinky on the phone: one experience on iPhone and Android

Started on 2026-11-13. Pinky's iPhone app (`ios/app`, [iphone-app.md](iphone-app.md)) and its Android app (`android/companion`, [android-app.md](android-app.md)) grew apart: different screens, words and states for the same things. This note is the design both now follow, and what was built for it. Each app keeps its platform's conventions (SwiftUI lists and sheets on the iPhone, Material-style cards drawn by `Look` on Android); the structure, the states and the words are the same.

## Names

| Where | Name |
| --- | --- |
| Home screen, the app's title, Android's settings and dialogs | **Pinky** on both (Pinky (dev) for Android's debug build) |
| Desktop Pinky, when it speaks of the app | **Pinky on the phone**; **Pinky on the iPhone** or **Pinky on {phone}** where it must say which (「手机上的 Pinky」「iPhone 上的 Pinky」, 「スマホの Pinky」「iPhone の Pinky」) |
| Before | Pinky for iPhone, Pinky for Android (「iPhone 版 Pinky」「Android 版 Pinky」), and Android's second launcher entry, Pinky tasks |

Bundle and package ids, `pinkyctl helper install`, `connection: helper` and the Go names do not change. Pinky Helper stays the name of the older iOS runner where desktop Pinky means that one.

Android had two launcher entries in debug builds (the debug page and Pinky tasks) and only the debug page in releases. Now both builds have one, Pinky, which opens the task screen, and the service's settings link in Android's Accessibility settings opens the task screen too; the debug page is reached with `am start -n <package>/app.pinky.companion.MainActivity`.

## One structure

```
┌───────────────────────────────┐
│ Pinky                 🕘  ⚙   │  title; Activity and Settings
├───────────────────────────────┤
│ [status card]                 │  at most one: set up / not ready /
│                               │  computer / running / ended
├───────────────────────────────┤
│ [Pinky asks]                  │  iPhone: in the app (and a notification);
│                               │  Android: the card over other apps
├───────────────────────────────┤
│ ┌───────────────────────────┐ │
│ │ What should Pinky do on   │ │  the task box, while Pinky is ready
│ │ this phone?               │ │
│ └───────────────────────────┘ │
│ Claude Opus 5.5        [Start]│  the model, the limit
│ Up to US$1.00 a task          │
├───────────────────────────────┤
│ STEPS                         │  the task shown above
│ ✓ Open Calculator             │
├───────────────────────────────┤
│ ACTIVITY                      │  five latest tasks that ended,
│ Work out 12 × 34   Done       │  Show all
└───────────────────────────────┘
```

### The status card's states

One set of states, in this order of precedence; the card shows the first that holds.

| State | When | Title | Body | Actions |
| --- | --- | --- | --- | --- |
| Set up | A setup step is left | Set up Pinky | The steps, numbered, done ones ticked, the next one with its button | The next step's button |
| Not ready | Set up, but the phone cannot be reached | Starting Pinky… / Pinky can't reach this phone | What to do | (iPhone: Turn on, when the VPN is off) |
| Computer | Pinky on a computer holds the phone | Pinky on your computer is using this phone | Its task; "You can start a task here when it's done." (iPhone adds Pause connection) | Stop, when the computer's claim can be stopped |
| Running | A task runs | Pinky is working on it (a green dot) | The task; steps, cost, time; "Waiting for you: …" in amber while an approval waits; the person's messages Pinky has not read yet ("Pinky reads this at its next step") | Stop; a field "Add or change instructions…" with Send; Edit and Withdraw on each unread message |
| Ended | The last task ended and was not dismissed | Done / Stopped / Couldn't finish / Couldn't start, with the end's color | The model's summary or why; the task; what it used; messages Pinky never read, when there are any | Dismiss (the task box stays below it); when it can go on (`continuable`), a field "Tell Pinky what to do next…" with Continue: the same task carries on, its limits counted anew |

### Setup steps

| | iPhone | Android |
| --- | --- | --- |
| 1 | Pair Pinky with this iPhone (Pair: on iOS 27 in Settings with a code; older: from a computer) | Turn on Pinky in Accessibility settings (with the restricted-settings hint from Android 13) |
| 2 | Turn on Pinky's VPN (Turn on) | — |
| last | Choose the AI model and add its API key (Add an API key, or Choose a model once the key is saved) | the same |
| after | — | Keep Pinky running (battery, autostart): a card of its own once set up, with Later |

### Words

Both apps use desktop Pinky's words (`cmd/pinky/locales`: `ask*`, `needs*`, `activity*`), which the Android app had already. The iPhone app's own words changed to them:

| Was (iPhone) | Now (both) |
| --- | --- |
| History | Activity |
| Working · 3 steps · 12 s · $0.01 | Pinky is working on it · 3 steps · US$0.01 · 12 s |
| Pinky finished / Pinky stopped / Pinky could not do it / Pinky reached its limit | Done / Stopped / Couldn't finish (+ the limit it reached) |
| Allow once / Allow for task / Deny | Allow / Allow for this task / Deny |
| Done, hand back / Decline | Done / Decline |
| Pinky waits for you / Pinky is working (Live Activity) | Waiting for you / Pinky is working on it |
| Provider / Address | Service / API address |
| Ask me before | When should Pinky ask you? |
| Connection | This iPhone (Android: This phone) |

### Settings

The same sections in the same order: **AI model** (Service, API key, Model, API address) · **Limits** (steps, time, cost per task) · **When should Pinky ask you?** (three choices, each with what it means) · **This phone** (iPhone: the VPN and where the core runs; Android: the accessibility service, battery, autostart, App info) · **About** (the version; Android: Check for updates).

### Activity

The tasks that ended, newest first, grouped by day (Today, Yesterday, then the date), 20 at a time with Show more. A row: the task (two lines), how it ended in a word in its color (Done green, Stopped grey, the rest amber), its time and what it used. A task opens with its words, the end card and its steps. Both cores answer the same `history` command (standalone's `PastTask`): the iPhone's core used the gateway's history without the agent's end; it now answers as Android's does. The iPhone's search went with it (neither app has search now).

### Look

Desktop Pinky's palette on both: the accent `#5450D6` (dark `#9895FF`), green for done and running, amber for what waits for the person, red only for Stop and errors. The status card is the one tinted card on the screen; the rest is grouped rows. On the iPhone, an inset-grouped list with the status card as its first section; on Android, `Look`'s cards and groups.

Pinky's mark, the app icon's artwork (`assets/icon/pinky-ios-1024.png`, Android's launcher layers), stands before the title Pinky on the task screen, in a rounded square, on both apps; the other screens keep a plain title. On the iPhone it sits in the large title from iOS 26 (`TitleWithMark`, the `largeTitle` toolbar placement); once the list scrolls, the inline title is the word alone. Before iOS 26 the mark leads the bar above the large title. On Android `Look.header(…, mark = true)` draws the launcher icon's two layers (`Look.mark`) in the same shape whatever mask the launcher uses. The iPhone app had no icon of its own before 2026-11-13, so the home screen, its notifications and its Live Activity showed a blank one; `App/Assets.xcassets` now holds it (`AppIcon`) and the mark (`PinkyMark`), and the kit desktop Pinky builds from carries them. Android's launcher and notification icons were already Pinky's. The Live Activity, the Dynamic Island and Android's pill keep their state icons (a hand, amber while Pinky waits); the word Pinky and the app's icon say whose they are.

On Android the pill (Working · …, Waiting for you) floats over other apps only: while Pinky's own screen is in front it hides, so that it never covers the title or Stop (`Overlays.windowsChanged`, from the service's window events). The iPhone has no pill; the Live Activity and the Dynamic Island show the same, with Stop.

## Desktop Pinky

Desktop Pinky names the apps as the Names table says, in its three languages, and the gateway's words that the person reads (the passcode step, Switching to Pinky on the iPhone) changed with them; the UI still matches the older words from a gateway that says them. Words only agents and logs read (the drivers' errors, MCP tool descriptions and results, `pinkyctl`, the docs in `docs/mcp.md` and `docs/notes/ios.md`) say Pinky on the iPhone and Pinky on the phone too, with WebDriverAgent named where it matters; `docs/notes/ios.md` keeps its old anchors (`#installing-pinky-for-iphone`, `#when-pinky-for-iphones-signature-runs-out`, `#through-the-webdriveragent-helper`) for the links that use them. The connection value `helper` in `phone_connect` and the setup step `install_helper` keep their names.

It tells people that the app can do tasks by itself, where they look, without nagging (`ui/phonetasks.go`):

- **The phone's sidebar**: a card, Pinky on the phone can work by itself, with what to do on the phone, How to set it up (the README's [Pinky on the phone](../../README.md#pinky-on-the-phone), or the Chinese README's) and Got it. It shows for a real phone that is connected and free: on Android once Pinky's app is installed and the companion card has nothing to say, on an iPhone always, with Install Pinky on the iPhone on a Mac with Xcode. Got it puts it away for every phone, for good (`phone_tasks_seen` in `update.json`). Since the card waits for a connected phone, a new person sees it after their first connection.
- **Settings › Phones**: each real phone has a row, Tasks on the phone, with the same words and links (an Android phone once Pinky's app is on it).
- **An iPhone without the app**, such as the iPhone 13 mini after its lite build was uninstalled on 2026-11-12: on iOS 27 left on Automatic it still connects through CoreDevice, and the card and the row offer Install Pinky on the iPhone; on a phone set to Pinky on the iPhone, or below iOS 27, the phone needs setup and its page and checkup offer the same install, as before.

## What was built (2026-11-13)

- **Android** (`claude/platform-phone-apps`): one launcher entry in every build, on-phone mode in release builds too (`on_phone_ui` true everywhere), the setup card with numbered steps, icon buttons for Activity and Settings, the tinted running and computer cards, the footer under the task box, and the pill hidden over Pinky's own screen.
- **iPhone**: the screens rewritten to this note: `ContentView` (the status card, Pinky asks, the task box with the model and the limit, Steps, Activity), `ActivityView` (by day, 20 at a time, a past task with its steps), `SettingsView` (the five sections), `TaskSettings` (the model's choices and the limits, sent with each start as Android sends them) and `Look`/`Palette` (desktop Pinky's colors, shared with the Live Activity). The string catalog has every new word in English, Chinese and Japanese, with the Android app's translations.
- **Desktop Pinky**: the names in its three locales and the gateway's words, the card and the row above, and the README's section on Pinky on the phone (English and Chinese).
- **Looking at the iPhone's screens**: the Simulator runs no packet tunnel, so a debug build launched with `-pinkyDemo <scenario>` (`setup`, `model`, `ready`, `running`, `asks`, `ended`, `computer`, `steer`, `continue`) shows canned states and talks to no core (`App/Demo.swift`). Add `-AppleLanguages "(zh-Hans)"` for Chinese; the canned tasks follow the language.

## Messages and Continue (2026-11-13)

Both apps, on the core's `steer`, `withdraw` and `continue` commands ([builtin-agent.md](builtin-agent.md#messages-and-continue)):

- **While Pinky works**, under the status card: the person's messages Pinky has not read yet, oldest first, each with "Pinky reads this at its next step", Edit and Withdraw; then a one-line field "Add or change instructions…" with Send (Return sends). Edit is a dialog on Android and inline on the iPhone; saving it empty withdraws. A message Pinky read meanwhile says "Pinky has already read it."; one the task ended under moves to Continue's field.
- **Once it ended and can go on** (`agent.continuable`; not while Pinky on a computer holds the phone): the unread messages under "Pinky didn't read these before the task ended. Continue to send them.", then "Tell Pinky what to do next…" with Continue, enabled with text or with messages waiting. Continue reads the key of the service the task runs on (`agent.provider`), not the one Settings shows now, and leaves Pinky's screen as Start does. The task box for a new task stays below.
- Once read, a message is a step, "You said: “…”" (the iPhone marks it with a speech bubble in the accent color).
- While Pinky's own screen is in front, the gateway refuses the agent's steps on it, as before: a message sent from the task screen is read at the next request, and the agent acts once the person leaves.
- Checked: Android's unit tests (`SteeringTest`) and debug build; the iPhone's simulator build and its `steer` and `continue` demo scenarios in English, Chinese and Japanese. Not yet tried on a phone or with a real core end to end.

## Quick entries (2026-11-13)

Reaching Pinky's own agent without opening Pinky first, after Mika's pill:

- **Android**: an optional floating button (double tap: ask about this screen; long press: the same by voice; tap: open Pinky), Share to Pinky for text, links and pictures, two Quick Settings tiles (New task, Floating button), and a mic in the task box. See android-app.md, "Quick entries".
- **iPhone**: a mic in the task box (Apple's speech recognition, on the phone where it can) and New Task in Pinky for Shortcuts and Siri, which can carry the share sheet's text or link. No Share extension and no "this screen"; iphone-app.md, "Quick entries", says why.
- **Desktop Pinky**: About the screen in Ask Pinky starts a task about what the phone shows now (with an empty box: "Explain what the phone shows."). No voice button: MyGo has no speech API, and the OS has its own dictation (the Fn key, or Edit › Start Dictation), which types into text fields that take the system's text input; whether Ask Pinky's box takes it was not checked.

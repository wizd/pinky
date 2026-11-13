# Integration

What the main session did to put the parts together, what was checked on real phones, and what is still open. Rounds one and two were integrated in the night of 2026-11-06; round three on 2026-11-07, see [Round three](#round-three).

## Merges

The five branches merged into `main` cleanly except `go.mod` and `go.sum`, which were combined by hand and tidied: `claude/gateway-core-da93b5`, `claude/android-driver-1aff78`, `claude/android-companion-76e6fd`, `claude/ios-driver-ba7421`, `claude/desktop-client-380250`. Build, vet and every test passed after each merge.

A second round on the same branches merged cleanly too:

| Branch | Second round |
| --- | --- |
| Android driver | The companion inside the driver (reads in about 70 ms instead of 2.2 s, typing through `set_text`, toasts and notifications as events, settle on video and accessibility events), `InstallCompanion`, and `PersonWatcher` over `getevent` |
| iOS driver | Element frames from `remoteAXService` (every Calculator key has a position; OCR only as a fallback), provider checks cached, `Touch` in video pixels |
| Gateway | Runs on both real phones (device tests), Settings presets, code hiding and blocked apps in the policy, history screenshots off the agent's path, a person's scope for a new agent kept |
| Client | Handoff and New agent cards, the gateway's extras (step screenshots, agents, read-only, wiping history), `Options.Hidden`, App Nap off, `video.FrameStream`, mirror states |

## Shared contract changes

- `driver.Toucher`: points are in the pixels of the picture the device's `Stream` sends (at `MaxSize` 0), because that is all the mirror knows. Drivers whose video differs from the screen convert: an iPhone 13 mini sends 1136x2464 video for a 1125x2436 screen.
- `driver.PersonWatcher` (new, optional): a channel that fires when the person uses the phone itself. The gateway listens on every device that has it and takes over for the person (`watchPerson` → `PersonTouched`), so a running agent task pauses. `internal/demo` devices implement it for tests.
- `gateway.NeedHandoff` came from the gateway branch.

## Wiring in `cmd/pinky`

- **Providers** (`providers.go`): Android and iOS. The Android provider starts an adb server when none runs; the iOS provider is skipped when usbmuxd is missing. `PINKY_DEVICES=id1,id2` limits Pinky to those phones, for development while other tools use the rest; the Android and iOS providers then do not even ask the others what they are (`android.Provider.Limit`, `ios.Provider.Only`).
- **The iOS provider ignores Wi-Fi devices** unless `ios.Provider.Wireless` is set: the gateway opens everything it lists, and usbmuxd lists every iPad and iPhone ever paired over the network.
- **App mode** (`app.go`): `pinky` with no command runs the gateway, the agent socket and the client window in one process, so approvals are answered in the app (the socket refuses them for real phones). Settings persist in `config/settings.json`; read-only mode reaches the gateway; blocked apps shown in Settings come from the deny rules in `rules.yaml`. The log goes to `data/pinky.log` as well as stderr.
- **The MCP shim** starts the app when no gateway runs (`pinky app --background`: menu bar or tray only, the window opens when something needs the person), instead of a headless daemon that could not answer approvals for real phones. That is on macOS and Windows, and on Linux when `WAYLAND_DISPLAY` or `DISPLAY` is set; when the app cannot show itself (no display after all, missing libraries), `pinky daemon` runs instead: the shim starts it when the app exits before its gateway is up (`launchGateway`), and the app starts it as it exits when its gateway had answered the shim already, with the restart marker, so that the agent's session reconnects to the daemon (`runApp`). Fake phones and machines without a desktop get `pinky daemon`. Until 2026-11-10 Windows and Linux always got the daemon (GitHub issue #68).
- **Opening Pinky while a gateway runs** (`running.go`, `takeOver`): the socket's `version` method says what hosts the gateway (`api.Server.Kind`: `app` or `daemon`). Another app gets the `show` method, which brings its window to the front (on Windows the new launch first passes on its right to the foreground with `AllowSetForegroundWindow`), and the new one exits quietly. A daemon, or a gateway too old to say what it is, is asked to `quit`, and the app takes the socket once it is free (up to 15 s). Before it asks, the app writes the update's restart marker, so agents' `pinky mcp` reconnect to the app as after an update instead of reporting that Pinky quit. A daemon the person runs on purpose on a desktop is taken over too; the app's Settings then decide what it serves (remote agents included). A spawned daemon does not exit when its last agent leaves: the next agent would wait for a cold start, and the app takes it over anyway.
- **Errors at start are shown**: opened from its icon, Pinky has no terminal, so an error before the window opens (another Pinky that does not answer or will not quit, a socket that cannot be made) shows in a dialog with the reason and the log's path (`showStartError`), not only on stderr. Started with `--background`, as by `pinky mcp`, it only logs. `TestTakeOver` covers the daemon, a gateway without a kind, another app (shown, or not when in the background) and a daemon that will not quit; `TestWaitForGatewayExited` the shim's fallback. Checked by hand on macOS with `--fake` in a private `PINKY_HOME`: the takeover with an agent's session open across it, a second launch showing the first window, and the dialog for a stopped (`SIGSTOP`) daemon. Not tried on Windows or Linux.
- **Settings** (`app.go`): the preset, code hiding, read-only mode and blocked apps are read from the policy (`rules.yaml`, `apps.yaml`) at start and written back through its API when Settings changes them; the rest (unlock, retention, recording, developer mode) stays in `settings.json`.
- **Demo phones have video**: `internal/demo` devices encode their drawn screen with `video.FrameStream`, so `pinky --fake` has a mirror that takes touches. `SetVideo(false)` turns it off for tests.
- **`pinky companion install [phone]`** installs or updates Pinky for Android, the companion app, from the APK `android.FindCompanionAPK` finds (inside Pinky.app after `make app`). Pinky never installs it on its own; with Update automatically on, the app updates an installed, older companion while the phone is free ([updates](updates.md)).
- **Packaging**: `cmd/pinky/mygo.json` and `make app` build `Pinky.app` (binary `Contents/MacOS/Pinky`, version from `mygo.json`) and a `.dmg`; the companion APK is copied into `Contents/Resources` when built. The `.dmg` is made before that copy, so only the `.app` carries the APK.
- **Checks**: the `e2e` build tag links MyGo's automation socket (`MYGO_AUTOMATION=/path/sock`), which can read, press and capture the app's windows even while the Mac's screen is locked. `scripts/mcpcall` drives `pinky mcp` as an agent: tools in order in one session, with `$ref:LABEL` resolved from the screens seen so far.

## Fixes made here

- **The Mi 10 Pro's mirror stayed black.** Its encoder tags H.264 with the YCgCo matrix, which swscale refuses to convert, so every `Convert` failed with "Operation not supported" while VideoToolbox decoded fine. `Decoder.Convert` now retries the picture as BT.709 and keeps doing so for that stream. `TestDecodeYCgCoStream` tags a test stream with `h264_metadata=matrix_coefficients=8` to reproduce it.
- **A provider wrapper must be a pointer**: the gateway compares providers, and a struct with a map field panics in `==`.
- **Read-only from the client never reached the gateway**: the gateway's `SetReadOnly` began returning an error in the second round, and the client's optional interface still had the old signature, so its type assertion failed without a word. The interface now matches (and `cmd/pinky` applies the setting as well).
- **Status bar OCR**: on the iPhone, OCR read the clock and the signal and battery icons as text ("5:05イ", "三の") that changed between reads and showed up in every diff. The gateway now leaves OCR lines centered in the top 6% of the screen out of what agents see.

## Checked on real phones

All through `scripts/devlock.sh`, with Calculator, Settings (read only) and the home screens only; captures were deleted after viewing.

| What | Result |
| --- | --- |
| `pinky devices` with the real providers | Mi 10 Pro and the iPhone listed, both online, in 1.4 s |
| App window, iPhone | Live HEVC mirror of the home screen |
| App window, Mi 10 Pro | Live H.264 mirror, correct colors after the YCgCo fix |
| Agent → `pinky mcp` → app → Mi 10 Pro | First connection asked "New agent" in the app; Allow answered it. Calculator AC, 6, ×, 7, = gave 42 with correct diffs; home; release |
| Agent → `pinky mcp` → app → iPhone | Connected, launched Calculator, read the screen |
| Claude Code (`claude -p`) on the Mi 10 Pro, through the app, companion in the driver | Asked to work out 6 × 7 in the Calculator only: connected, launched it, saw the old result and used `phone_batch` to clear and tap 6 × 7 =, read 42, pressed home and released, in 37 s and 7 turns |
| Agent → `pinky mcp` → app → iPhone, second round | Calculator 6, Multiply, 7, Equals by ref gave 42, 1.8–2.4 s a tap |
| Claude Code (`claude -p`) on the iPhone | Asked for 12 × 12 in the Calculator only: cleared it, entered 1 2 × 1 2 = in one `phone_batch`, read 144, pressed home and released, in 43 s |
| Claude Code (`claude -p`) with fake phones | Found last week's order, reordered, reached the payment, which Pinky held for approval; with no answer it was denied after 60 s and Claude stopped without paying. It also used `phone_request_approval` on its own to ask which order was lunch |

Timing on the Mi through the app, the same Calculator flow:

| | `uiautomator` (first merge) | Companion in the driver (second round) |
| --- | --- | --- |
| `phone_connect` | 2.7 s | 1.0 s |
| `phone_launch` | 10.8 s | 1.1 s (already in front) to 2.4 s |
| `phone_tap` | 8.3 s | 1.8–2.2 s |
| `phone_observe` | 2.2 s | 73 ms |

With `uiautomator`, each step settled (median 1.3 s), read the screen (2.2 s), took a history screenshot (0.75 s) and read the screen again to make sure no blocked app came to the front (2.2 s). With the companion the reads are about 70 ms; what is left is mostly the settle (the Calculator animates for about 0.9 s, then 0.4 s of quiet) and the screenshot. The companion labels elements the way TalkBack reads them, so the Calculator's keys are 清除, 乘 and 等于 on this phone rather than AC, × and =; the owner's own floating accessibility button shows up as `button "Mika"`. The accessibility setting was back to exactly the owner's service after every run.

## Open issues found tonight

- **Two lost taps (seen once).** In one run through the app on the Mi, taps on 7 and = did nothing: the gateway sent them to the right points (history has the targets) and the phone did not react. Three later runs through the app and two in-process runs did not reproduce it.
- **iPhone elements without positions** (first round): fixed in the second round by frames from `remoteAXService`.
- **The app under automation while the Mac is locked.** The floating approval panel captured blank, and pressing Allow on a payment card did not answer it, although the same press worked for the "New agent" and "Agent request" cards. The client session traced it to MyGo: a locked screen makes windows occluded, MyGo then holds their frames, and a press only counts at the next build. `ui.Tester` builds every frame, and `ui/realgateway_test.go` and the client's `TestGateway*` tests show these flows work against the real gateway. Watch them with the screen on. Fixed in MyGo by egoist/mygo#1485, which Pinky uses since 2026-11-08: automation builds the frame a window out of sight waits for when it acts on it, reads it or captures it.

## Decisions

1. Merge the session branches into `main` without asking: the owner asked for everything to be built overnight and checked in the morning.
2. Only USB iPhones by default, until Pinky has its own pairing flow for Wi-Fi.
3. The desktop app hosts the gateway (as DESIGN says), and the shim starts the app on macOS.
4. Keep the companion APK out of git and the Go binary for now: `make companion` builds it and `make app` bundles it.

## Round three

After the second round the owner asked for the window checked with the screen on, a decision on typing on an iPhone with Pinyin active, the companion app shipped inside Pinky.app, no keeping phones awake (burn-in, heat), and everything else that was still missing. The plan, the decisions and who did what are in [round3.md](round3.md).

### The window with the screen on

With the Mac unlocked, [`scripts/screencheck.py`](../../scripts/screencheck.py) drove the `e2e` build through MyGo's automation socket and acted as the agent over MCP, on `emulator-5560`: the New agent card (Approve), an approval in the window (Deny), the floating panel with its screenshot and countdown (Allow), clicking the mirror to take over ("You're in control"), Hand back and Stop all. All ten checks passed, and the captures looked right. It found one bug: with no phone added, and on Activity, Settings and Add phone, the window has no Needs you column, so an agent's first connection waited unseen until it timed out; the floating panel now shows approvals whenever the window cannot (a793b8e). Computer-use access to the window was declined, so the check uses the automation socket only, which acts inside Pinky's windows.

### Merges

Six branches merged into `main` without conflicts: `claude/platform-5568b2`, `claude/android-companion-76e6fd`, `claude/android-driver-1aff78`, `claude/ios-driver-ba7421`, `claude/gateway-core-da93b5`, `claude/desktop-client-380250`. Build, vet and tests passed after the last one except two client tests that still expected the gateway's stubs.

### Fixes made while integrating

- **The client's tests** expected the gateway's round-three stubs; the merged gateway records, exports and gives the demo phones unlocking, background work and files, so the client shows those controls (`TestGatewayRound3`, `TestGatewayLockedHandoff`).
- **The agent token** reaches the window (`ui.Options.AgentToken`), which Settings › Agents › Remote agents shows.
- **`PINKY_DEVICES` hid Wi-Fi pairing**: the filter now passes `driver.Pairer` through and lets a paired phone in (`TestDeviceFilterKeepsPairing`).
- **A wrong PIN** now carries `driver.ErrLocked` as well as `gateway.ErrWrongPIN`, so the client words it.
- **Key frames on shared streams**: the gateway shares one stream among the mirror and the recorder, so drivers answer its key frame requests: iPhones with an RTCP FIR, Android phones by restarting the encoder (scrcpy's reset-video). Without it a background task's recording on Android was empty: scrcpy sends key frames only every 10 s, and the recorder drops what comes before the first readable screen.
- **PIN stores say whether they work** (`secret.Supported`), so the client no longer probes the keychain with a delete.
- **`pinky quit`** ends the running app or daemon over the socket; Windows has no signal for it, and the smoke run uses it on every system.
- **Background work is the default on Android 13 and later** (`driver.BackgroundDefaulter`): there the virtual display keeps working while the phone's own screen is off and locked, which answers the owner's worry about keeping screens on. The person's choice in Settings wins and is kept.
- **An empty background display read as unreadable**: the gateway opens the display before the agent picks an app, and accessibility reports a display without windows as one it cannot see, so every task moved to the phone's locked main screen. The driver now shows it as an empty screen until an app starts, and `phone_connect` tells the agent to open apps with `phone_launch` (also in `docs/SKILL.md`).
- **The gateway's emulator tests** unlock through the gateway (`PINKY_TEST_PIN`), keep the recording test on the main screen, and check display 0's own power state: the phone as a whole stays awake while a background display is open.
- **Background displays went to sleep** about 10 s after the agent's last input while the phone was locked: Android gives every display group the lock screen's short timeout, so the app on the display slept and its tree read empty. Claude Code, which thinks longer than that between steps, then saw an empty screen and asked for a handoff. The Android driver now sends a key that does nothing (`KEYCODE_UNKNOWN`) to an open background display every 4 s without other input, which keeps that display's group awake and leaves the phone's own screen off (1437a55, the Android driver session).
- **The companion's idle shutdown never fired** while something kept dumping the screen: each `uiautomator dump` makes Android reconnect every accessibility service, and the app restarted its three-minute count at each reconnect. The companion now keeps counting across rebinds, and of several drivers using it, the last to leave turns it off (the companion and Android driver sessions).
- **Screenshots of background displays**: Android cannot screencap a virtual display, so approval cards, history and `phone_observe`'s screenshot had no picture during background tasks. The gateway now decodes one from the display's shared video, from a key frame it asks for, so it shows the screen as it is now (0.6–1.1 s on the emulator).

### Checked

| What | Result |
| --- | --- |
| `make check`, `go vet` with the `device` and `e2e` tags, Windows and Linux builds | Pass |
| CI (`.github/workflows/ci.yml`) on Linux, Windows and macOS, with the smoke run | See the run for the commit on GitHub |
| Gateway on the Android 16 emulator, main screen | Unlocked through the gateway with the PIN in 2.8 s; a 39 s recording (987 frames, 19 step marks) with nothing written while a blocked app was up |
| Gateway on the Android 16 emulator, background display, phone off and locked | Clock on the display by taps, 400 frames recorded, the phone's own display stayed off |
| Remote agents | `pinky daemon --fake --http`: 401 without the token; Claude Code over Streamable HTTP opened the Calculator on the fake Pixel and released it (24 s, $0.21) |
| `pinky quit` | Ends the daemon and the app |
| Pinky.app | FFmpeg's four libraries in `Contents/Frameworks`, the companion APK and FFmpeg's license in `Resources`, ad hoc signature valid, `.dmg` 13 MB |
| Claude Code on the emulator through the app, the emulator off and locked | The first runs found the two bugs above. After the fixes Claude worked on the background display (opened the Clock, started and paused the stopwatch, read the time, released) in 94 s for $0.46; the phone's own screen stayed off and locked |

The phones were not available for end-to-end runs: the iPhone's screen was off and locked, the Mi 10 Pro locked after someone pressed its power key at 10:35, and the Pixel is still offline. The sessions' own real-phone checks are in their notes: the iPhone's 16 tests (typing by paste with Pinyin active, Chinese and emoji, the clipboard put back), and the Calculator on a background display of the Mi (unlocked, main screen untouched).

### The Pixel, back at noon

The Pixel 1 (Android 10) came back on USB. The Android driver's device tests pass on it (observe, screenshots, the Calculator, typing and the clipboard, the stream and settle, Pinky's own input not counted as the person's). Two fixes came from it:

- **No background work below Android 11**: an app runs on a virtual display there, but the companion cannot read another display's windows before API 30, so the driver says so (`driver.BackgroundSupporter`) and the client offers no switch.
- **English app names on a phone in another language**: `phone_launch "Calculator"` found nothing on the Pixel, whose apps have Chinese names (计算器). The gateway and the driver now also match the parts of the package or bundle id.

A Claude Code run on the Pixel stopped when the person watcher reported a finger on its screen: the owner had touched it, so the takeover worked as designed.

### With the owner back

- **The Mi 10 Pro's companion** was updated (build 130, replacing round one's build signed with another key; its service on next to the owner's Mika). Claude Code then worked out 8 × 9 in the Calculator on a background display in 38 s ($0.21, about 1.4 s a tap), and the Calculator task the owner had open went back to the main screen afterwards.
- **The iPhone over USB**: Claude Code typed "Wi-Fi" into Settings' search by paste with Pinyin active, read the first result, went home and released the phone, in 45 s ($0.25).
- **Signing and notarization**: `make release` signed Pinky.app with the NATURAL SELECTION LABS Developer ID (team 492J8Q67PF, hardened runtime, timestamp) and Apple notarized the `.dmg` (accepted and stapled); Gatekeeper accepts both as "Notarized Developer ID". The signed app loads FFmpeg from its own Frameworks.

### Unlocking the iPhone from the computer

- **Nobody knew the feature was there.** The owner locked the iPhone and Pinky said nothing: idle phones' lock state was not read, and the offer to save a PIN came only once an agent hit the lock. The gateway now reads idle phones' lock state every 6 s, and a locked phone's page shows a card that offers to save its PIN, or to unlock it once one is saved (fc835f4).
- **Unlock failed with "the phone did not accept the saved PIN"**, though the PIN was right and nothing had been typed: the passcode pad was looked for through an accessibility session made before the phone locked, which did not see it, and the failure was kept for 30 s, so the next tries failed in a second. A diagnostic that types nothing showed the pad about 2 s after the swipe in a session made on the lock screen. The driver now looks in a fresh session, retries after errors and waits up to 5 s; `driver.ErrPINNotEntered` tells "nothing was entered" from a refused PIN; and after a refused PIN the client offers no further tries until the phone is unlocked on it or the PIN changes (0db9973).
- **Then it worked**: Unlock with Touch ID from the card unlocked the iPhone; and with the iPhone locked, Claude Code asked for help, the owner clicked Unlock with Touch ID (4 s), and Claude finished the Calculator task (76 s, $0.27).

### Pairing the Mi over Wi-Fi

- **adb no longer saw the network.** Its server had run since two days before and its mDNS discovery listed nothing, while macOS's Bonjour saw the Mi's wireless debugging service; restarting the server brought discovery back, and it reconnected the Mi over Wi-Fi at once (this Mac had paired before). Pinky does not restart adb by itself, since other tools share it; the pairing step now says what to check after 30 s (171b99b). Since 2026-11-07 Pinky searches the network itself and pairs, connects and reconnects by address, so a server whose discovery stopped no longer keeps a phone away; it still never restarts the server ([android.md](android.md#wi-fi-without-adbs-discovery)).
- **One phone showed twice**, over USB and over Wi-Fi, with another id on Wi-Fi. Over Wi-Fi a phone's id is now its hardware serial (`ro.serialno`, the serial adb gives the USB connection), its connections are listed once, and USB is used while it works (2e73598).
- **Then it worked**: the owner paired the Mi by QR code from Add phone, and the new release lists the Mi once.

### Unlocking the Mi from the computer

- **"The lock screen showed no PIN pad"**: on the Mi 10 Pro (LineageOS, an under-display fingerprint sensor) `wm dismiss-keyguard` brings up the alternate bouncer, which waits for a finger on the sensor, and the driver's fallback swipe started on the sensor, which took it. Nothing was entered. The driver now recognizes the alternate bouncer (`alternate_bouncer`, `udfps_touch_overlay`) and taps above the sensor, which brings up the PIN pad, and swipes from above the sensor otherwise (6eed8c9). A check on the locked Mi that enters nothing found the PIN pad that way; then Unlock with Touch ID unlocked the Mi over Wi-Fi.

### Unlocking other Android phones

The owner asked whether unlocking from the computer works on every Android phone or needs work per ROM. Until now the driver found the PIN pad by AOSP's resource ids (`pinEntry`, `key0`..`key9`) or a focused password field, and typed the PIN as key events. OEM skins draw their own lock screens, so the driver now also reads a PIN pad from what it shows: ten keys labelled with one digit each (in their text or description, with or without the keypad letters), laid out as a phone keypad. It taps those keys, then the pad's OK key if it shows one (OK, Enter, 确定, ✓ and the like), or leaves the check to the pad, which is what MIUI, EMUI and ColorOS do with fixed-length PINs. AOSP's pad and focused password fields still get key events, as before.

Safety comes first, since a wrong entry costs one of the phone's attempts and repeated ones lock it out or can erase it. Pinky taps only when it reads exactly one such keypad, all of it on one window and not under another window, in the same place in two reads in a row (a pad still sliding in reads a few dozen pixels off). It never taps a guessed position and never retries a PIN the phone refused. A screen with digit keys it cannot read with confidence gets nothing, not even the fallback swipe (`driver.ErrPINNotEntered`). Details are in the [Android notes](android.md).

What is checked where:

| Lock screen | How the PIN goes in | Checked |
| --- | --- | --- |
| LineageOS 24 (Android 17) on the Mi 10 Pro, with the fingerprint screen in front | Key events, after a tap past the fingerprint screen | **On the phone**: the owner unlocked it from the computer, before this change, which leaves that path as it was |
| AOSP, Android 10, 16 and 17 (our emulators; Android 17 on a temporary AVD) | Key events | **On the emulators**, through the gateway (`TestEmulatorUnlock`) |
| The same pads read as an OEM pad, ignoring AOSP's ids | Taps on the keys, then Enter | **On the emulators**, reading with the companion and with uiautomator, right and wrong PIN (`TestDeviceUnlockByTaps`); real dumps of these pads are test fixtures |
| Pixel (Android 10) | Key events | Not tried on the phone; the Android 10 emulator stands in for it |
| Xiaomi MIUI / HyperOS | Taps; the pad checks the PIN itself | Synthetic dump only |
| Samsung One UI | Taps, then OK; key events if its PIN field turns out to be a focused password field | Synthetic dump only (its ids are unknown) |
| OPPO ColorOS, OnePlus OxygenOS, realme UI | Taps (the 0 key, which has no description, by its id); the pad checks the PIN itself | Synthetic dump only |
| vivo FuntouchOS / OriginOS | Taps (keys described "One".."Zero") | Synthetic dump only |
| vivo OriginOS opening on its fingerprint page (vivo V2172A, Android 15) | A tap on its 使用密码 / "Use password" link, then the PIN as on its pad | The problem is from a user's report (DIYgod/pinky#41): Pinky found no PIN pad until they tapped 使用密码 by hand, after which its unlock went through. The fix is checked on a synthetic dump made from their picture of the page only |
| Huawei EMUI / HarmonyOS | Key events into `fixedPinEntry`, if it is a focused password field | Synthetic dump only |
| Honor MagicOS | Taps; the pad checks the PIN itself | Synthetic dump only |
| SystemUI's Compose bouncer (Android 15 and later, where turned on) | Taps, then Enter | Synthetic dump only: the emulators' Android 16 and 17 images still use the View bouncer |
| LineageOS with "Scramble PIN layout" | Key events; read from its labels, taps follow the scrambled digits | Synthetic dump only |

The synthetic dumps (`internal/driver/android/testdata/keyguard/synthetic-*.xml`) were made up from what is published about these lock screens (AOSP's sources, Auto.js unlock scripts with per-ROM selectors, a vivo SystemUI layout, Appium reports); each file says what it is based on and what is invented. They show that the reading logic handles those layouts, not that those phones accept the taps. Still unknown on real OEM phones: whether `wm dismiss-keyguard` or the swipe brings up their PIN pad (Auto.js scripts say MIUI may need another gesture), whether `KEYCODE_WAKEUP` wakes them, whether their lock screens show their keys to accessibility at all, and Samsung's and Honor's labels. Where any of that fails, Pinky enters nothing and says so, and the person unlocks on the phone. Pattern locks are not supported.

**vivo's fingerprint page.** On a vivo V2172A (OriginOS, Android 15, over Wi-Fi) the lock screen opened on a page of its own asking for a fingerprint, with the title 请验证指纹, the sensor's ring, and 紧急拨号, 使用密码 and 取消 at the bottom. Neither `wm dismiss-keyguard` nor the swipe brought up the PIN pad, and it carries none of AOSP's alternate bouncer ids, so Pinky said it found no PIN pad. Pinky now taps the "use password" link on such a page (`internal/driver/android/passwordlink.go`) and then waits for the pad as before; the PIN still goes in only once the pad is read with confidence. Known from the user's report and picture: the layout and the Chinese words, and that tapping 使用密码 brings up a pad that Pinky's unlock then gets through. Inferred and not checked on a phone: that the page shows its words to accessibility (in SystemUI's or a keyguard package), that the link is a node of its own with 使用密码 in its text or description, the English wording ("Use password"; "Use PIN" and others are accepted too), and that one tap at the link's center works. Where any of this differs, Pinky finds no link, falls back to the swipe as before, and enters nothing.

### Mirroring over Wi-Fi

- **Why it lagged**: the Mi's Wi-Fi link runs at 1200 Mbit/s (11ax, RSSI −51), but the phone sends far less: adb carried 4–14 Mbit/s within the same hour (less while the phone dozes), and plain TCP without adb 10.7 Mbit/s, so the phone's Wi-Fi limits it, not adb's TLS. scrcpy's default 8 Mbit/s at 60 frames a second then queued on the phone and in the network for seconds. The computer adds nothing: the mirror's player keeps only the newest picture and skips to a key frame past 30 queued packets, so only a lower bit rate shortens the queue.
- **The fix**: over Wi-Fi the stream is 2.5 Mbit/s, 30 frames a second, in H.265 (`Device.baseOptions`; `android.Config.VideoBitRate`, `MaxFPS` and the new `VideoCodec` override it). USB keeps scrcpy's defaults and H.264. A phone whose H.265 encoder fails uses H.264, now also on background displays, and the failure no longer turns video off altogether (cd38d00).
- **Measured** on the Mi (`TestDeviceWifiStream`: Settings scrolled once a second on a background display, each frame's arrival against its capture time on the phone, two runs of two rounds):

  | Stream | Frames | Queued on the way (p50 / p90 / max) | Frames bunched within 8 ms while moving |
  | --- | --- | --- | --- |
  | Before: 8 Mbit/s, 60 fps, H.264 | 30–38 fps | 49–785 ms / 0.22–2.8 s / 0.5–3.9 s | 43–48% |
  | After: 2.5 Mbit/s, 30 fps, H.265 | 23–25 fps | 12–25 ms / 92–194 ms / 0.4–0.8 s | 9–20% |
  | 4 Mbit/s, 30 fps, H.265 | 22–23 fps | 13–33 ms / 66–330 ms / 0.3–1.2 s | 19–22% |

  2.5 Mbit/s at 60 fps sent more frames (33–35 fps) for the same queueing, at half the bits a frame. The bit rate is not chosen from measured throughput: the same link swung between 4 and 14 Mbit/s within minutes, and scrcpy cannot change a running encoder's bit rate (a new session restarts the video, and a background display cannot be restarted at all). The worst spikes (0.4–0.8 s) are the Wi-Fi's own.
- **H.265 on the computer** works end to end with the app's FFmpeg (`TestDeviceHEVCOnComputer` on the Mi): every picture decoded by VideoToolbox, a key frame 170–203 ms after asking for one, the recording written as hvc1 (ffprobe) and played back whole. CI decodes H.265 on Windows and Linux (`TestDecodeTestStream`). An emulator's software H.265 encoder sends 230x512 for a 1080x2400 display, so a background display now maps coordinates through its own size whatever the video's.

### Background displays on Android 17

- **The display stopped with the phone's screen.** scrcpy asks for a display group of its own, and up to Android 16 got one; Android 17's separate screen timeouts put every virtual display that is not a virtual device's into the main display's group (`LogicalDisplayMapper.assignDisplayGroupLocked`, `DisplayGroupAllocator`). The display then follows the main screen's power: once the screen timed out (or the owner pressed power), no frames came, the app stopped and the companion could not read it. A display-scoped wake lock does not help. The Android 17 emulator (`pinky-api37`, created for this) does the same, so it stands in for the Mi in the tests that press the power key.
- **What Pinky does now** (`internal/driver/android/dark.go`): while such a display is open and the phone sleeps locked, Pinky wakes it (it stays locked) and turns its panel off at once with scrcpy's display power control, as `scrcpy --turn-screen-off` does, and keeps it awake with the keep-awake key; nothing lights up and the display runs. A touch on the phone, an unlock, or a call or alarm over the lock screen turns the panel back on for the person. The power key puts the awake phone to sleep, and after that Pinky does not wake it again: the agent gets `device_locked` and asks for a handoff, and the display runs again once the person turns the screen on. While the person uses the phone, Pinky sends no keep-awake key, and when their screen then times out or they turn it off, Pinky wakes it dark again. When the task ends the keep-awake key stops and the phone times out by itself (9–10 s). `BackgroundByDefault` stays true from Android 13 (1aff83f).
- **Two traps.** A scrcpy server that turned the panel off turns it back on when it exits while the phone is awake, so the panel is switched by two control-only sessions of its own that exit only once the phone sleeps. And SurfaceFlinger keeps the power mode a virtual display had before its first surface and restarts it in that mode at every video reset, so a display created while the phone slept stopped drawing at its first reset (a mirror joining, a recording, a screenshot) even once the phone was awake: Pinky wakes the phone before it creates the display.
- **What the person sees**: an always-on display goes dark while an agent works. The first try lit the lock screen for 450 ms (scrcpy's first panel-off call loads classes); warming it up while the phone sleeps brought that to about 100 ms, the time SurfaceFlinger takes to power the panel on, behind which the panel-off waited. Now the second session's panel-off train leaves 50 ms after the wake key's, and on the Mi the panel-off began 1–13 ms after the power-on finished; the display power controller sets the brightness only once its own power-on call returns, which was after the panel-off in the logs read. To check by eye: whether a dark Mi blinks when a background task starts.
- **Checked**: `TestDeviceBackgroundScreenOff` on the Mi over Wi-Fi and on the Android 17 and 16 emulators (frames, reads after 25 s idle, the phone asleep again after the task); `TestDeviceBackgroundPerson` on the Android 17 emulator (touch, power key, timeouts); the gateway's `TestEmulatorBackground` (launch with the screen off, reads after 8 and 14 s idle, screenshots from the video, a recording, the phone's own screen off) and `TestDeviceBackgroundLocked` / `TestDeviceBackground` on both emulators.
- **Limits**: while dark the phone is awake, so its touchscreen works (touches reach the lock screen, in a pocket too), the first press of its power key turns it off rather than on, and notifications do not light it. While the person uses the phone, the agent's input on the display counts as their activity and keeps their screen on. If the computer's connection drops while the phone is dark, the control sessions' servers exit and the lock screen shows until it times out (10 s). Quitting Pinky during a dark background task waits up to that long for the phone to time out. A phone that has no lock screen, or locks long after its screen goes off, is not woken dark: its background display pauses while the screen is off, and a task that starts then runs on the phone's own screen.

### Known limits found in round three

- **A screenshot is one instant.** Content that blinks (Clock's paused stopwatch) may show blank; the element list still has its text.
- **Apps that hide text from accessibility** (Clock's running stopwatch hides its time) need a screenshot to read it; the agent guide says to ask for one when the list is not enough.

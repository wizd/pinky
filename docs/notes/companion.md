# Android companion

The companion, Pinky for Android (called Pinky Companion before November 2026; it shows on the phone as Pinky), is a small Kotlin app with an accessibility service, plus its Go client. It gives the Android driver a fast element tree, events (window changes, toasts, notifications, content changes), text input with `ACTION_SET_TEXT` and a settle signal. The wire protocol is in [companion-protocol.md](companion-protocol.md).

Status, 2026-11-07, round 3: background displays work (tree, `set_text`, window and toast events and `wait_idle` for a given display, Android 11 and later), every build is signed with a committed key, and the versions come from the build, so the driver can tell an outdated app. Checked on the Android 16 emulator (`pinky-api36`, `emulator-5560`) and the Android 10 emulator (`pinky-api29`, `emulator-5562`), plus a temporary AVD with `pinky-api29`'s system image while `pinky-api29` was locked with a PIN this session did not know yet. Round 2 results on the Pixel and the Mi 10 Pro are below, unchanged: nothing was installed on the Mi 10 Pro this round, and its 01:43 build is signed with another key (see [Versions and signing](#versions-and-signing)). The Go client in `internal/driver/android/companion` belongs to the Android driver session from round 3 on; this session owns the app and these notes.

2026-11-08: the service is on only while an agent works on the phone, and releases are signed with a private key. Banking apps refuse to sign in while an app from outside an app store has an accessibility service on (Malaysia's banks since 2024, Singapore's since 2023), and until then Install kept the service on for good. And every build, releases included, was signed with a key committed here, public since the repository is (2026-11-07), so anyone could sign an APK that installs as an update of a released Companion and keeps its service. See [Only while an agent works](#only-while-an-agent-works) and [Versions and signing](#versions-and-signing).

## What is where

| Path | What |
| --- | --- |
| `android/companion/` | Gradle project (wrapper included). Package `app.pinky.companion`, minSdk 29, targetSdk 36, compileSdk 36.1 (the newest platform in the local SDK). AGP 9.2.1 with its built-in Kotlin, Gradle 9.4.1, no AndroidX. The debug APK is about 2.7 MB. |
| `.../PinkyAccessibilityService.kt` | The service: starts the socket server when Android connects it, feeds events to the hub, turns itself off after a minute without a connection (three minutes in builds until 2026-11-08) |
| `.../CompanionServer.kt` | Abstract socket `@pinky_companion` (`@pinky_companion_dev` in the development build), peer check, one thread per connection |
| `.../RequestHandler.kt` | Parses requests; runs everything that touches the tree on one worker thread |
| `.../TreeDumper.kt` | Windows and nodes to JSON, with per-window timing |
| `.../Displays.kt` | Which windows are on which display: checks display ids for requests, lists a display's windows, and maps windows to displays for events |
| `.../TextSetter.kt` | Target resolution, `ACTION_SET_TEXT`, and the input connection and paste when an app refuses it |
| `.../EventHub.kt` | Event conversion, content-change folding, subscriber queues, `wait_idle` |
| `.../AppLabels.kt` | App display names, loaded in the background |
| `.../MainActivity.kt` | Debug page (own process `:debug`): status, a text field, a password field, toast and notification buttons |
| `android/companion/app/src/debug/res/values/strings.xml` | The development build's names, "Pinky (dev)", so the two builds can be told apart in Android's settings |
| `assets/pinky-companion.json` in the APK | The version, written by the build, so Pinky can read a bundled APK's version without the Android SDK |
| `res/drawable/ic_launcher_*.xml` | Adaptive icon converted by hand from the brand SVGs (`pinky-android-foreground.svg`, `pinky-android-background.svg`) into vector drawables; the SVG transforms are kept as groups, and the body's radial gradient is drawn in its unit box so it stretches the way SVG's `objectBoundingBox` does |
| `internal/driver/android/companion/` | Go client: `Client` (Ping, Tree, Dump, SetText, Events, WaitSettled, Enable, Disable, ForceDisable, Install, InstallIfChanged, Installed), tree-to-screen conversion, `Runner` (adb) |
| `internal/driver/android/companion/cmd/companionctl` | A command-line tool for trying all of it by hand and for the benchmark |

No shared contract changed. The client implements `driver.Settler`. The app's debug page shows its version, version code and the display it is on ("Version 0.1.0 (68) on display 10").

## How to build and run

```sh
make companion                   # android/companion/app/build/outputs/apk/debug/app-debug.apk
```

`make companion` runs `./gradlew assembleDebug` in `android/companion` with `ANDROID_HOME` (`~/Library/Android/sdk`) and `JAVA_HOME` (Android Studio's JBR) set, and `make app` copies the APK into Pinky.app. The build needs git and the repository's whole history for the version code; `-Ppinky.versionCode=N` overrides it.

By hand, through the device lock (every command from `ping` on turns the service on for its run and restores the setting afterwards; `-keep` leaves it on):

```sh
go build -o /tmp/pinky-companionctl ./internal/driver/android/companion/cmd/companionctl
ID=FA6C20303860
scripts/devlock.sh $ID /tmp/pinky-companionctl -s $ID install android/companion/app/build/outputs/apk/debug/app-debug.apk
scripts/devlock.sh $ID /tmp/pinky-companionctl -s $ID tree            # the screen as agents see it
scripts/devlock.sh $ID /tmp/pinky-companionctl -s $ID tree -raw       # the raw JSON tree
scripts/devlock.sh $ID /tmp/pinky-companionctl -s $ID settext -id app.pinky.companion:id/debug_input "你好"
scripts/devlock.sh $ID /tmp/pinky-companionctl -s $ID events toast window
scripts/devlock.sh $ID /tmp/pinky-companionctl -s $ID bench 10        # companion tree vs uiautomator dump
scripts/devlock.sh $ID /tmp/pinky-companionctl -s $ID disable         # cleanup after a crash: removes Pinky's entry
```

`install` skips the install when the phone already has the same APK (compared by SHA-256); `install -force` always installs.

Tests:

```sh
go test ./internal/driver/android/companion/                    # synthetic data only
scripts/devlock.sh $ID env PINKY_DEVICE=$ID go test -tags device -v ./internal/driver/android/companion/
```

The device test installs the APK if it changed, turns the service on, reads Settings and the Calculator, writes Chinese and emoji into the debug page (by resource id, by point with append, by node handle), checks the password field and a `not_found` error, waits for a toast event, then turns the service off and checks that `enabled_accessibility_services` is exactly what it was. `PINKY_COMPANION_APK` overrides the APK path.

## Versions and signing

- **versionName** is Pinky's version, read from `cmd/pinky/mygo.json` at build time (`0.1.0` now). **versionCode** is the number of commits up to the latest one that changed `android/companion` (since 2026-11-10; until then the count of all commits, `git rev-list --count HEAD`), so it moves only when the app does: a Pinky release that leaves the companion alone bundles the same versionCode, and a phone that refuses installs over USB has nothing new to install by hand. "Changed" is `git rev-list --full-history HEAD -- android/companion` (merges included), and the latest is the one with the most commits behind it among `git merge-base --independent` of those; so a later build whose companion differs always counts higher, also when a branch changed it before merging main, and the commit that switched the scheme is itself a change, so it counts above every build before it. A shallow clone stops the build with a message (fetch the history; `fetch-depth: 0` for `actions/checkout`) instead of building versionCode 1; `-Ppinky.versionCode=N` overrides the count.
- **Reading the installed version:** `adb shell dumpsys package app.pinky.companion` prints `versionCode=68 minSdk=29 targetSdk=36` and `versionName=0.1.0`; `adb shell pm list packages --show-versioncode app.pinky.companion` prints `package:app.pinky.companion versionCode:68`; while the service runs, `ping` returns `version` and `version_code`.
- **Reading a bundled APK's version:** the APK is a zip file, and `assets/pinky-companion.json` in it holds `{"package":"app.pinky.companion","version_name":"0.1.0","version_code":68}`, written by the same build from the same values as the manifest (`aapt2 dump badging` shows them too, but needs the SDK).
- **Comparing:** an installed versionCode lower than the bundled one means outdated. The same versionCode with a different file is a build with uncommitted changes; compare SHA-256 when that matters (`InstallIfChanged` does).
- **Two builds** (2026-11-08). The **release build** (`./gradlew assembleRelease`, which `scripts/dist.sh companion` runs in the release workflow) is `app.pinky.companion`, minified with R8 (about 100 KB), not debuggable, and signed with Pinky's private release key: the workflow decodes it from its secrets `COMPANION_KEYSTORE` and `COMPANION_KEYSTORE_PASSWORD` into a temporary file for the build (`PINKY_COMPANION_KEYSTORE`, `PINKY_COMPANION_KEYSTORE_PASSWORD`; alias `pinky-companion`), and a release build without them stops. The **development build** (`./gradlew assembleDebug`, `make companion`, which `make app` carries) is `app.pinky.companion.dev`, named "Pinky Companion (dev)", listening on `@pinky_companion_dev`, and signed with the machine's own debug key (`~/.android/debug.keystore`, which Android's tools create): it never installs as an update of a released Companion and can be installed beside one. The Go client takes the build (`companion.Release`, `companion.Dev`); the Android driver uses the one its bundled APK names (`companionApp`), the release build when it has none. `companionctl -dev` talks to the development build.
- **Signers.** `dumpsys package <pkg>` names an installed build's signer: `signatures=PackageSignatures{97e501d version:2, signatures:[dd80ed25], past signatures:[]}`, where `dd80ed25` is Java's hash code (`Arrays.hashCode`) of the DER certificate. `companion.APKCertificate` reads a bundled APK's certificate from its APK Signing Block (v3, else v2), and `SignerName` gives the same hash, so the driver tells a build signed with another key before installing. `Version.Signer` holds it.
- **The public key, until 2026-11-08.** Every build from round 3 (2026-11-07) until the release key (2026-11-08; release 2026.1108.8 was the last release then) was signed with `android/companion/pinky-companion-debug.keystore` (PKCS12, alias `pinky-companion`, store and key password `android`; certificate `CN=Pinky Companion Debug, O=Pinky`, SHA-256 `7e:ac:a4:e5:a5:de:59:98:9c:c9:66:1b:f1:06:bc:93:1a:91:2b:63:82:42:33:07:85:0d:85:42:94:38:5d:3e`, signer `dd80ed25`, `companion.OldPublicSigner`), committed so that a build from any machine installed over an older one. The repository went public on 2026-11-07, so the key is public: it is gone from the tree and must never sign anything again. Pinky replaces builds signed with it by itself: their signer differs from the bundled APK's, so `CompanionState` says `public_key` (other keys give `outdated`), and the automatic update (or the person's Update) turns the service off, uninstalls Pinky's own package, installs the release build, and leaves the service off until the next task; the person's other accessibility services stay. The client says so in a toast and What's new. Where the phone asks before installs over USB (Play Protect), the automatic update waits for the person, and the phone's Update card says why, in place of its usual text: the installed version is signed with an old key that is public, so anyone could make a fake update of it, and the new one is signed with Pinky's private key; Settings › Phones says "Pinky for Android has a security update" and the checkup says the same. After the person's Update, the notice is the replacement's. The certificate is `internal/driver/android/companion/testdata/old-public-signer.der`, for tests.
- **Builds from before round 3** (versionCode 1) were signed with each machine's own `~/.android/debug.keystore`. Installing a current build over one fails with `INSTALL_FAILED_UPDATE_INCOMPATIBLE` (signatures do not match). The installer has to uninstall the old app first (`pm uninstall app.pinky.companion`, only Pinky's own package), and only while its service is off: Android drops an uninstalled package's services from `enabled_accessibility_services` by rewriting the setting. Pinky does that for any build signed with another key.

## Only while an agent works

Since 2026-11-08 the service is on only while an agent works on the phone: from a task's start to its end, except while the person is in control. The gateway tells the Android driver (`driver.WorkWatcher`), and the driver turns the service on in the background as the agent starts, so the first read of the task finds it ready, and off 5 s after the agent's last call once the task ended or the person took over, or after a minute without calls while it works. While no agent works, reads do without it (uiautomator) and nothing turns it on, except unlocking the phone from the computer, which reads the PIN pad several times. A service left on (an older Pinky's Install, or another client's run) goes off when Pinky opens the phone, unless another client is connected. Install and Turn on check that the service comes on, then it goes off again. The app's own watchdog, now a minute without any client, covers a Pinky that crashed.

What turning it on costs, from writing the setting until the service answers, then its first tree (`companionctl coldstart`, five rounds each; `-cold` stops the app's process first, as Android does to an app unused for long):

| Phone | On | First tree | A tree once on |
| --- | --- | --- | --- |
| Android 16 emulator (`emulator-5560`), process alive | median 238 ms | 8 ms | 3 ms |
| Android 16 emulator, process stopped | median 241 ms | 10 ms | 7 ms |
| Mi 10 Pro (Android 17, adb over Wi-Fi), process alive | median 333 ms | 102 ms | 60 ms |
| Mi 10 Pro, process stopped | median 691 ms | 84 ms | 60 ms |

Through the driver, with the release build signed with the release key (CI run 42457681518): on the emulator the service came on in 177 ms after `AgentWorking(true)`, and the task's first Observe, called right after it, returned 32 elements 212 ms after the task started; on the Mi 10 Pro it came on in 323 ms and the first Observe returned 404 ms after the start (a later one took 677 ms there, most of it the window manager's state over Wi-Fi, which every read asks for). It went off 0.2 s (emulator) and 0.4 s (Mi) after the task. So a task's first read costs 0.2 to 0.8 s more than with the service kept on, once per task (and after each pause of a minute), and phone_connect starts the video meanwhile: no setting to keep it on was added. The Pixel (Android 10, the slowest) was not connected.

**Replacing on a phone with Google Play Protect** (Mi 10 Pro, Google Play services 26.43.30, `package_verifier_user_consent` 1, the verifier settings unset, so `installAsks` says the phone may ask and automatic updates wait for the person's Update): the person's path (`SetUpCompanion`) replaced release 2026.1108.3, signed with the public key, with the release build in 2.6 s, with no dialog, keeping the owner's other accessibility service (Mika) in the setting. Putting the public-key 2026.1108.8 build back afterwards (`pm install` after an uninstall) showed Play Protect's "Send app for a security check?" and waited 90 s until the owner answered (Don't send); consent stayed 1. So an install over USB can ask even with consent given, and `installAsks` stays as it is. Checked by `TestDevicePhoneCompanionReplace` (opt-in for phones, `PINKY_COMPANION_PHONE_OK=1`), which puts the phone back as it was; Pinky 2026.1108.8 ran meanwhile and turned the service off when the test ended, so the test's last step had to be repeated with `companionctl keepon`.

## Background displays

Android 11 and later give accessibility services the windows of every display Android opens to them, so the companion reads and writes a background display the same way as the main one: `tree`, `set_text`, `events` and `wait_idle` take a `display`, and every event names its display (-1 when that is not known). `ping` lists the feature `displays` where this works; on Android 10 every request for another display fails with `unsupported`. The protocol has the rules, including how input focus works across displays: [companion-protocol.md](companion-protocol.md#displays).

Checked on both emulators with a virtual display from the driver (`androidctl bg -app app.pinky.companion`, which shows the debug page there) and a raw protocol script (the Go client does not send `display` for `set_text` and events yet):

| Check | Android 16 (`emulator-5560`) | Android 10 (`emulator-5562`; the last row on the temporary AVD) |
| --- | --- | --- |
| `ping` | `displays: [0, 10]`, `features: ["displays"]` | `displays: [0]`, `features: []` |
| Tree of the virtual display | The debug page, 15 nodes ("Version 0.1.0 (68) on display 10"); 1 ms on the phone and 4 ms round trip, the same as the main display; the main display's tree still shows the launcher | `unsupported`: "display 2: Android 10 (API 29) gives accessibility services the main display only; other displays need Android 11" |
| `set_text` on it | By resource id ("你好，后台 Pinky 🎉", verified, the page's echo shows 13 characters), into the focused field after a tap there (`input -d 10 tap`), at a point with append, by node handle | `unsupported`, as above |
| Focus stays per display | With the virtual display touched last, a `set_text` for display 0 without a target gives `not_found` and leaves the background field alone; an id target for display 0 does not reach the other display | |
| Events | A subscriber for display 10 gets only display 10's events (Settings opening there, its window list changes, the debug page's toast), one for display 0 none of them, one without a filter all of them with `display: 10`; notifications pass every filter with `display: -1`; the soft keyboard opened on the virtual display (`display: 10`), as scrcpy's local IME policy asks | `events` with `display: 2` gives `unsupported` |
| `wait_idle` for the display | Settled after 300 ms | `unsupported` |
| Errors | Display 9999: `not_found`; display -3: `bad_request` | |
| Events of the virtual display without a filter | `display: 10` | The debug page's window events after it moved there: `display: -1`, never 0; the launcher coming to the front of the main display meanwhile: `display: 0` |

Found on the way:

- **Android 16 sends toast events with no display and no window** (both -1), so a toast takes the display of its app's last window event. The first version used the app's last window id instead and lost toasts whose last window was the launch splash screen, which closes before the toast shows. A toast from an app that was already open when the service started, before any window event of its own, had no display at all; that is the usual case when the driver turns the companion on for an app the agent is using. So when the service starts, the app now records which display each open app's windows are on (leaving out an app with windows on two displays), and a toast falls back to that.
- **The first toast after the service started could keep the app's name** ("Main toast Pinky Companion"): app names load in the background, and a toast that came before any window event of its app found none loaded yet. A toast now loads its app's name at once.
- **Starting an app on the virtual display moves its task off the main display.** The debug page's task (#13) was on display 0, in the background, before `androidctl bg -app app.pinky.companion`, and on the virtual display afterwards, without the activity being recreated. An app the person has open would leave their screen the same way. For the Android driver.
- **On Android 10 the shell cannot start activities on scrcpy's virtual display** (`am start --display 2` fails with "Permission Denial: starting Intent ... with launchDisplayId=2"): Android 10 lets only activities that allow embedding open on another app's virtual display. scrcpy's own start did put the debug page there (its task moved from display 0, as on Android 16). A plain `am start` then opened a second task on the main display instead of reaching the page on the virtual display.
- **A tap on the virtual display ended the scrcpy session on Android 10** (`androidctl bg … tap:274,1753`: "device offline: scrcpy session ended: scrcpy: control channel: EOF", 2.5 s after the display opened), once, on the temporary AVD. For the Android driver; the companion was not involved.

## Input method windows that cover the app

Found after the Android driver session reported a nearly empty Settings search tree on the Android 16 emulator:

- **Settings search came back nearly empty on the Android 16 emulator** (reported by the Android driver session). Gboard had opened its "Try out your stylus" introduction: a sheet over a scrim that covered everything below the status bar (its window and touchable region `[0,128,1080,2400]`). It came up after the Android driver session injected touches through the emulator console, whose virtio touch device reports a stylus button (`BTN_STYLUS`); that session dismissed it afterwards. Android marks the nodes of a window that a higher window's touchable region covers as not visible to the user, so the app's search field and results were left out, and the input method window, not walked by default, held the only visible content. `uiautomator dump` still listed the app's nodes, most likely because it runs while no service tracks windows (it unbinds the others), so Android has nothing to tell it what covers them; taps there would have reached Gboard's sheet, whose touchable region they were in. The tree now walks an input method window that covers the screen like a sheet (see the `ime` parameter). The sheet did not come back on its own in later runs (Gboard switched to its stylus toolbar), but the toolbar's menu opens it again: tap a text field through the emulator console, then More stylus options › Try handwriting demo. Checked that way with release 2026.1107.1 (2026-11-07): the tree walks the sheet and the menu, sends none of the app's nodes under them, and the client lists exactly what the person can tap; the trees are fixtures in `internal/driver/android/companion/testdata`. The Android driver also reads the tree again with `ime` when it comes back empty while a keyboard shows, which covers older builds of the app.
- **Gboard's stylus toolbar has a touchable region much smaller than its bounds:** a strip on the left and one at the bottom (`[[21,896,169,1586],[0,2337,1080,2400]]`) inside bounds of `[0,896,1080,2400]`. The app's nodes in between are visible and receive taps, but a client that cuts elements by the input method's bounds drops them (the search results below y 896). Windows now carry `region` on Android 13+ when it differs from the bounds.

## Fields that refuse `set_text` (GitHub issue #31, 2026-11-09)

A Redmi K70 (HyperOS, Android 16) with Gboard's Chinese keyboard could not type into WeChat's chat input on a background display: `phone_type` reported nothing wrong and nothing appeared. The chat input refuses `ACTION_SET_TEXT`, so the driver fell back to keys and a scrcpy paste, and Gboard's Pinyin composes keys (ASCII showed up only in its suggestion strip) and ignores the paste key and Ctrl+A. Nothing checked the field afterwards.

`set_text` now goes on to the field's input connection and then to a paste, as [companion-protocol.md](companion-protocol.md#set_text) describes, and its `verified` is trustworthy (feature `set_text_fallback`). The input connection is the keyboard's own way into a field, so it reaches custom views that take text from no other way, whatever the keyboard; it needs Android 13 and `flagInputMethodEditor`, which changes nothing about the keyboard the person uses. The paste is for fields that drop committed text but paste, which few do.

**The input test app** (`android/inputtest`, `app.pinky.inputtest`) has one field for each case and a line that echoes what each holds: an ordinary EditText; one that refuses `ACTION_SET_TEXT`; one that also drops committed text, so only a paste fills it; a chat input built like WeChat's (a View, not a TextView, with its own input connection; it refuses `ACTION_SET_TEXT` and `ACTION_PASTE`, reports its hint as its text while empty, and sends on its editor action); an EditText of at most 4 characters; and a chat input that takes no text at all. Build it with `android/companion/gradlew -p android/inputtest assembleDebug`. `TestEmulatorBackgroundTypeFallback` (`internal/driver/android/typing_device_test.go`) types into every field on a background display.

Checked on the Android 16 emulator (`emulator-5640`, `zh-CN`, Gboard 简体中文 (拼音), the field on a background display), through the driver's `Background.Type`:

| Field | Result |
| --- | --- |
| Ordinary EditText, "hello" | `set_text` |
| Refuses `ACTION_SET_TEXT`, "你好" | `input_connection`, 0.35 s |
| Drops committed text, "粘贴" | `paste`, 1.6 s (the input connection waits 1.5 s for the field to change first) |
| Chat input, "微信", then "发送" with submit | `input_connection`, 30 ms; the editor action sent it |
| At most 4 characters, "abcdefg" | `set_text`, `verified: false`: the driver's error says the field reads 4 characters where 7 were typed |
| Takes no text | Refused with the three reasons; the driver's paste did not land either, and the agent gets an error saying so (5 s) |
| Drops committed text, phone locked | `the app did not accept ACTION_PASTE (apps cannot paste while the phone is locked)`; the same field pasted once the phone was unlocked |

The phone in the issue was not available. On HyperOS, check that the input connection reaches WeChat's chat input (the `PinkyText` log line names the way used) and that HyperOS does not restrict `flagInputMethodEditor` or the clipboard for accessibility services.

## Turning the service on and off

- `Enable` reads `enabled_accessibility_services`, appends only `app.pinky.companion/.PinkyAccessibilityService` and keeps every other entry, reads the setting back, then pings until the service answers (up to 10 s). If the setting read back lacks Pinky's entry, Enable stops at once with a `driver.CompanionStartError` whose reason is `setting_ignored` ([below](#when-the-phone-ignores-the-change-github-issue-94-2026-11-12)); `TurnOn` does the same. If the service never answers, Enable restores the setting itself.
- `Disable` removes the entry again. When nothing else changed, it writes back exactly the original text (or deletes the key if it did not exist), even if Android rewrote the list in its own order or short form meanwhile. When the person changed something else during the task (say, turned TalkBack on), only Pinky's entry goes and their change stays.
- If Pinky's service was already listed when Enable ran (the person turned it on, or an earlier run crashed), Enable leaves the setting alone and Disable does not turn it off; the Android driver then removes Pinky's entry with `ForceDisable` when it is done, which removes it whoever added it. The service's own idle watchdog covers the crash case: after a minute without any connection (three minutes in builds until 2026-11-08) it calls `disableSelf()`.
- **Kept on after Install, until 2026-11-08.** Pinky's Install turned the service on and sent `config` with `idle_disable_ms: 0`; Pinky no longer does (see [Only while an agent works](#only-while-an-agent-works)), but the app still keeps such a config. The app keeps config until the service is turned off, so the service stays on across reboots, app updates and `uiautomator dump`s, and its watchdog never fires. When the person turns it off in Android Settings (or Pinky's `ForceDisable` does), the app forgets it, and the next on-demand `Enable` gets the 3-minute backstop again. Checked on the Android 16 emulator: kept across a `uiautomator dump` (the log shows the service reconnecting in the same process) and a reinstall; forgotten after `ForceDisable` and after the watchdog turned the service off (with 12 s).
- **The watchdog counts across rebinds.** Android unbinds every accessibility service for each `uiautomator dump` and binds it again, and build 73 restarted the idle count with every bind, so a driver or gateway dumping now and then kept a service on for ever that nobody used (found by the Android driver session on the Android 16 emulator: still on more than 4 minutes after its last client, past the 3-minute limit). The count now lives in the app's process and only restarts when the service was turned on afresh, the check runs at every bind instead of 10 s later, and it uses elapsed realtime so that time asleep counts. Checked with the limit at 20 s and a `uiautomator dump` every 8 s: build 73 stayed on for 60 s, the fix turned itself off 25 s after the last client. On the Android 10 emulator, kept across a reboot: the service came back during boot, before the lock screen was unlocked, still with 0. On phones with file-based encryption, an app that is not direct-boot aware (the companion is not) can only start after the first unlock, so expect the service then.
- `accessibility_enabled` is never written. On the Pixel, Android set it from 0 to 1 when the service started and back to 0 when it stopped.
- This setting is the only one Pinky changes, and only its own entry, as docs/WORKING.md requires.

## When the service does not come on (GitHub issue #56, 2026-11-10)

An Android 10 phone over Wi-Fi kept showing the Turn on Pinky Companion card, and Turn on said the install had not finished, asking the person to tap Allow on the phone, where nothing happened. The app was installed; its service did not answer within `Enable`'s 10 s. The wait's error wrapped `context.DeadlineExceeded` (over Wi-Fi the last ping often runs into the deadline), and the client took that for its own 5-minute wait for the phone's install prompt running out.

Now the client says the install did not finish only when its own wait ran out. When the service does not answer, `waitReady` reads what the phone shows before `Enable` restores the setting (`probe.go`, one shell, about 150 ms) and returns a `driver.CompanionStartError`:

- the setting read back (did something take Pinky's entry off again?);
- `am get-started-user-state 0`: `RUNNING_LOCKED` means not unlocked since the phone started, and the app, which is not direct-boot aware, cannot start;
- `dumpsys accessibility`, only its lines about services: whether Android lists the service as enabled, binding, bound (by its label) or crashed (Android 11+), and whether a `Ui Automation[…]` is connected;
- `pidof` the app and its abstract socket in `/proc/net/unix`;
- the app's last `PinkyService`/`PinkyServer` log line, and a crash of the app in the last minute from the crash buffer.

`Reason` is `not_unlocked` or `ui_automation` when one of those explains it (unlocking comes first), and the client words it; otherwise the client shows `Detail`, the facts in English, with what to try. The driver logs the failure as `setting up the companion: …` (pinky.log, so it is in the diagnostics of Send feedback), and the checkup's companion item carries it.

**UI automation keeps every accessibility service off.** While a UiAutomation is connected, Android unbinds all accessibility services and binds none, unless the tool asked for `FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES`. `uiautomator events`, a `uiautomator dump` that hangs, uiautomator2 (ATX), Appium's UiAutomator2 driver and Maestro all hold one; people who try Pinky often have one of them around. Reproduced on the Android 10 emulator over a TCP transport (`adb connect`, as over Wi-Fi) with `uiautomator events` running: the service stayed unbound, and the error now reads `no answer within 10s; Android lists it as enabled but has not bound it; another tool holds Android's UI automation; its app is running; its socket is not listening; its last log line: "I/PinkyServer: socket closed"`. Without it the service came on in 176 ms. On the Android 17 emulator, which has a PIN and had not been unlocked since it started, both runs reported `not_unlocked`, and `Ui Automation[…]` shows on a line of its own there.

Ruled out for the issue: Android 10 over Wi-Fi as such (the emulator over TCP came on in under 200 ms, and the Pixel on Android 10 works over Wi-Fi); a settings write refused without an error (Pinky's adb uses shell protocol v2, which keeps the exit status, so a refusal would show `WRITE_SECURE_SETTINGS`; but a phone can also take the write and keep nothing of it, which no exit status shows: [issue #94](#when-the-phone-ignores-the-change-github-issue-94-2026-11-12)); a port blocked on the network (the forward runs inside adbd's connection). Not reproduced, and what the probe should now tell apart: a phone maker's autostart or battery manager that keeps the app from starting (MIUI, ColorOS, EMUI, and HarmonyOS, which reports Android 10), a crash when the service starts, and something removing the setting again.

### The Huawei Mate 10 (EMUI, Android 10), second round

The reporter's phone gave two answers. First: `something took it off enabled_accessibility_services again; Android does not list it as enabled; its app is not running`. On Android 10 and earlier, `AccessibilityManagerService` removes a package's services from `enabled_accessibility_services` when the package is force-stopped (`onHandleForceStop`), and EMUI's launch manager force-stops apps it manages automatically, so this fits. After the person turned off auto-manage and allowed auto-start, associated start and background activity, the setting stayed: `Android is still binding it; its app is not running; its socket is not listening`, and no log line of the app. So Android accepted the bind (it lists the service in `Binding services` only after `bindServiceAsUser` returned true) but the app's process never ran for 10 s, with no crash in the crash buffer. Stock Android starts the process for such a bind (the Android 10 emulator and the Pixel on Android 10 do, in well under 2 s); something in EMUI keeps it from starting or kills it at once. The earlier probe could not tell which: it read only the app's side.

The probe now also reads the system's side:

- `dumpsys package`: `stopped=true` (force-stopped, or not started since the install) and `notLaunched=true` (never opened);
- `dumpsys activity services <package>`: whether Android's `ServiceRecord` has a process (`app=null`), its `restartCount` and `crashCount`;
- `dumpsys activity processes`: whether Android counts the app as a bad process;
- the system's own log lines that name the package from the events, system and main buffers in the last two minutes (`am_proc_start`, `am_proc_died`, `am_kill` with its reason, `ActivityManager: Unable to launch app …`, a phone maker's startup manager), the last four, without bookkeeping (`SettingsShellCmd`, which would also name the person's other services, `ProcessStats`, `artd`, `am_compact`);
- a crash of the app in the last ten minutes instead of the last minute, with its age: an earlier try may have made it crash.

logcat's `--------- beginning of main` header no longer passes for the app's last log line. On the Android 16 emulator the whole probe ran in about a second.

The client also keeps the driver's English error for each phone whose service did not come on and puts it in the summary of Send Feedback's diagnostics (`Companion on <phone-1>: …`). Before, it was only in pinky.log, logged once per new reason, and in the reporter's diagnostics it had scrolled out of the last lines under the lease messages of a long remote session.

Not tried, and worth asking the reporter: opening Pinky once from the phone's launcher (it clears `notLaunched`, and some phone makers' managers start only apps the person has opened), then turning it on again.

### Third round

With 2026.1110.21 the reporter's phone answered `Android is still binding it; its app is not running; its socket is not listening`, and the only system lines naming the package were Huawei's `SPMN` settings provider logging the write of `enabled_accessibility_services` (with another app's accessibility service, MikaAccessibilityService of `com.wangzan101.mika`, listed before Pinky's) and `HwUidTcpMonitor` traffic counters for the app's uid. No `am_proc_start`, no `am_kill`, no crash, no `stopped=true`: Android accepted the bind and never logged starting the process, and EMUI logged no reason. Still open: whether EMUI's own start control (which logs elsewhere) holds the start, whether the other accessibility app matters, and whether a reboot or opening the app once from the launcher changes it.

The `SPMN` line published the person's other accessibility service in a public issue. The probe now leaves out every line that mentions `enabled_accessibility_services`, as it did `SettingsShellCmd`, and `HwUidTcpMonitor`.

## When the phone ignores the change (GitHub issue #94, 2026-11-12)

A vivo phone with OriginOS 6 (Android 16), over wireless debugging: `settings put secure enabled_accessibility_services …` returns without an error and writes nothing, and `settings get` reads `null` right after. Its Developer options have no switch like Xiaomi's USB debugging (Security settings) or ColorOS's Disable permission monitoring. Pinky then waited `Enable`'s 10 s for a service that could not come on, and the probe blamed the service. The reporter also turned the service on by hand, and it went off again as soon as they took over.

What Pinky does now:

- `Enable` and `TurnOn` read the setting back after writing it. Without Pinky's entry there, they stop at once with `driver.CompanionStartError{Reason: "setting_ignored"}` (`driver.CompanionStartSettingIgnored`), whose detail says the phone took the change without an error and did not keep Pinky's entry, and that the person can turn on Pinky in the phone's accessibility settings by hand. `Enable` still restores the setting as it found it, which on such a phone changes nothing; a phone that stores the list without Pinky's entry (the tests' `rewrite`) gets its text back. The detail never quotes the setting's value: it names the person's other accessibility services.
- The Android driver remembers it for the phone (`companionLink.manual`, and across Pinky's restarts in `companion-manual-phones.json` in Pinky's config folder, `Config.CompanionManualFile`; see below): `CompanionState` says `manual` (`driver.CompanionManual`) instead of `ready` or `off`, also after the person turned the service on by hand and it went off again, until a change of Pinky's is kept. Another failure after it (UI automation, say) shows as `off`, with its own words.
- The checkup's item fails with `turn_on_companion_by_hand` (`driver.FixTurnOnCompanionByHand`) and no Turn on button; the Pinky for Android card says the same without Turn on, and Settings › Phones says to turn it on by hand on the phone. Where the switch is: on vivo phones, Accessibility in the section of Settings for shortcuts and accessibility, described rather than quoted because OriginOS 6's menus could not be checked; elsewhere, Settings › Accessibility, where Pinky is among the downloaded or installed apps. Both say to search Settings when it is not there.
- Install and Turn on fail at once with `companionErrSettingIgnored`, and an agent whose background display cannot be read is told that the phone does not let Pinky turn the service on over USB debugging and that the person can turn it on by hand. Before, a task's first read on such a phone waited about 10 s for the warm-up's `Enable` and its probe, and every retry two minutes later did again.

Part 2 (the owner decided on 2026-11-12 that a service the person turned on on such a phone stays on): on a phone the driver knows as manual, a service the person turned on stays on until they turn it off on the phone. Nothing changes for other phones.

- The driver no longer removes Pinky's entry there: not when a task ends (`disableLocked`), not at the first `AgentWorking(false)` after the phone opens (`quietLocked`), and not after an update in place (`UpdateCompanion`, which reports `ServiceOn`). `replaceCompanion` still does, since the uninstall takes the service with it anyway.
- It stops the app's idle backstop (`SetIdleDisable(0)`, the Kotlin app unchanged) whenever it finds the service on there: as a task turns it on, in `quietLocked`, after an update, and in `CompanionState` (the phone's page asks every 30 s), so that a service the person turned on with no task in sight does not go off a minute later. The app forgets `idle_disable_ms` when the service goes off, so the next time the person turns it on gets the default minute until Pinky sees it.
- `CompanionState` says `manual_on` (`driver.CompanionManualOn`, additive; clients that do not know it treat it as `on`) while the service is on there; the checkup's item passes and says that the service stays on until the person turns it off in the phone's accessibility settings and that the phone's apps, banking apps among them, can see it while it is on; Settings › Phones says the same in short. The `manual` words (card, checkup) say it stays on once turned on, instead of that it goes off after the task.
- Where it is kept: the driver's own file, a sibling of `netStore` (`manualStore`, `manual.go`), by the id the provider lists the phone by, read when the device opens (`useManuals` in `OpenDevice`), before the gateway's first `AgentWorking(false)` runs `quietLocked`. Not in `update.State`: that is the auto-update session's, written by the client, and would reach the driver only through the gateway after the device is open. A change of the driver's that the phone keeps again removes the phone from the file.
- The phone is known as manual only after a write of the driver's was ignored. A phone where the person turned the service on before Pinky ever tried a write is handled like any other until then. The driver tells that the service is on by `settings get` listing it: if OriginOS 6 does not list a service turned on by hand, Pinky neither uses it nor keeps it on (the reporter's answer to whether it does is still open).

Only the reporter can tell: the model and OriginOS build; whether `settings get` lists the service once it is on by hand; whether the phone also ignores removing the entry; the menu path to the switch in OriginOS 6; whether the service now stays on after a task and after Pinky restarts.

Tests: `TestEnableOnAPhoneThatIgnoresTheChange`, `TestEnableWhenThePhoneDropsTheEntry` and `TestTurnOnOnAPhoneThatIgnoresTheChange` (fake phone with `ignoreWrites`), `TestCompanionManual` in the driver (`ignoringRunner`) and in the client, `TestCompanionInstallOnAPhoneThatIgnoresTheChange`. Part 2: `TestCompanionManualStaysOn` (after a task, after an update, across a restart through the file, and cleared when a change is kept) and `TestCompanionNotManualGoesOff` in the driver, `TestCompanionManualOn` in the client.

## Results

### Round 3: the two emulators

Main display, with the current build (versionCode 68), each run through the emulator's device lock: `companionctl install`, the device test suite (`PINKY_DEVICE=… go test -tags device ./internal/driver/android/companion/`), then a raw protocol script for event displays and notifications. `enabled_accessibility_services` was `null` before and after every run.

Both emulators have a test PIN that the Android driver session set for its unlock tests and asked to keep. `emulator-5562` was locked with it at first, and the device suite skipped there ("the phone is locked"), so the Android 10 checks first ran on a temporary AVD with the same system image (`system-images;android-29;default;arm64-v8a`, `emulator-5564`, deleted afterwards). Once that session shared the PIN, they ran on `emulator-5562` too, unlocked under its device lock and locked again afterwards.

| Check | Android 16 (`emulator-5560`, Google APIs) | Android 10 (`emulator-5562`; the temporary AVD) |
| --- | --- | --- |
| Install | `adb install` of the new key's APK, no prompt | The same |
| Enable | Answers 222 ms after the setting changes | Answers 78 ms after the setting changes (210 ms on the temporary AVD) |
| Device test suite | Passes: Settings (11 elements), every `set_text` case (Chinese and emoji by resource id, point with append, node handle, password field, `not_found`), toast event. Calculator skipped: neither image has one. | Passes the same way: Settings 11 elements (13 on the taller temporary AVD) |
| Event displays | Toast from the debug page `display: 0`, its window events `display: 0`, notification (permission granted inside the emulator) `display: -1` | The same (no permission needed) |

### Pixel (FA6C20303860), Android 10

| Check | Result |
| --- | --- |
| `adb install -r` | Works, no prompt |
| Enable, ping | Answers 1 to 2 s after the setting changes; `null` before, `null` after |
| Tree: home screen, Settings, Calculator, debug page | Works. Settings: 12 elements ("网络和互联网, WLAN、移动网络、流量使用和热点" and so on, rows merged into one button each). Calculator: 25 elements (digits by text, operators by description: "乘", "等于") |
| `set_text` Chinese and emoji into the debug page | Works: "你好，Pinky 世界 🎉" read back and verified; the debug page's echo (another process) shows 13 characters |
| Events | Toast with CJK text, notification (title and text) |
| Tree time, phone side | Settings 53 to 60 ms when the screen has not changed, 167 ms on the first read after it changed; Calculator 42 to 46 ms |

### Mi 10 Pro (3e8be48e), LineageOS, Android 17

Checked with `companionctl` against the build installed at 01:43, without reinstalling.

| Check | Result |
| --- | --- |
| Enable, Disable | `enabled_accessibility_services` was the owner's `com.wangzan101.mika/.a11y.MikaAccessibilityService`, became `…MikaAccessibilityService:app.pinky.companion/.PinkyAccessibilityService` during the run and went back to exactly the original text |
| Tree: Settings | 12 elements ("网络和互联网, 移动网络、WLAN、热点", …), 397 ms on the first read after the launch |
| Tree: Calculator | 37 elements (scientific keys included: "平方根", "正弦", digits, "等于"), 121 ms |
| `set_text` into the debug page | "你好，Pinky 世界 🎉" verified; the debug page's echo (another process) shows 13 characters |
| Toast event | Arrives, but this build reports "Toast 测试 ✓ Pinky Companion": SystemUI adds the app's name, which the current build drops (fixed and tested on the emulator) |
| Other accessibility services | Mika's floating button is an accessibility overlay window and shows up as `button "Mika"` above the app, as on screen |

### Android 16 emulator, round 2 (API 36, Google Play image, arm64 on the Mac)

The Pixel went offline before the final test run, and a new build cannot go onto the Mi 10 Pro without Play Protect asking, so the full device test suite ran on a temporary emulator (created for this, then deleted).

| Check | Result |
| --- | --- |
| Device test suite | Passes: Settings, every `set_text` case (resource id, point with append, node handle, password field, `not_found`), toast event. Calculator skipped: the image has none. |
| Enable | Answers 230 ms after the setting changes |
| Notification event | Works once the app may post notifications (granted inside the emulator only) |
| `wait_idle` after starting an activity | Settled after 1.0 s |
| `include_not_important` | Settings: 29 nodes, 3.4 ms median; with it on, 60 nodes, 4.8 ms (`uiautomator dump`: 63 nodes) |
| Keyboard | The input method window reports only the keyboard's area (`[0,1111,1080,1920]`), so elements under the keyboard are dropped and lists above it keep their visible part |

### Tree speed against `uiautomator dump` (step 0, item 5)

Round trip from the computer: the companion's time includes adb forwarding and JSON decoding; `uiautomator dump` is `uiautomator dump <file> && cat <file>` through `adb shell`, the way a driver without the companion has to read it. Ten samples each, taken while the same app stayed in front (`companionctl bench 10`).

| Device | Screen | Companion tree | `uiautomator dump` | Ratio |
| --- | --- | --- | --- | --- |
| Pixel, Android 10 | Settings | median 70 ms (phone side 63 ms), 31 nodes | median 2118 ms (min 1983, max 2892), 33 nodes | 30x |
| Emulator, Android 16 | Settings | median 4 ms (phone side 2 ms), 29 nodes | median 1975 ms (min 1946, max 2004), 63 nodes | ~500x |
| Emulator, Android 16 | Launcher | median 6 ms, 15 nodes | median 1956 ms, 30 nodes | ~300x |
| Mi 10 Pro, Android 17 | Settings | median 29 ms (phone side 24 ms), 36 nodes | median 2402 ms (min 2344, max 2451), 70 nodes | 83x |
| Mi 10 Pro, Android 17 | Calculator | median 30 ms (phone side 25 ms), 41 nodes | median 2314 ms (min 2286, max 2337), 50 nodes | 77x |

`uiautomator dump` pays about 2 s for starting `app_process`, connecting a UiAutomation and waiting for the app to go idle, whatever the screen. The companion pays one round trip to the app for the root of each window it walks (20 to 40 ms on the Pixel, under 1 ms on the emulator; the whole tree takes about 25 ms on the Mi 10 Pro) and reads the rest from Android's node cache. By default it walks only views that are important for accessibility; `uiautomator dump` walks all views, which roughly doubles the node count but adds no labels or actions.

## Decisions

- **Abstract socket with a peer check.** Simplest transport for `adb forward`, nothing on disk, gone when the service stops. Any app can connect to an abstract socket, so the server accepts only root, shell (adbd) and its own uid; anything else gets `forbidden`.
- **The debug page runs in its own process.** In the service's own process, Android's accessibility cache did not refresh a text view that changed after `set_text` (the field itself refreshed, the echo next to it did not). Every real app is another process, so the debug page is one too.
- **Only what agents need, by default.** Views that are not important for accessibility, the keyboard's keys and the status and navigation bars are left out: they add nodes and time but no labels or actions, and the status bar clock would change every minute and show up in every diff. Each can be turned on (`include_not_important`, `ime`, `bars`). Windows that are not walked still count for occlusion.
- **Elements, not nodes.** A clickable row without its own label takes its descendants' text as one label ("Network & internet, Wi‑Fi, …") and their switch or checkbox state, the way a screen reader reads it; the texts are not listed again. A target merges only while it covers at most half the screen and holds at most 12 texts, so a clickable root never swallows the screen. A target with its own label keeps its children's texts as separate lines. Unlabeled targets are listed only when small and mostly on screen.
- **Labels and roles.** Label: content description, else text; inputs show their text, else hint, else description; labels are trimmed, whitespace collapsed, capped at 1000 runes (merged ones at 500). Roles map class names onto the screen model's roles (`EditText` to input, `Switch`/`ToggleButton` to switch, `CheckBox`/`RadioButton`/`CheckedTextView` to checkbox, `TabView` to tab, scrollables and list classes to list, clickables to button, images to image), and an app's role description ("Tab", "Switch", "link") wins when present. States follow the order of the constants in `internal/screen`; `editable` is not repeated on inputs.
- **Paths** are the window type and package, then each ancestor's short class name and resource id name (`application:com.android.settings>FrameLayout>RecyclerView#recycler_view`). No sibling indices, so refs survive rows being inserted above.
- **Occlusion.** An element whose center lies under a higher window (keyboard, dialog, status bar) is cut to its largest uncovered part, or dropped when nothing useful is left.
- **Password text never leaves the phone.** The app omits `text` for password fields (with "speak passwords" on, some versions expose the real characters), `set_text` does not read it back, and the client ignores it even if one arrives.
- **Foreground app.** The top-most focused or active application window, or a focused full-screen system window (the notification shade, the lock screen). App names load in the background (the first lookup of a package loads its resources, 100 ms or more on the Pixel), so the first tree after an app opens may use the window title instead.
- **Active window root through the cache.** `AccessibilityWindowInfo.getRoot()` bypasses the node cache and drops what it prefetched, so every child costs another round trip; `getRootInActiveWindow()` keeps the prefetched nodes. Together with not walking the bars, this took the Pixel's Settings tree from 58 to 169 ms down to 53 to 60 ms.
- **Node handles** are `AccessibilityNodeInfo.hashCode()` (the view and window ids), stable while the view lives, so `set_text` can target exactly the node a tree showed.
- **Submit on Android 10.** `ACTION_IME_ENTER` exists from Android 11. Below that, `set_text` reports `submitted: false` and the caller presses Enter itself; the client does not inject keys behind the driver's back.
- **Events** are folded for content changes (one per 100 ms per connection, with a count) and queued per subscriber, so a slow reader never blocks the service. Times are converted to the host's clock using the skew `ping` measures.
- **Idle watchdog** of a minute without any connection (three minutes in builds until 2026-11-08), changeable with `config`; 0 keeps the service on, and the app keeps that until the service is turned off, so a reboot or an update does not undo it. Pinky's Install set 0 until 2026-11-08 and sends nothing now. A service the person enabled by hand in Android Settings gets the backstop too.
- **Display support is a feature, not a new protocol version.** It is additive, and the driver accepts exactly protocol 1, so a new number would have made it refuse every new app until it changed. An older app ignores `display` and would act on the main display, so clients check `features` before sending one.
- **Unknown displays are -1, never absent.** A client must not mistake an event of unknown origin for one of the main display (a toast from the person's app shown to an agent working in the background, or the other way round), so every event carries `display` and -1 (Android's `INVALID_DISPLAY`) means unknown. Filtered subscriptions drop unknown events, except notifications, which belong to the phone.
- **Input focus per display.** The phone's input focus is on the display touched last, and the other displays have no focused window, but their views keep their focus. So `set_text` without a target uses the phone's focus only when it is on the requested display, and otherwise the focused view of that display's focused or top-most application window. Searching only that one window means a dialog without a focused field gives `not_found` instead of the field behind it.
- **Android 10 gets a clear `unsupported`** for other displays rather than an empty tree: its accessibility services see only the main display's windows, and its events do not say which display they came from.
- **versionCode counts commits up to the last companion change** (the plain commit count until 2026-11-10). It grows only when the companion changes, never goes down, needs no state anywhere, and any machine with the history computes the same number. With the plain count every Pinky release had a new companion, which a phone that refuses installs over USB had to install by hand each time (DIYgod/pinky#54). A shallow clone fails the build rather than produce versionCode 1, which every newer app would refuse to downgrade to.
- **The version also sits in the APK as `assets/pinky-companion.json`,** so Pinky reads a bundled APK's version with `archive/zip` instead of the Android SDK's `aapt2` or a binary manifest parser.
- **The debug build ships.** `make companion` builds it and `make app` copies it into Pinky.app. It is debuggable, which adds nothing to what adb already allows. The release build (`./gradlew assembleRelease`, minified: 90 KB against 2.7 MB) is signed with the same key and has the same version file, should the packaging prefer it; it was built but not run on a phone this round.
- **Runner interface.** The client needs only `Shell`, `Forward`, `RemoveForward` and `Install`. `ExecRunner` uses the adb command line (the SDK's adb first, as the design says); the Android driver can pass its own adb implementation when the main session integrates the two.

## What does not work yet

- **Play Protect on the Mi 10 Pro.** The first `adb install -r` there, at about 01:43, opened Google Play Protect's "Send app for a security check?" dialog (always send / send this time / don't send), and the install waited for it. Per the safety rules nobody answered it and the companion session stopped using the phone. At about 01:54 the Android driver session's tests began with a Home key press, which hid the dialog, and the install completed; at 02:08 the phone showed the launcher with nothing unusual in the activity stack. The owner may want to check Play Protect's settings. A newer build would ask again, so the Mi 10 Pro still runs the 01:43 build; install the current one (`companionctl install`) while the owner is around, then run the device test there.
- **The Pixel went offline** at about 01:45, during the Android driver session's tests, and macOS no longer sees it on USB. The cause is unknown: its old battery may have browned out with the screen on (Settings showed it discharging while plugged in, 21% and seven minutes later 17%), or a reboot did not finish, or the cable. Someone has to look at it. The APK changes made after that ran on the emulator only.
- **Play Protect on other phones.** Any phone with Google Play and "verify apps over USB" may ask the person about an unknown APK, as on the Mi 10 Pro. Setup should explain it. Every build is now signed with the same key, so Play at least sees the same app each time.
- **The Mi 10 Pro's build must be uninstalled before the current one goes on**: it is signed with another key (see [Versions and signing](#versions-and-signing)), and it predates `features`, so the driver must not send it `display`.
- **`uiautomator dump` turns the companion off while it runs** (Android unbinds every other accessibility service for a UiAutomation), so a driver must not mix the two; the client reconnects once by itself afterwards.
- **Screen off.** With the screen off, Android lists only the status bar window, so the tree is empty. The driver should check the power state itself.
- **Background displays on Android 10** are not supported by the companion (`unsupported`), so the driver's background mode there has no element tree. Android 11 and 12 had no emulator this round: their event displays come from the window list and are untested.
- **Not tested:** background displays on a real phone (the Mi 10 Pro runs the 01:43 build, which ignores `display` for `set_text` and events), background displays while the phone is locked (the Android driver session's check), WebView, Compose and Flutter apps, landscape on a real phone, toasts and notifications with the companion running for hours, and `include_not_important` on Android below 13 (it cannot clear the node cache there).
- **Shipping:** decided in round 3: the APK goes into Pinky.app and the `.dmg` (`make app`), and Pinky installs it when the person clicks Install. A CI build of the APK needs the whole git history for its version code.
- **Possible speedups:** non-active windows (dialogs, the shade) still pay a cache-bypassing root fetch per tree; a client could also keep one subscription and request trees only after `content` events.

## Integration notes for the driver

1. When a task starts: `InstallIfChanged(apk)`, then `Enable`. When it ends, or after about a minute idle: `Disable`.
2. `Observe`: `Tree` gives a `screen.Screen` without refs; add `ScreenEvent()` results from an `Events` subscription to `Screen.Events`, filtered by the gateway's policy.
3. `Type`: `SetText(Target{Point: &center, ID: element.ID}, text, SetTextOptions{Append, Submit})`, or `Target{}` for the focused field. When `Submit` was asked and `Submitted` is false, press Enter through scrcpy.
4. Settle: the client implements `driver.Settler` (`WaitSettled`, 300 ms of quiet), which can back up the frame-size detector.
5. Fall back to `uiautomator dump` only when the companion is not available, never while it is on.
6. Input method windows: show the elements of one that comes with a `root`: the companion walks it when it covers the screen like a sheet, or when `ime` was asked for. Cut elements under higher windows by their `region` when there is one, else by `bounds`.
7. Background displays: when `ping`'s `features` holds `displays`, pass the display id to `tree` (`DumpOptions.Display`), `set_text` and `wait_idle`, and route events by their `display` (or subscribe per display). Treat -1 as unknown, never as the main display. Without the feature, do not send a `display` at all.
8. Install: compare the installed versionCode (`dumpsys package`) with the bundled APK's (`assets/pinky-companion.json`). On `INSTALL_FAILED_UPDATE_INCOMPATIBLE`, uninstall Pinky's own package while its service is off, then install again.

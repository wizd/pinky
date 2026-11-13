# On-phone tasks on a background screen, over Wireless debugging

Pinky on an Android phone can run its own tasks on a hidden second screen (a virtual display) and show it in a small floating window, so the person keeps using the phone meanwhile. An ordinary app cannot start other apps on a display of its own ([android-virtual-display.md](android-virtual-display.md)), so Pinky gets the shell user's privileges the way Pinky on a computer has them: it pairs with the phone's own Wireless debugging and runs the scrcpy server's background display from that connection. The feature is off by default, works on Android 13 and later, and is turned on in Settings › Background screen. Without it, or when it cannot connect, tasks run on the main screen through the accessibility service as before.

Branch `claude/android-wadb-display-02fd70`, 2026-11-13.

## Parts

| Part | Where | What it does |
| --- | --- | --- |
| adb client | `internal/driver/android/wadb` | Pairing (TLS 1.3 + SPAKE2 over edwards25519, then the PeerInfo exchange under HKDF/AES-GCM), and the adb transport over TLS (CNXN, STLS, TLS, CNXN; streams with OPEN/OKAY/WRTE/CLSE). `adb.Client.Direct` gives the existing adb code (push, shell, forward) a connection to it. The RSA key lives in the app's private files (`files/…/wadb/adbkey.pem`, 0600) |
| Manager | `wadb/manager.go` | The setting (`wadb.json`): on/off, paired, the port. Status off, unpaired, no_port, ready, connected, refused, failed. Connects only while on and a task needs it; `SetOn(false)` closes the connection at once |
| Background display | `internal/driver/android/local/background.go` | With a shell and SDK ≥ 33 and a companion that knows displays, `OpenBackground` starts the scrcpy server's virtual display (1280 max, 30 fps, 4 Mbps, no system decorations, IME on the display) with the task's app. Reads, taps and SetText go through the accessibility service with the display id; keys and the person's touches through scrcpy's injection |
| Core | `internal/standalone/display.go` | Control commands `shell` (on, off, port, pair, forget), `video`, `takeover`, `handback`, `touch`; `state.shell` and `state.background` |
| Setup screen | `BackgroundActivity`, `Pairing`, `AdbServices` | Explains the feature, asks for the person's yes, links to Developer options and Wireless debugging, takes the pairing code from a notification's reply box or its own field, shows the status |
| Port | `Overlays` + `AdbServices` | While the feature is on, the service process looks for this phone's `_adb-tls-connect._tcp` record (DNS-SD) and tells the core its port, or 0 when none is found in 5 s. A record counts only when its host is one of the phone's addresses and adbd answers on that port on loopback (below) |
| Floating window | `AgentWindow` | The display's live picture (MediaCodec on a SurfaceView); small: Larger and Stop; large: Smaller, Take over/Hand back, Stop |

## The person's steps

1. Settings › Background screen › Use the background screen. A dialog says what Wireless debugging gives Pinky and what Pinky uses it for; Turn on is the person's yes.
2. Developer options: the row opens them, or About phone when they are off (seven taps on Build number). Pinky never turns them on.
3. Wireless debugging: the row opens Developer options scrolled to it (`:settings:fragment_args_key` = `toggle_adb_wireless`). The person turns it on.
4. Pair Pinky: Pinky asks for the notification permission (Android 13), starts looking for `_adb-tls-pairing._tcp`, posts a notification with a reply box, and opens Wireless debugging. The person taps Pair device with pairing code and types the six digits into the notification without leaving the dialog. Without notifications the code goes into the screen's own field, in split screen. The core pairs (`shell op pair`), and the notification says how it went.

From then on the status reads Ready or Connected. When Wireless debugging goes off (reboot, another Wi-Fi), the port disappears, the status reads "Wireless debugging is off", and tasks run on the main screen until the person turns it on again; nothing is turned on for them. Forget pairing deletes Pinky's key (the phone's own list of paired devices is the person's to clear).

Mika's flow was the model for the steps (pair once on the phone, a small live view); no code of it is used.

## During a task

The core uses the background display when the feature is on and connected, and the phone qualifies; otherwise the main screen. `state.background` tells the service to show the floating window while the task runs. The window plays the core's `video` command: one JSON line (`{"ok":true,"video":{"codec","width","height"}}`), then frames, each a 12-byte big-endian header (PTS in µs with bit 63 = config and bit 62 = key frame; length) and the data. `no_display` means the task has not opened its display yet: the window tries again each second; `own_screen` means the task runs on the main screen after all, and the window goes away for that task.

Approvals and handoffs show on the card as on the main screen. Tapping the picture while Pinky works takes the task over (the tap does not press anything); the window grows (and returns to its earlier size after Hand back), and the person's touches on it go to the background display as `touch` commands on one connection kept open (moves that pile up are dropped). Hand back gives it back; Stop stops the task. The person's own use of the main screen does not pause a task on the background display (the gateway's `byInput` rule for background leases). The window's views are sensitive and its rect goes to OwnUI, like the pill and the card, so agents can neither read nor press it.

## Android versions

| Android | Result |
| --- | --- |
| 16 (API 36), emulator `sdk_gphone64_arm64` | End to end in the app (below), and with `bgcheck`: pairing, connection, display opened with Settings in 1.3 s, observe (12 elements) on it while the main screen showed the launcher, tap 54 ms, Back 435 ms, H.264 576×1280 at ~40 packets/s, close 12 ms |
| 13–15 | Not run. Virtual displays with `new_display` and the companion's display reads need 13 (`MIN_SDK`); 14 gives a display its own focus, which keeps the person's keyboard on the main screen |
| 11–12 | Wireless debugging exists, but scrcpy's `new_display` and the accessibility reads per display don't: the setting says it needs 13 |
| Real phones | Not run: no real phone's settings were changed for this work. Xiaomi's HyperOS drops injected input on background displays (desktop issue #48); taps go through the accessibility service, but the person's touches on the window go through scrcpy injection and may not work there |

`bgcheck` (`internal/driver/android/local/cmd/bgcheck`) runs the whole path on a phone from the shell user, with a key that is already paired. On an emulator SELinux stops the shell user from opening the app's companion socket (`connectto` on an `untrusted_app` socket), so it needs `setenforce 0` there; inside the app the core is that app and this does not apply.

### In the app, API 36 emulator, 2026-11-13

Debug build, `fakemodel -script loop` as the model, `corectl start` for the task:

| Check | Result |
| --- | --- |
| Setup | Settings › Background screen, the consent dialog, Pair Pinky asked for notifications and opened Developer options; the code typed into the notification's reply box paired (`AdbDebuggingManager: Pairing succeeded`); the status read Ready |
| Wireless debugging off and on | Off: the status stayed "Wireless debugging is off" for 30 s; on: Ready within 5 s with adbd's new port |
| A task | Settings opened and browsed on the background display while the home screen stayed the person's; the floating window played it |
| Take over | A tap on the picture paused the task (`state.task.state` "paused"), the window grew; taps and swipes on it opened and scrolled Settings › Apps › All apps on the hidden display |
| Hand back, Stop | Hand back resumed the task and the window shrank back; Stop in the window ended it, the display and the scrcpy server went away |
| Turning it off | `state.shell.status` went `off` and the TCP connection to adbd disappeared at once (`ss -tn`) |

Two faults turned up and were fixed:

- **The first read after a launch failed**: accessibility did not track the new display yet (the companion answers `unsupported`), and the gateway moved the task to the main screen for good two seconds in. The phone's driver now does what desktop Pinky does (`launchGrace`): such reads count as an empty display for 10 s after a launch, nudging the display with a key that does nothing and then a second start of the app.
- **A stale or foreign port**: both emulators share netsim's Wi-Fi and have the same address (10.0.2.16), so the other emulator's `_adb-tls-connect._tcp` record looked like this phone's, and a record can outlive Wireless debugging being turned off. A port now counts only once adbd answers on it on loopback (checked off NSD's thread). The pairing port is not probed: a stray connection could use up the pairing dialog's one attempt.

## Security

While the feature is on, Pinky can hold a connection with the shell user's rights: it could install apps, read logs and grant permissions. What it does with it:

- push the scrcpy server jar to `/data/local/tmp` and run it with `app_process`, with options for a virtual display and the task's app;
- read that server's video and send it keys and the person's touches.

Nothing else goes over the connection, and the setup screen and the consent dialog say so. The connection is opened when a task needs the display and closed when the feature is turned off (`Manager.SetOn(false)`, tested in `TestManagerPairConnectAndOff`). The key stays in the app's private storage and never leaves the phone; Forget pairing deletes it. Pairing only accepts records whose host is one of the phone's own addresses, so another phone's pairing dialog on the same Wi-Fi is never used. The pairing notification's reply goes to an explicit, unexported receiver.

Pinky does not grant itself `WRITE_SECURE_SETTINGS` to turn Wireless debugging back on after a reboot, as Mika presumably does: turning a system setting on is the person's to do.

## Left

- The floating window cannot be dragged; it sits at the top right (small) or top centre (large).
- NSD discovery runs in the service while the feature is on; its battery cost was not measured.
- Android 13–15 emulators and real phones untested; the Japanese and Chinese strings want a native read.
- The pill keeps reading "Working · <last step>" while the person has the task (as on the main screen); the window says "You have control".
- The setup screen's own pairing-code field (without notifications) was not driven; the notification reply was.

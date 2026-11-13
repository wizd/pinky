# Pinky on the iPhone

Pinky's iPhone app runs agent tasks on the iPhone itself, with no computer at run time: the person types a task, the agent works in the phone's other apps, and Pinky's rules, approvals (behind Face ID), Stop, Live Activity and history work as on the desktop. It is sideloaded only. Its design, the research behind it and what is left are in [docs/notes/iphone-app.md](../docs/notes/iphone-app.md).

## How it works

- **Loopback.** A packet tunnel routes 10.7.0.1 into a utun and sends each IPv4 packet back with source and destination swapped, so a connection to 10.7.0.1 reaches the phone itself. There `remotepairingd` answers RemotePairing as it would a computer on the network. Its port changes from phone to phone and with each reboot; the app finds it in the `_remotepairing._tcp` service `remotepairingd` advertises on `lo0` (49152 is only the fallback). LocalDevVPN does the same reflection; the lite build relies on it.
- **Pairing.** `cmd/rppair pair` asks `remotepairingd`, once and from a Mac the phone already trusts, to pair a fresh host identity over lockdown (`com.apple.dt.remotepairingdeviced.lockdown`, fixed PIN). The phone shows nothing. The app keeps the identity in its Keychain. On the phone the Go core pair-verifies, opens a tunnel listener, speaks TLS-PSK to it and hands the CDTunnel to Pinky's own iOS driver (`internal/driver/ios`, `OpenOverTunnel`): display stream, HID, the accessibility tree, screenshots, the app list and the lock state over RSD, as on the desktop.
- **Core.** [onphone](onphone) is Pinky's gateway on the phone: `internal/standalone`'s core (the same rules, approvals, history and agent loop, `internal/pilot`, as Android's) over a guarded device. The guard refuses input while Pinky's own app is in front, so an agent can never tap Approve or Stop, hides Pinky from the app list, and goes back to the task's app after the person answers in Pinky. [core](core) is the C library (`-buildmode=c-archive`, `ios` build tag) that the app and its packet-tunnel extension link: it connects through the loopback, retries, and answers the app's JSON calls (`onphone`'s package comment lists them).
- **App.** SwiftUI: the task and its steps, approvals (Allow needs Face ID or the passcode) and handoffs, Stop, history, settings for the model (Claude through the Anthropic API by default, or any OpenAI-compatible server), the API key (in the Keychain, never shown again) and when Pinky asks first. A Live Activity follows the task and offers Stop. Local notifications announce approvals and ended tasks; they carry no buttons and only open the app.

## Layout

| Path | What |
| --- | --- |
| `onphone/` | the core: guard, link state, the JSON calls |
| `core/` | the C library around it, the connection through the loopback |
| `rppair/`, `cmd/rppair/` | RemotePairing pair-setup over lockdown, pair-verify, the tunnel listener; the Mac tool that makes and checks the pairing |
| `app/` | the Xcode project (XcodeGen): `App`, `Tunnel` (packet tunnel, where the core runs), `Activity` (Live Activity), `Shared` |

## Building

Xcode 27, XcodeGen and Go. On the Mac:

```sh
go test ./ios/...                    # core, rppair
ios/build.sh core                    # the Go core only
PINKY_SIGN=0 ios/build.sh full       # compile check, unsigned
ios/build.sh sim                     # for the Simulator: the app's screens only
```

The app speaks English, Simplified Chinese and Japanese, with desktop Pinky's terms (`cmd/pinky/locales`). Its words are in the string catalogs `app/Shared/Localizable.xcstrings` (the app and its extensions) and `app/App/InfoPlist.xcstrings` (the permission prompts); add the Chinese and Japanese with every new or changed English string. A build collects new strings with `SWIFT_EMIT_LOC_STRINGS=YES`, then `xcrun xcstringstool sync app/Shared/Localizable.xcstrings --stringsdata <the build's .stringsdata files>` adds them to the catalog. Steps, approvals and summaries come from the core in English, as on Android. To see the screens in a language, launch with `-AppleLanguages "(ja)"`. The Simulator runs no packet tunnel, so the app there never gets a core; launch a debug build with `-pinkyDemo <scenario>` (`setup`, `model`, `ready`, `running`, `asks`, `ended`, `computer`, `steer` or `continue`) to see a screen with canned state and no core (`app/App/Demo.swift`), for example `xcrun simctl launch booted cc.diygod.pinky.standalone -pinkyDemo running -AppleLanguages "(zh-Hans)"`.

The app's bundle id is yours to choose (`PINKY_BUNDLE_ID`, an App ID of your team; the default is the maintainer's). The app derives every other name from it. Choose `pinky.<your team id>`: Pinky on a computer then knows the app as Pinky's too and keeps its agents off the app's screen, as it does for the maintainer's `cc.diygod.pinky` ids.

To run it on a phone:

1. Pair once, from a Mac the phone trusts (under `scripts/devlock.sh` in this repository). Keep the file outside the repo: whoever holds it can open a CoreDevice tunnel to the phone from its network.

   ```sh
   go run ./ios/cmd/rppair pair -udid <udid> -out ~/.pinky-standalone/rp_pairing.plist
   go run ./ios/cmd/rppair check -udid <udid> -id ~/.pinky-standalone/rp_pairing.plist -addr <phone's Wi-Fi IP>:<port>
   ```

   The port is the one the phone advertises on Wi-Fi: `dns-sd -B _remotepairing._tcp`, then `dns-sd -L` on its name. It was 49152 on the test phone.

2. Build with the pairing inside; the app moves it into its Keychain on its first start. Xcode builds unsigned and `build.sh` signs each part with the development profile Xcode keeps for its App ID (`~/Library/Developer/Xcode/UserData/Provisioning Profiles`), or the team's wildcard profile when it has none, so Xcode needs no signed-in account.
   - **Full** (own packet tunnel; a paid developer account, since a personal team cannot sign the Network Extension capability). It needs three explicit App IDs and a development profile for each that lists the phone: the app's id and `.tunnel` with the Network Extension capability, and `.activity`. With the App Store Connect CLI, for example:

     ```sh
     asc devices register --name "<the phone's name>" --udid <udid> --platform IOS     # once per phone
     asc bundle-ids create --identifier <id> --name "Pinky"                            # and <id>.tunnel, <id>.activity
     asc bundle-ids capabilities add --bundle <its id> --capability NETWORK_EXTENSIONS # the app and .tunnel
     asc profiles create --name "Pinky Development" --profile-type IOS_APP_DEVELOPMENT \
       --bundle <its id> --certificate <your development certificate's id> --device <the phone's id>
     asc profiles local install --id <the profile's id>
     PINKY_BUNDLE_ID=<id> PINKY_PAIRING=~/.pinky-standalone/rp_pairing.plist PINKY_UDID=<udid> ios/build.sh full
     ```

   - **Lite** (no tunnel of its own, so a personal team can sign it; needs LocalDevVPN from the App Store, and the core runs in the app, which background audio keeps running during a task). Its ids add `.lite`; under a paid team's wildcard profile they need no new App IDs:

     ```sh
     PINKY_BUNDLE_ID=<id> PINKY_PAIRING=~/.pinky-standalone/rp_pairing.plist PINKY_UDID=<udid> ios/build.sh lite
     ```

3. Install: `xcrun devicectl device install app --device <udid> <the .app the build printed>`.
4. On the phone: open Pinky, allow notifications, Turn on (the full build asks to add a VPN configuration; with the lite build turn LocalDevVPN on first), add the model's API key in Settings, then type a task.

To host WebDriverAgent for Pinky on a computer, as desktop Pinky's install builds it ([notes](../docs/notes/iphone-app.md#hosting-webdriveragent)), add `PINKY_WDA=<WebDriverAgentRunner.xctest>`, the test bundle from a `build-for-testing` of WebDriverAgent (in `WebDriverAgentRunner-Runner.app/PlugIns`). `ios/build.sh kit <out.tar.gz> [version]` packs the iPhone app kit a release carries; `PINKY_IPHONE_KIT=<the archive or its unpacked folder>` has desktop Pinky's `pinkyctl helper install` build the app from it.

The core logs to the unified log with the prefix `pinky` (connection attempts and why they failed, slow or failed screen reads, and with `heartbeat_s` a heartbeat with memory), which a Mac reads with the device's syslog. It never logs a task's text, the model's key or what is on the screen.

A debug build also starts a task from launch arguments once the phone is ready, so a test needs no typing. The key comes in the launch environment and stays in memory; never put it in a file. The `--` keeps devicectl from reading the app's arguments as its own. `-service <id>` picks a model service by its ID (`docs/notes/model-services.md`), or `-provider` with `-baseURL` names one as settings did before there were services; `-model` and `-baseURL` override the service's for the run. `-pinkyHeartbeat 1` has the core log its memory every second. `-pinkyForgetPairing YES` drops the pairing the app keeps in its Keychain, which outlives deleting the app, so the app starts unpaired (for trying pairing on the phone); pairing logs its steps with the prefix `pinky pairing`.

```sh
xcrun devicectl device process launch --device <udid> \
  --environment-variables '{"PINKY_MODEL_KEY":"…"}' \
  <id> -- -pinkyTask "…" -service deepseek -model <model>
```

The task starts only at the app's launch. If Pinky is running, end the app first by its process id (`xcrun devicectl device info processes`, then `xcrun devicectl device process terminate --pid <pid>`). The new app keeps the tunnel that is up, with the options it was started with. To start the tunnel again (for `-pinkyHeartbeat`, say), end the tunnel extension (`PinkyTunnel`) by its process id as well and launch about 8 s later. `--terminate-existing` ends both and launches at once: the new app still finds the VPN on and does not start it, and iOS then gives up on the dead extension.

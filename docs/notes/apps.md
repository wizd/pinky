# Installing, updating, uninstalling and stopping apps (phone_app, 2026-11-09)

A developer's agent builds an app, puts it on the phone, runs it and checks it. `phone_app` gives it the three steps the screen tools cannot do: install (or update) an app from a file on the computer, uninstall an app, and stop (force-quit) one.

## The tool

One new tool, `phone_app`, with `action: install | uninstall | stop`, rather than actions on `phone_apps`:

- `phone_apps` is annotated read-only, and some agent hosts run read-only tools without asking the person at their end. An install or uninstall must not ride on that hint, so the actions go in a tool annotated destructive.
- One tool with three actions adds one line to the tool list, where three tools would add three.
- `install` takes `paths`: one `.apk`, `.ipa` or `.app` folder, or the split APKs of one app together, relative to the phone's folder on the computer (`<data>/files/<phone id>/`, the folder `phone_files` uses). Nothing outside that folder: no absolute paths, no `..`, no symbolic links on the way. An agent on another computer uploads the file there first with `phone_files` `upload`; `phone_files` `list` and `upload` work for iPhones too now, on the computer's side only (an iPhone has no Pinky folder to copy to).
- `uninstall` and `stop` take `app`, an installed app's name or id, matched as `phone_launch` matches it.
- `device`, `intent` and `approval_id` work as on the screen tools.

The result says what happened in plain words; the history records "Installed “Dinely” 1.3 (14) from “app-debug.apk”", "Updated “Dinely” to 1.3 (14) …", "Uninstalled “Dinely”", "Stopped “Dinely”", and the same with "Waiting for you:", "Blocked by …:" and "Failed:" in front when they did not happen.

## Reading the package first

The approval names the app the file holds, never only the file: the person cannot judge `app-debug.apk`, and an agent could name a file anything. Before asking, the driver reads the package (`driver.AppManager.InspectPackage`):

- **APK** (`internal/apppkg`): the binary manifest gives the package name, `versionCode`, `versionName`, the split name and the label (resolved through `resources.arsc` when it is a resource); the APK Signing Block (v3, else v2) or, for old v1-only APKs, the PKCS#7 signature gives the signing certificate, shown as its common name and the start of its SHA-256. Split APKs must share one package and one signer, and exactly one of them must be the base.
- **IPA or .app**: `Info.plist` gives the bundle id, name and version; `embedded.mobileprovision` gives the team that signed it, the devices the profile lists and when it expires. The iPhone driver refuses before asking when the build is for the Simulator, has no profile, the profile has expired, or the profile does not list this phone (and is not an enterprise profile for all devices): each in plain words, with what to do (add the phone's UDID to the profile, build again). The phone's own verdict (`ApplicationVerificationFailed`, a profile or entitlement error) is put the same way when it comes after the transfer.
- The driver also says which version is installed now, if any, so the card can say "Update “Dinely” to 1.3 (14)" and "replaces 1.2 (13)".
- The signer on the card: an APK's certificate common name and the start of its SHA-256, "Pinky Test (292579aa…)"; an iPhone build's team from its profile, "Example Dev (A1B2C3D4E5)". The profile is read with the parser that reads the WebDriverAgent helper's profile (`wda_profile.go`), which now keeps the team's name and id too.

The files the person approved are what gets installed. After the yes, the gateway copies them into a new temporary folder (a `.app` folder whole, without symbolic links), reads that copy again and installs the copy. If the copy holds another app id, version or signer than the card showed, nothing is installed and the agent is told to call again: a build that finished between the question and the answer gets its own question.

## Approvals

The panel reads: title "Install “Dinely” 1.3 (14) on Pixel 9?" (or "Update “Dinely” to 1.3 (14) on Pixel 9?"), detail "Claude Code · com.example.dinely · signed by Example Dev (ab12cd34) · from “app-debug.apk” · replaces 1.2 (13)"; "Uninstall “Dinely” from Pixel 9?" with "Claude Code · com.example.dinely · its data on the phone is deleted too".

Why each answer is what it is:

- **Installing or updating from the computer asks, whatever the preset.** The presets are about steps on the screen, which the person can watch and stop. An install is code the person has not seen, which then runs with what it asks for; an update replaces an app and keeps its data, so a bad build can read what the app holds. Rule "Installing from the computer". The person's earlier answers to other rules do not lift it.
- **Allow for this task** covers further installs of the same app id signed by the same signer, for the rest of the task: the build–install–check loop asks once. Another app, or the same id with another signer, asks again.
- **Always allow, narrowly** (added 2026-11-10, issue #72). A standing yes to install anything from the computer would let any agent replace apps on any later day without the person looking, so there is none. But a developer who builds, installs and tests across many tasks had to approve every task, so an install offers Always allow for that agent on that phone, for builds of that app id signed by that signer (`policy.Grant`, kept under `always_allow` with `agent`, `device`, `app`, `signer` and `signer_name`; see `internal/policy/standing.go`). A build signed with another key, another app id (whatever its name), another agent or another phone asks again. It is not offered, and does not apply, while every action asks, nor when a rule of the person's own asks about the app too (it would keep asking). Everything refused stays refused: the refusals and read-only mode come before the question. Settings › Safety › Always allowed lists it with the app, the agent, the phone and the signer, and its × makes it ask again; removing the phone drops it.
- **Uninstalling is a deletion and always asks** (rule "Uninstalling apps"). Allow for this task covers uninstalling the same app again in this task, after the agent installed it again: by then its data is what the task made.
- **Stopping does not ask.** It loses what the app had not saved, like swiping it away in the app switcher, which the agent can do anyway. Under "Every action" it asks, as every step does.
- **Refused outright** (`StatusBlocked`, with the rule named), whatever the rules file says:
  - Pinky for Android (`app.pinky.companion`, `app.pinky.companion.dev`) and, on iPhone, the helper: an agent could remove or replace what gives Pinky its access. Rule "Pinky's own apps". The helper is any XCTest runner, an app whose bundle id ends in `.xctrunner`, whatever comes before (2026-11-11): Pinky Helper keeps the id it was first installed with, such as iPhone Use's `cc.diygod.iphoneuse.wda.xctrunner` or a `--bundle-id` given to `pinky helper install`, so naming WebDriverAgentRunner in the id is not required. A developer's own UI test runner counts too; the person can still remove it on the phone.
  - Apps the phone lists as system apps, and launchers and system UI ("System apps").
  - Blocked apps ("Blocked by you"), finance apps ("Finance apps") and password managers ("Password managers"), judged by the app id and name, the new app's for an install. An install over such an app keeps its data for the new build; an uninstall deletes it; stopping it can cut off a payment in progress. Unlike opening, a task's unblock does not lift this.
  - The person's own deny rules on an app apply as they do to every action.
- **The agent's scope and the task's app list apply**, to the new app's id and name for an install: an agent limited to some apps cannot add others. A task's unblock of an app does not lift any of this.
- **Read-only refuses all three**: read-only mode on the phone or for all phones, and read-only and watch-only agents.
- The phone must be free as for any action: not taken over, no other approval open, not locked (unless the task works on a background display).

## Android

- Install: the APKs go to `/data/local/tmp`, then `pm install -r -t --user current` (one APK) or `pm install-create`, `install-write` per split and `install-commit` (`install-abandon` when one fails) for split APKs; the temporary files are removed either way. `-r` replaces, `-t` takes test-only builds (Android Studio's Run makes them), no `-d` (no downgrades) and no `-g` (the app asks for its permissions as it would after a store install). `--user current` installs for the user the phone's screen and its background displays run as.
- Errors in plain words: `INSTALL_FAILED_UPDATE_INCOMPATIBLE` (signed with another key: uninstall first, which deletes its data), `INSTALL_FAILED_VERSION_DOWNGRADE`, `INSTALL_FAILED_USER_RESTRICTED` (Xiaomi's "Install via USB" is off, or the phone's install prompt was declined), `INSTALL_FAILED_DEPRECATED_SDK_VERSION`, `INSTALL_FAILED_NO_MATCHING_ABIS`, `INSTALL_FAILED_OLDER_SDK`, `INSTALL_PARSE_FAILED_NO_CERTIFICATES`, `INSTALL_FAILED_INSUFFICIENT_STORAGE`, `INSTALL_FAILED_MISSING_SPLIT`.
- Uninstall: `pm uninstall --user current <package>`, or `--user <n>` for an app clone (the clone alone goes). When no other user has the app, Android removes it entirely.
- Stop: `am force-stop --user current <package>` (`--user <n>` for a clone).
- The app list is read again after each change.
- Which version is installed comes from `pm list packages --show-versioncode` and `dumpsys package`'s `versionName`. Android 10's `pm list packages` lists nothing for `--user current` (its install and uninstall take it), so that one command reads the current user's number with `am get-current-user` first.

## iPhone

- Install: go-ios's streaming zip conduit, the service Xcode and `ios install` use (`com.apple.streaming_zip_conduit` through usbmuxd, over the cable or Wi-Fi; the installation proxy itself only lists and uninstalls). A `.app` folder is packed into a temporary `.ipa` first (`Payload/<name>.app/…`, stored, symbolic links refused), because go-ios sends folders with wrong paths.
- Uninstall: the installation proxy's `Uninstall`.
- Stop: CoreDevice through the tunnel: `listprocesses` finds the app's processes by its bundle path, `sendsignaltoprocess` sends them SIGKILL.
- The same three on the WebDriverAgent connection: installing and uninstalling go through usbmuxd as above (they need no tunnel), stopping through WebDriverAgent's `/wda/apps/terminate`, as XCTest stops an app.
- A WebDriverAgent connection to an iOS Simulator (as in Pinky's own WDA tests) has no usbmuxd entry, so installing and uninstalling return `unsupported` there; stopping works.
- Only development-signed builds whose profile lists the phone install; App Store and ad-hoc builds for other phones fail with the plain-words reason above. Simulator builds and iOS Simulators as phones are left to another change.

## In the app

- History steps and approval cards are written by the gateway in English. In Chinese and Japanese the app reads them back in `internal/gwtext/gwtext.go` (`stepText`, `needTitle`, `needDetail`) and shows them from the catalogs (`gwStepInstalledApp`, `gwDoInstallApp`, `gwAppSignedBy` and the like). The card's detail is translated part by part in a fixed order (signer, files, replaced version, or the data note for an uninstall), so the agent's own intent, the last part, is never taken for one of them. Words Pinky does not know stay in English.
- The four new rules ("Installing from the computer", "Uninstalling apps", "Pinky's own apps", "System apps") have names in the catalogs (`ruleInstallFromComputer` and so on) and are in telemetry's list of known rules, so counts name them instead of "other".

## Tests

- `internal/apppkg`: a tiny APK built locally with the SDK's `aapt2`, `d8`, `zipalign` and `apksigner` (testdata, a few KB, no downloaded APKs), split APKs, v1-only signing, a hand-made `.app` and `.ipa`.
- `internal/policy`: the new ops against scope, task apps, finance, passwords, blocked, read-only and "Every action".
- `internal/gateway`: install asks with the right card; Allow for this task covers the same id and signer only; Always allow covers the same id, signer, agent and phone only (`TestAlwaysAllowInstall`); uninstall asks and offers no Always allow; stop does not, except under "Every action"; Pinky for Android, system apps, finance, password managers and blocked apps refused; scope and task apps; read-only, watch-only; paths outside the folder and symbolic links refused; the history's words.
- Drivers: the Android commands and error mapping against a scripted adb; the iPhone profile checks.
- An emulator run, `TestEmulatorApp` in `internal/gateway/emulator_app_test.go` (`device` tag), builds the test app three times (1.0, 2.0 with its split, and 3.0 with another key) and goes through the gateway: install asks and Allow for this task is chosen; launch; the split update runs without asking; an older build and the other key are refused in plain words; stop without asking; Settings and Pinky for Android refused; uninstall asks. Passed on the Android 10 and Android 16 emulators (2026-11-09):

  ```sh
  go test -c -tags device -o /tmp/pinky-gw/gateway.test ./internal/gateway
  cd internal/gateway && ../../scripts/devlock.sh emulator-5560 env PINKY_EMULATOR=emulator-5560 PINKY_TEST_PIN=1234 /tmp/pinky-gw/gateway.test -test.run TestEmulatorApp -test.v
  ```
- Not yet on a real phone: an install on a real Android phone (Xiaomi's "Install via USB" in particular), and any install on an iPhone, which waits for the owner's OK.

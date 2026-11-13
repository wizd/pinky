# Connecting a phone: Android steps by brand, and checkups

Status as of 2026-11-08, branch `claude/onboarding-doctor-b2be1e`. The goal: someone who has never turned on USB debugging connects their phone alone, and when a step is stuck, Pinky says why and where to tap.

## What is built

- **Add phone › Android** (`ui/addandroid.go`, `ui/brands.go`). A row of brands to pick: Xiaomi · Redmi · POCO; OPPO · OnePlus · realme; vivo · iQOO; HONOR; HUAWEI; Samsung; Google Pixel; Other (stock Android). Four steps, drawn with the step list `ui/guide.go` shares with Add phone › iPhone:
  1. Turn on Developer options: the menu path as chips (no screenshots), the item to tap marked ×7, what differs on other versions.
  2. Turn on USB debugging: the path to Developer options and the switches to set, each drawn as a small switch on or off, with why (Xiaomi's USB debugging (Security settings) and Install via USB, ColorOS's Disable permission monitoring, Samsung's Auto Blocker off, HUAWEI and HONOR's ADB in charge-only mode).
  3. Plug in and allow: a drawing of Android's "Allow USB debugging?" prompt with the box ticked and Allow marked. The step says what it waits for: no phone yet (and what to try), the phone found but not allowed (and, after 12 s, what to do when the phone shows no prompt), a phone the computer may not open (Linux udev), a phone that does not answer.
  4. Check the phone: the phone's checkup, compact (items that passed show their name only), with Done first.
  Once the phone is online, its checkup reads the maker's system and the steps switch to that brand ("Pinky recognized HyperOS 2.0 on Redmi K80"). A Xiaomi phone on LineageOS switches to Other, whose steps are stock Android's. The card scrolls when the steps are taller than the window.
- **Checkup** (`ui/checkup.go`). On the phone's page in place of the mirror: from ⋯ › Checkup under the mirror, Settings › Phones › Checkup, Checkup on an offline phone's page, and See what's wrong on a phone that needs setup. It also opens by itself after pairing over Wi-Fi, as the last step of adding a phone that way. Each item has a mark (passed, failed, works with a limit, not checked), what Pinky found, and for an item that failed what to do in the phone maker's words and menu path, with a button where Pinky can fix it (Install, Turn on, Update Pinky for Android; Open Local Network settings). The driver's own words show under a failed item, and under every item in Developer mode. It runs again when the phone's connection changes, after an install, and with Check again.
- **Install errors in the maker's words** (`companionError`). INSTALL_FAILED_USER_RESTRICTED on a Xiaomi phone says to turn on Install via USB and to tap Install within 10 seconds; elsewhere it says to confirm the install or look for such a switch. INSTALL_FAILED_ABORTED says the install was declined on the phone. A refused WRITE_SECURE_SETTINGS (turning on the app's service) says to turn on Xiaomi's USB debugging (Security settings) or ColorOS's Disable permission monitoring. While Pinky installs on a Xiaomi phone, the hint says to tap Install within 10 seconds with the phone unlocked.

## The contract

Additive, at the end of `internal/driver/driver.go`:

- `driver.Check{ID, Status, Fix, Detail}`, `driver.CheckStatus` (`pass`, `fail`, `warn`, `skip`), and the names of items (`CheckADB`, `CheckConnection`, `CheckAuthorized`, `CheckCompanion`, `CheckInput`, `CheckScreen`, `CheckBackground`, `CheckWiFi`, `CheckLocalNetwork`) and fixes (`FixReconnect`, `FixAuthorize`, `FixInstallCompanion`, `FixTurnOnCompanion`, `FixUpdateCompanion`, `FixInputPermission`, `FixRestart`, `FixVideoEncoder`, `FixScreenshot`, `FixNewerAndroid`, `FixUnlockedOnly`, `FixLocalNetwork`, ...).
- `driver.Checkup{Family, System, Checks}`: Family is the maker's system for brand-specific steps (`xiaomi`, `oppo`, `vivo`, `honor`, `huawei`, `samsung`, `pixel`, or "" for stock Android), System the system and its version for people to read.
- `driver.Checker`, optional on a `Device`: `Checkup(ctx) (driver.Checkup, error)`. It must not change anything on the phone the person would notice.

`internal/gateway/checkup.go`: `Gateway.Checkup(ctx, deviceID) (CheckupView, error)` starts every phone with `ConnectionChecks(info)` (connected; allows this computer, with `Info.Detail`) and adds the driver's items once the phone is online. A phone that is not online gets its `Info.Setup` (from the iPhone branch's shared commit `df661f9`, cherry-picked here) as the fix: `trust`, `developer_mode`, `no_disk_image` and the other iPhone steps, and two of Android's, `driver.SetupUSBPermission` (Linux: no udev rule or not in plugdev) and `driver.SetupUSBDriver` (Windows: no driver for the phone's USB debugging), which the bundled-adb branch sets. Without a Setup, the client tells the two Android cases apart by the computer's system. The client finds it with a type assertion (`checkupRunner`), as the other extras; `ui/contract_test.go` pins its signature. The fake (`ui/fakeapi/checkup.go`) runs the same connection items and has `SetCheckup`, `SetInstallError`, `ConnectPhoneAs` and `Unplug`, and a scenario, `checkup`: a Redmi on HyperOS that refuses taps and installs. The demo's Android phones (`pinky --fake`) pass every item.

iPhones: the iOS driver adds its own items by implementing `driver.Checker`, with the ids agreed with the iPhone session: `trust`, `developer_mode`, `disk_image`, `helper` (WebDriverAgent on iOS 17.4 to 26), and the shared `screen` and `input`; at `no_disk_image` the client can offer the disk image's mirror download (`gateway.DiskImageMirror`, `DownloadDiskImage`, on the iPhone branch). The client shows items it has no words for by their driver's Detail, and `checkTitle` and `checkWords` take their words once they exist. Until then an iPhone's checkup shows the connection items.

## The Android checkup

`internal/driver/android/checkup.go`, about 2.5 to 4 s, the items in parallel:

| Item | How | Fails or warns when |
| --- | --- | --- |
| adb | `host:version` | the server does not answer |
| Pinky for Android | `CompanionState` | missing, off, older than the bundled one |
| Tapping and typing | `KEYCODE_UNKNOWN` through the scrcpy session that carries every tap (apps and the system ignore it), then 1.2 s watching the server's log | the server logs the `INJECT_EVENTS` SecurityException (Xiaomi without USB debugging (Security settings), ColorOS with permission monitoring); on a Xiaomi phone whose `persist.security.adbinput` is already 1 the fix is a restart, since the switch takes effect only then |
| Seeing the screen | `screencap`, and the session's video encoder | no screenshot; a video encoder that failed (warns: still pictures) |
| Background | Android version | Android 10 (skipped), 11 and 12 (warns: only while unlocked) |
| Wi-Fi | Android version, connection | Android 10 (skipped) |
| Local network (Mac) | 2.5 s of the same mDNS search pairing uses | macOS drops Pinky's traffic (not allowed, or the prompt is up) |

It opens no background display (on Android 17 that wakes the phone), installs nothing and changes no setting; the device test checks that the screen is on or off as before.

The maker's system comes from one `getprop`, in this order: `ro.lineage.build.version` (LineageOS keeps the maker's name), `ro.mi.os.version.name` (HyperOS, `OS2.0`; HyperOS keeps `ro.miui.ui.version.name=V816`, so it goes first), `ro.miui.ui.version.name` (MIUI, `V140`), `ro.build.version.oplusrom` / `opporom` / `realmeui` / `ro.oxygen.version` (ColorOS, realme UI, OxygenOS), `ro.vivo.os.build.display.id` (`OriginOS 5`, `Funtouch OS_14`; `ro.vivo.os.name` says Funtouch on both), `ro.build.version.magic` (MagicOS), `hw_sc.build.platform.version` / `ro.build.version.emui` (HarmonyOS, EMUI), `ro.build.version.oneui` (`60100` = One UI 6.1), then Google's Pixels (not emulators).

## Known errors and what Pinky says

| What the phone or adb says | Means | Pinky says |
| --- | --- | --- |
| scrcpy: `... to have the INJECT_EVENTS permission.` | Xiaomi: USB debugging (Security settings) off, or on but not restarted; ColorOS: permission monitoring on | Turn on the switch (Xiaomi: then restart; it checks with Xiaomi online and may want a SIM card or the Xiaomi account), or restart |
| `INSTALL_FAILED_USER_RESTRICTED: Install canceled by user` | Xiaomi: Install via USB off, or nobody tapped Install within 10 s | Turn on Install via USB, keep the phone unlocked, tap Install |
| `INSTALL_FAILED_ABORTED: User rejected permissions` | the install was declined on the phone (vivo asks) | Try again and confirm on the phone |
| `Permission denial ... WRITE_SECURE_SETTINGS` | turning on the app's service: Xiaomi's security switch off, ColorOS's monitoring on | The maker's switch |
| adb `unauthorized` | the phone has not allowed this computer (also after 7 days without a connection: Android revokes it) | Unlock and tap Allow; what to do without a prompt |
| adb `no permissions (missing udev rules? ...)` | Linux: no udev rules or not in plugdev | plugdev, udev rules, plug in again |
| Samsung: USB debugging greyed out, phone not in `adb devices` | Auto Blocker on (One UI 6.1.1 and later, on by default on new phones) | Turn Auto Blocker off first |
| Wireless debugging off after a network change | Android 11 to 16 turn it off when the phone leaves the network or its access point changes | Turn it on again or pair again |
| USB debugging turns itself off (HONOR, HUAWEI) | Allow ADB debugging in charge only mode is off | Turn that on first (the steps list it before USB debugging) |
| ColorOS: the companion's service turns off right after it is turned on | permission monitoring, restricted settings | Disable permission monitoring; Allow restricted settings |

## The brands' steps, and how sure each is

The names below were checked against the makers' own help pages and, for Xiaomi and AOSP, the system's string resources; where a name could not be confirmed, the steps say to go by the phone's own names (every brand's page says so under the brands).

- **Xiaomi, Redmi, POCO.** Global ROM: Settings › About phone › OS version (MIUI: MIUI version) ×7; Settings › Additional settings › Developer options. China ROM: 设置 › 我的设备 › 全部参数与信息 › OS版本 (MIUI: 全部参数 › MIUI版本); 设置 › 更多设置 › 开发者选项. Switches: USB 调试, USB调试（安全设置）, USB安装. Security settings: three warnings of 5 s each, an online check with Xiaomi (may want a SIM card or the account), a restart. Install via USB: needs the Xiaomi account; each install asks, and declines by itself after about 10 s. The path depends on the ROM's region, not the language: a Global ROM in Chinese may read 关于手机 (not confirmed). Don't turn off system optimization: it does not help.
- **vivo, iQOO.** OriginOS 4 to 6: 设置 › 关于手机 › 版本信息 › 软件版本号 ×7; 设置 › 系统管理与升级 › 开发者选项 (OriginOS 3: 系统管理 › 关于手机 ... and 系统管理 › 开发者选项). Overseas: Settings › About phone › Build number (some versions: Software version); Settings › System management › Developer options. Dialing `*#*#7777#*#*` opens Developer options. No switch beyond USB debugging on current phones (USB 模拟点击 was Funtouch 3 and older). The developer-mode notification turns developer mode off when tapped. Installs may ask on the phone.
- **Samsung.** Settings › About phone › Software information › Build number ×7; Developer options at the bottom of Settings (some models: under General). Auto Blocker: Settings › Security and privacy › Auto Blocker / 设置 › 安全与隐私 › 自动拦截程序 (Samsung China's site). Not confirmed: 编译编号 and 软件信息 in Chinese.
- **Google Pixel and stock Android.** Settings › About phone › Build number ×7 (Chinese: 关于手机 to Android 14, 关于本机 from 15; Build 号); Settings › System › Developer options. From AOSP's string resources (Android 14 to 17).

- **OPPO, OnePlus, realme.** ColorOS 13 and later: Settings › About device › Version › Build number ×7 (设置 › 关于本机 › 版本信息 › 版本号; some English versions say Version number). Developer options: Settings › System & update (ColorOS 15, realme UI 6; 系统与更新), Settings › Additional settings on ColorOS 13 (其他设置); ColorOS 14 and 16 not confirmed, so the steps also say to search Settings. Disable permission monitoring (禁止权限监控, at the bottom of Developer options, then a restart) lets adb write secure settings, which turning on the companion's service needs; that it also gates input was not confirmed (no reports), but it is the step the checkup gives when input is refused. On ColorOS and OxygenOS 16 it is hidden in Chinese and shows in English as Disable system optimization. China ColorOS shows a full-screen 权限提示 page for each install (继续安装); older China ROMs ask for the OPPO account. OxygenOS 13 and later have no property of their own: `ro.build.version.oplusrom`, with `ro.oplus.image.system_ext.area` (`domestic`, `export`, `gdpr`) telling China's ColorOS from OxygenOS.
- **HONOR.** Settings › About phone › Build number ×7; Settings › System & updates › Developer options (开发人员选项). HONOR turns USB debugging off after a while or when unplugged unless Allow ADB debugging in charge only mode (仅充电模式下允许ADB调试) is on first (HONOR's own support). Its prompt's check box reads 始终允许这台计算机进行调试, its button 确定 (OK). Owner only (not a second user or a private space). `ro.build.version.magic` (`MagicOS_9.0.0`, `MagicUI_6.1.0`; HONOR phones made by Huawei: `3.0.1` with EMUI's properties).
- **HUAWEI.** EMUI: Settings › About phone › Build number ×7; HarmonyOS 2 to 4: HarmonyOS 版本. Settings › System & updates › Developer options (开发人员选项). Huawei asks to turn on Allow ADB debugging in charge only mode (“仅充电”模式下允许 ADB 调试) with USB debugging; Monitor apps installed by ADB (监控ADB安装应用) makes each install wait for a confirmation (declined: INSTALL_FAILED_ABORTED); Pure mode's Enhanced protection (纯净模式 › 增强防护, HarmonyOS 3+) may block installs from outside AppGallery (whether it blocks adb: not confirmed). The prompt's check box reads 始终允许使用这台计算机进行调试 (its button: not confirmed; the steps say 确定). HarmonyOS 5 and later (NEXT) run no Android and have no adb: Pinky cannot use those phones, and adb never lists them. `hw_sc.build.platform.version` shows on EMUI 11 to 15 too, so the version in `ro.huawei.build.display.id` (`LIO-AL00 4.2.0.120(...)`) tells HarmonyOS 2–4 from EMUI.

## How to run

```sh
go run ./internal/driver/android/cmd/androidctl -s <serial> checkup [-json]   # a phone's checkup from a terminal
scripts/devlock.sh <serial> env PINKY_DEVICE=<serial> go test -tags device -run TestDeviceCheckup -v ./internal/driver/android/
go test ./ui -run 'TestCheckup|TestAddAndroid|TestCompanionErrors'
PINKY_SCREENSHOTS=dir go test ./ui -run TestCheckupScreenshots               # every brand's steps and checkups, English and Chinese
go test -tags e2e -run TestCheckupInTheApp ./cmd/pinky                        # the e2e app with the demo phones
scripts/devlock.sh <serial> env PINKY_E2E_ANDROID=<serial> go test -tags e2e -run TestCheckupOnPhoneInTheApp ./cmd/pinky
go run ./cmd/pinky-ui-demo -scenario checkup                                  # the Redmi that refuses taps
```

A Go test binary or an unsigned `androidctl` is usually not allowed on the local network on this Mac, so its local network item fails; the app and a re-signed binary pass.

## Checked

- `pinky-api36` (Android 16, emulator-5560): every item passes but the local network for the test binary; the checkup took 3.5 to 4 s and left the screen as it was.
- `pinky-api29` (Android 10, emulator-5574): the companion's service off, the video encoder failing (works with a limit), background and Wi-Fi skipped; the same through the real gateway and the e2e app in English and Chinese (`TestCheckupOnPhoneInTheApp`), with the emulator locked.
- The e2e app with the demo phones: ⋯ › Checkup, Back to the phone, and every brand's steps in Add phone, in English and Chinese.
- Not on a phone of a maker with switches of its own: the Xiaomi, ColorOS, vivo, Samsung, HONOR and HUAWEI paths are from the research above, and the errors are matched on the texts quoted there.

## Not done

- An Android phone with USB debugging off is invisible to adb, so before step 3 Pinky cannot tell the brand. Reading the USB vendor id of a plugged-in phone (IOKit, sysfs, SetupAPI) would let Add phone say "a Xiaomi phone is plugged in, but USB debugging is off".
- Whether adb itself works on the computer is the bundled adb's `driver.HostChecker` (branch `claude/bundle-adb-3b0832`): `adb`, `adb_server`, `adb_old`, `adb_restarts`. The checkup puts what a phone's provider lacks first (`driver.CheckHost`, the code as its fix), and Add phone › Android shows it above the steps, asking `HostChecks` every 2 s; both show nothing until that branch is merged. Pairing over Wi-Fi without a working adb says to go back for why.
- iPhone items: the iOS session's.
- Android 13+ restricted settings, and Android 17's Advanced Protection, can turn the companion's service off again; the checkup says how to allow it, but does not read `ACCESS_RESTRICTED_SETTINGS`. On ColorOS 15, `appops set <package> ACCESS_RESTRICTED_SETTINGS allow` after turning the service on is reported to keep it on: the companion's install could do that (companion session).
- The exact output of an install that HUAWEI's Monitor apps installed by ADB or Pure mode stops, and of China ColorOS's 取消安装, was not seen: those errors fall to the generic messages.

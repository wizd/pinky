# On-phone tasks on a separate screen (feasibility)

The question (2026-11-13): when Pinky on an Android phone runs a task by itself, with no computer, can the task's apps run on a separate virtual display that the person watches in a small floating window, while their own screen stays theirs? Other products do this (Mika, ClosePaw, below), and Pinky on a computer already does it through the scrcpy server running as the shell user ([DESIGN.md](../DESIGN.md), "Background mode").

**Answer: not for an ordinary app.** Pinky's app can create a virtual display and draw it in a floating window, but Android does not let an app without system privileges start other apps' activities there, and does not let its accessibility service read or touch a private display. Every product that does it gets the shell user's privileges first: through wireless debugging paired with the phone itself, through Shizuku (the same thing, in another app), or through root. The closest workable route for Pinky is the same: [below](#the-closest-workable-route).

Nothing was built beyond the probe. The task brief said to stop at this point when the answer is no.

## What Android allows an ordinary app

From AOSP (`ActivityTaskSupervisor.isCallerAllowedToLaunchOnDisplay`, [source](https://android.googlesource.com/platform/frameworks/base/+/refs/heads/main/services/core/java/com/android/server/wm/ActivityTaskSupervisor.java)), a start with `ActivityOptions.setLaunchDisplayId` is allowed when:

1. the caller holds `INTERNAL_SYSTEM_WINDOW` (signature): any display; else
2. on a display that is not trusted (`Display.FLAG_TRUSTED` off), the activity must declare `allowEmbedded`, **and** the caller must hold `ACTIVITY_EMBEDDING` (signature|privileged) or already have a window on that display; then
3. a public display allows it when the user is visible there; a private one only for its owner or a uid already on it.

A virtual display is trusted only when created with `VIRTUAL_DISPLAY_FLAG_TRUSTED`, which needs `ADD_TRUSTED_DISPLAY` (signature). So every display an ordinary app can create is untrusted, step 2 applies, and almost no app's activities declare `allowEmbedded`. The owner rule in step 3 never comes into play: even the owner's own `allowEmbedded` activity is refused (T3 below), since the owner has no window there yet and no `ACTIVITY_EMBEDDING`. Asking for a public display, with or without MediaProjection, does not change this: it is still untrusted (T5, T7).

Accessibility services leave private displays out entirely: `getWindowsOnAllDisplays` does not list them, `takeScreenshot(displayId)` fails with `ERROR_TAKE_SCREENSHOT_INVALID_DISPLAY`, and gestures there are cancelled (T8). Public displays (from MediaProjection) are read and touched like the main one, but nothing can be started on them.

## The probe

`docs/notes/virtual-display-probe/` is a small app, `app.pinky.vdprobe`: an activity that runs each test, an `allowEmbedded` activity of its own, an accessibility service that lists each display's windows, takes a screenshot of a display and dispatches a gesture on it, and a foreground service for MediaProjection. To build it, copy `gradlew`, `gradle/`, `build.gradle.kts` and `gradle.properties` from `android/companion` next to its `settings.gradle.kts`; results go to logcat under the tag `VDPROBE`.

Run on the `pinky-api36` emulator (Android 16, API 36, Google APIs, arm64), 2026-11-13:

| # | Test | Result |
| --- | --- | --- |
| T1 | `createVirtualDisplay` with `OWN_CONTENT_ONLY`, no projection | Created: flags `FLAG_PRIVATE`, not trusted; `dumpsys display` shows `canHostTasks false` |
| T5 | The same with `VIRTUAL_DISPLAY_FLAG_PUBLIC`, no projection | Created, public (no `FLAG_PRIVATE`) and not trusted; nothing was started on it, as step 2 above refuses it like T7 |
| T6 | The same with `VIRTUAL_DISPLAY_FLAG_TRUSTED` | `SecurityException: Requires ADD_TRUSTED_DISPLAY permission` |
| T2 | Settings onto the private display (T1) | `SecurityException: Permission Denial … with launchDisplayId=13` |
| T3 | The probe's own `allowEmbedded` activity onto it | `SecurityException: Permission Denial` |
| T7 | After the person allows Share entire screen: a public display from `MediaProjection.createVirtualDisplay`, then Settings onto it | Display created (`FLAG_PUBLIC`, not trusted); Settings and the probe's own `allowEmbedded` activity both `Permission Denial` |
| T8 | Accessibility on the private display | Not in `getWindowsOnAllDisplays`; `takeScreenshot` fails (code 4, invalid display); a gesture is cancelled |
| T8b | Accessibility on the public projection display | Listed; `takeScreenshot` 720×1280; a gesture completes |
| T9 | Settings on display 0 with an explicit launch display | Starts (the probe's code path is right) |
| C1 | Control: `adb shell am start --display 13 -a android.settings.SETTINGS` | Settings resumes on the probe's private display; the probe reads it in full from its own `ImageReader` (every sample non-black). The shell user holds `INTERNAL_SYSTEM_WINDOW`, `ACTIVITY_EMBEDDING` and `ADD_TRUSTED_DISPLAY` |

The floating window itself is not the problem: the app owns the display's `Surface` and can point it at a `TextureView` in a `TYPE_APPLICATION_OVERLAY` window (`VirtualDisplay.setSurface`; the probe has an `overlay` test for it, not run, as nothing could be started on the display). C1 shows that this half works once something privileged has put the app there.

Not run: Android 14 and 15, Android 17 (the only API 37 emulator was another session's), and real phones. They are expected to refuse the same way, as the rule above is AOSP's (its history per version was not checked); vendors (HyperOS, ColorOS, One UI) add restrictions to secondary displays, not exceptions for ordinary apps, but no vendor phone was tried.

### Other routes, ruled out

- **Starting from an activity already on the display, a PendingIntent or the accessibility service**: the same check runs on the uid that starts the activity, and Pinky's app never gets a window on the display in the first place (T3).
- **`ActivityView` / `TaskView`**: hidden, system-only.
- **Virtual Device Manager (`VirtualDeviceManager.createVirtualDevice`, Android 14+)**: needs `CREATE_VIRTUAL_DEVICE` (signature|privileged, or a companion device role the system grants only to some apps).
- **Android's Computer Control** (Android 17): exactly this feature, a trusted background display with input and a mirrored view the person can take over, but it needs `ACCESS_COMPUTER_CONTROL`, which "OEM-preloaded" assistants with an allowlisted certificate get ([Android developers](https://developer.android.com/ai/computer-control), [Android Authority](https://www.androidauthority.com/android-computer-control-feature-3603862/)). Worth watching should Google open it to apps from the store.

## How others do it

| Product | Route | Source |
| --- | --- | --- |
| Mika (Apache-2.0) | Wireless debugging paired with the phone itself once ("pair wireless debugging on the phone once"), which starts a "phone helper"; or started over USB from a computer. Android 12+, 14+ recommended; turns wireless debugging back on "with your permission". A small live view to watch | [README](https://github.com/wangzan101/Mika) |
| ClosePaw | Shizuku: "the agent operates a parallel Android display so the foreground stays yours"; accessibility for the main screen | [README](https://github.com/imoonkey/closepaw) |
| DroidOS | Shizuku, hidden APIs by reflection, to start apps on display ids | [README](https://github.com/aloim/droidos) |
| Aether, OpenCyvis | Root or Shizuku (from summaries, not their code) | [search results](https://pyshine.com/Aether-AI-Agent-for-Android-Device-Automation/) |

None runs other apps on a separate display as an ordinary app. Mika's service runs next to Pinky's on the owner's Mi 10 Pro ([ACCEPTANCE.md](../ACCEPTANCE.md)); it is likely the product the owner saw.

## The closest workable route

Do what Pinky on a computer does, from the phone: get a shell-user process once, through wireless debugging paired with the phone itself, and run the scrcpy server's background display from it (`new_display`, start-app, video into the floating window), with the accessibility service reading and acting on that display as it does for a computer today (the display is public and trusted, so T8b applies, not T8).

What it would take, roughly in order of size:

- **An adb client on the phone that pairs and connects to the phone's own adbd**: TLS with a key pair the app keeps, and the SPAKE2 pairing code exchange (Android 11+). Pinky's Go adb package speaks only the adb server protocol and leaves pairing to the adb server on a computer, so this is new code (Shizuku and Mika both carry one).
- **Setup the person does once**: Developer options on, Wireless debugging on, the pairing code typed into Pinky (from the notification, as Shizuku does). Wireless debugging needs Wi-Fi and turns off on reboot and on some network changes; a helper already started survives until reboot. Granting the app `WRITE_SECURE_SETTINGS` over that connection would let it turn wireless debugging back on itself (`adb_wifi_enabled`), which is presumably how Mika does it (not verified).
- **The helper**: the scrcpy server Pinky already ships for computers, started with `app_process` as shell from the app's own files, its video going to a `Surface` the app owns.
- **The floating window and the pill** after [phone-apps-ux.md](phone-apps-ux.md), with takeover moving the task to the main display.
- **Known limits that carry over from desktop background mode**: Android 13+ only; Android 17 stops a background display with the screen off, so the phone stays awake with its screen dark; Android 10 cannot read it; Xiaomi's HyperOS drops injected input on background displays, which is why gestures go through the service (GitHub issue #48); some apps refuse secondary displays and fall back to the main one.
- **Safety**: a shell-user process can do far more than the accessibility service (install apps, read logs, grant permissions). Pinky would hold it on the person's phone at all times, not only while a computer is plugged in, which needs a decision of its own.

Until then, on-phone tasks keep running on the main screen through the accessibility service.

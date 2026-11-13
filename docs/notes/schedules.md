# Scheduled tasks

The person gives Pinky's own agent (Ask Pinky) a task to run at set times: once, every day, on weekdays, on some days of the week, or every few hours. A task may carry a condition ("tell me only if it will rain"): it runs every time, but the person hears of it only when the condition holds. This note records how it works on the computer and on Android, what happens when nobody watches, and why the iPhone has no schedules of its own.

## Pieces

| Piece | Where | What it does |
| --- | --- | --- |
| Schedules and their store | `internal/schedule` (`schedule.go`, `store.go`) | The `Schedule` and `Run` types, validation, the next times (`Next`, `Upcoming` for the preview of the next three), the last 10 runs (`MaxRuns`), and whether a run is told (`notify`). Kept in `schedules.json` in Pinky's data folder. |
| Scheduler | `internal/schedule/scheduler.go` | `Tick(now, Host)` runs or skips what is due, one task at a time; `RunNow`; `NextTick` for the host's timer or alarm. |
| Agent | `internal/pilot/scheduled.go` | `StartScheduled` with `Scheduled{Unattended, Condition, Zone, Last, LastMet}`: the system prompt's rules for a run nobody watches, finish's `condition_met`, and the last result and the last result whose condition held, for the agent to compare with. |
| Desktop | `ui/schedules.go`, `cmd/pinky/app.go` | The Schedules screen of Ask Pinky, the host (`schedHost`) and its notifications. |
| Android core | `internal/standalone/schedules.go` | The control commands (`schedules`, `schedule_save`, `schedule_preview`, `schedule_delete`, `schedule_pause`, `schedule_run`, `schedule_due`, `notice_seen`) and the host on the phone, with `State.Notices`. |
| Android app | `android/companion/.../Schedules.kt`, `ScheduleNotices.kt`, `SchedulesActivity.kt`, `ScheduleEditActivity.kt` | The alarm, the notifications, and the two screens. |

### What a schedule holds

- **Repeat**: `once` (a date and a time), `daily`, `weekdays`, `weekly` (days), each at up to 12 times a day, or `interval` (every 1, 2, 3, 4, 6, 8 or 12 hours from a first time to the end of each day).
- **Zone**: the IANA time zone the times are in. Desktop Pinky saves the computer's; the phone names its own in every command, because Go on Android knows no local zone. A schedule keeps its zone when the person travels; a new one takes the current zone.
- **Condition** and **cooldown**: the agent says in finish whether the condition holds (`Run.Met`). The person is told only when it holds, and at most once per cooldown (none, an hour, a day).
- **Paused**: kept, but never due. Run now still works.
- **Runs**: the last 10, newest first, each with how it ended, its summary, steps and cost. `LastMet` keeps the last run whose condition held even after it drops out of the 10.

Saving a schedule moves its cursor to now, so that an edit never runs a time that was due before it.

## Limits

A scheduled task is a task like any other: it runs with the person's settings for steps, minutes and cost (`pilot.Config`), and ends at the first it reaches. One task at a time per phone: a time due while the phone runs another waits (below).

## When nobody watches

A run started by a schedule is **unattended**; Run now is not (the person pressed it and sees it). For an unattended run:

- **Payments are never made.** The prompt forbids paying, buying, ordering, subscribing, tipping, donating or moving money, and the agent finishes saying it skipped that step. Behind the prompt, the host denies at once any approval whose rule is about money (`schedule.IsPayment`: "payment", "pay", "purchase", "money", "transfer" and their Chinese and Japanese words), so the step never runs even if the agent tries.
- **Other approvals** reach the person as a notification, as for any task (desktop: Pinky's approval notification; Android: a high-priority notification while the task is unattended, which opens the task screen). They wait for the gateway's deadline (60 s from when the card opens); no answer is a denial, and the agent finishes saying which step it skipped.
- **No handoff.** `phone_handoff` is left out of the agent's tools. When the phone needs the person (a sign-in, a code, a choice only they can make), the agent finishes with failed and says what it needed.
- **The phone must be free.** Before a time runs, the host checks the phone (`Host.Check`):

  | Reason | Desktop | Android | What happens |
  | --- | --- | --- | --- |
  | `locked` | The phone reports locked | Screen off or keyguard showing, unless the background screen is set up ([below](#only-while-the-phone-is-free)) | Skipped at once. Pinky never unlocks a phone, turns its screen on, or types a PIN for a schedule. |
  | `in_use` | The person took over | Screen on, unlocked, touched less than 60 s ago | Waits up to 15 minutes, looking again every minute, then skipped. |
  | `busy` | Another task or agent has the phone | Another task runs | Waits up to 15 minutes, then skipped. |
  | `offline` | Not connected, paused, connecting or waiting for setup | (never) | Skipped at once. |
  | `no_key` | No model or key | No key for the model service | Skipped at once. |
  | `no_phone` | The phone is no longer Pinky's | (never) | Skipped at once. |
  | `off` | Ask Pinky turned off | On-phone mode turned off | Skipped at once. |

Every skip is recorded as a run and told to the person, with its reason.

## Steer and Continue

A scheduled task is a task of Pinky's own agent like any other, so the person can steer it and continue it ([builtin-agent.md](builtin-agent.md) has steering and Continue):

- **Steer during a run** is fine, also during one nobody was meant to watch: the message goes to the model as from the person, and the run stays unattended (payments are still denied, there is still no handoff) until it ends. It ends, is recorded and is told as the schedule's run.
- **Continue after a run** is the person's own doing, so what follows is theirs: the schedule recorded the run that ended, and the continued task is no longer the schedule's run. `Runner.Continue` marks it attended (`run.attendLocked`): `Status.Scheduled` stays set (a schedule started it) with `Unattended` and `Condition` cleared and `Met` dropped. When the run was unattended, the continue message tells the model that the person watches now, that payments and other risky steps go to them for approval as in any task, and that the condition was answered. The host no longer watches it: it does not deny payments, and nothing is told by notification.
- The model's tools stay as they were for the conversation and its prompt cache (`claude.go`), so a continued unattended run still has no `phone_handoff`: the model finishes saying what the phone needs, and the person continues again once they did it. Giving the tool back would need the models to change their tools mid-conversation; a follow-up if people need it.
- Continue is never automatic: a schedule's next time starts a new task, which ends the conversation Continue would carry on (one task a phone at a time).

## What the person hears

`Store.Finish` decides (`Run.Notify`): every unattended run is told, except one the person stopped, one whose condition did not hold, and one whose condition held again within the cooldown. Run now is never told (they watch it). Every run, told or not, stays in the schedule's last runs.

- **Desktop**: a system notification titled with the task, saying how it went ("Done", "Condition met", "Skipped: the phone was locked") and the summary. Clicking it opens the schedule. While Pinky's window is in front, a toast instead.
- **Android**: a notification on the "Scheduled tasks" channel, which opens the schedule. The core queues it in `State.Notices` until the app says it posted it (`notice_seen`), so a notice survives the app's screen being closed.

## Desktop

Desktop Pinky runs schedules **while it runs**, with its window closed too. The scheduler wakes at the next time due (`NextTick`), and at least every minute while a time waits for the phone.

When Pinky is not running, or the computer sleeps, nothing runs. When it starts or wakes:

- a time missed by up to 30 minutes (`DefaultGrace`) runs late;
- older times are not run: the latest of them is recorded and told as skipped (`not_running`), with the number of times missed, so the person knows rather than finding a run at an odd hour.

## Android

The core keeps the schedules and decides what runs, but a Go timer does not survive Doze. So the app sets an alarm for the next time due, which the core names in each reply (`next_ms`). When the alarm fires, `ScheduleReceiver` sends `schedule_due` with the model's settings and key, the phone's zone, and what only the app knows: whether the phone is locked, in use, or on-phone mode is off. The core runs or skips what is due and answers with the next time.

### Alarms, not WorkManager

- **Exact alarm** (`setExactAndAllowWhileIdle`) when Android lets Pinky set them: the `SCHEDULE_EXACT_ALARM` permission, "Alarms & reminders" in Android's settings. Android 14 and later leave it off for newly installed apps; the Schedules screen has a row that opens the page where the person allows it.
- **Inexact alarm** (`setAndAllowWhileIdle`) otherwise. Doze may hold it back for some minutes; it still runs.
- **Not WorkManager**: its periodic work runs every 15 minutes at best and at no set time, which does not fit "every day at 8".
- Android clears alarms when the phone restarts. `ScheduleReceiver` sets the alarm again on `BOOT_COMPLETED`, `MY_PACKAGE_REPLACED`, `TIME_SET`, `TIMEZONE_CHANGED` and when exact alarms are allowed, and the service sets it again once the core is up (`ScheduleNotices.Watcher`), because after a restart the core starts only with the accessibility service.

### What the person allows

The Schedules screen shows a row for each of these while it is not allowed, and hides the section once all are. Pinky never changes a setting itself; each row opens Android's own page.

- **Notifications** (Android 13 and later, `POST_NOTIFICATIONS`): asked once, when the list first has a schedule. After that Android asks no more, and the row opens the app's notification settings. Without it, runs still happen and their results are in the schedule's last runs.
- **Alarms & reminders**: exact times, as above.
- **Battery: Unrestricted**: the same request as Settings › Phone uses (`Battery.askUnrestricted`), so that the alarm wakes Pinky while the phone is idle. Some vendors also stop apps that are not allowed to start on their own (autostart); the screen does not cover that yet, and the person allows it in their phone's settings.

### Only while the phone is free

Android cannot drive a locked phone's own screen, and Pinky does not unlock one or turn its screen on. So without the background screen a scheduled task runs only while the screen is on, unlocked and untouched for a minute; a time due while the phone is in the person's pocket is skipped and told (`locked`). That suits a phone left unlocked on a stand or charger (for example with "Stay awake" while charging, which the person sets themselves).

**The background screen** (Settings › Background screen, [android-wadb.md](android-wadb.md)) lifts the lock: tasks run on a virtual display, which Android 13 and later keep unlocked whatever the phone's own lock. While it is set up (`Core.backgroundReady`: turned on, paired, Wireless debugging's port there, and the gateway puts tasks on a background display), a locked phone does not skip the time. Should the display not open, the task moves to the phone's own screen, finds it locked and ends failed, saying so: told like any run. "In use" still waits, background screen or not: a task that moves to the phone's own screen would land under the person's fingers. After a restart Wireless debugging is off until the person turns it on, and the lock skips times again.

Not yet tried on a real phone: a scheduled task on a locked phone's background screen, in Doze, over Wireless debugging. Android 17 stops a background display while the screen is off ([android-virtual-display.md](android-virtual-display.md)); whether the phone's own core keeps the phone awake and dark for it, as desktop Pinky does, is to check.

The Schedules screen says all this in its note.

"In use" is the accessibility service's view: a tap, scroll, typing or a new window in the last 60 s (`ScheduleRules.touches`). Notifications and content that changes by itself do not count.

### When the core is not there

The core runs in the accessibility service's process once on-phone mode is on. If the alarm fires while the service is off (turned off in Android's settings, or killed), nothing can run: the app posts "A scheduled task couldn't run" (open Pinky to start it again) and the schedules wait for the service. A time missed that way is reported as `not_running` once the core is back, as on the desktop.

## iPhone

Pinky on the iPhone does not run schedules of its own. Desktop Pinky can run a schedule on an iPhone it drives, as on any phone. On the phone alone it cannot work:

- **No set times.** iOS gives apps no way to run at a chosen time in the background. `BGTaskScheduler` runs app refresh and processing tasks when the system decides (often hours late, never on a locked phone for processing that needs the network, and less for apps the person rarely opens). Local notifications fire at set times but run no code.
- **The phone is locked nearly every time.** The iPhone locks itself a short while after the person puts it down, and a locked phone shows only its lock screen ([iphone-app.md](iphone-app.md)); Pinky never unlocks it. A schedule would find it locked almost always, more so than on Android, where a phone on a stand may stay unlocked.
- **Nothing to wake the core.** The core lives in the packet tunnel extension while the VPN is on (the lite build: in the app, kept alive by audio only during a task), and iOS gives neither a timer that wakes it at a set time. The extension's 50 MB is not the obstacle: a whole task peaks at about 28 MB there.

A scheduled task on the iPhone alone would be skipped almost every time, so Pinky offers none there rather than promise times it cannot keep. If iOS one day lets an app run at set times, the core's `schedule_due` command is ready for it.

## Verified

- `internal/schedule`: next times across repeats, zones and daylight saving; validation; the scheduler's grace, waits and skips; notify with conditions and cooldowns; the last runs.
- `internal/pilot`: the unattended prompt, no `phone_handoff`, `condition_met`, and Continue of an unattended run (`TestScheduledContinue`).
- `ui`: the desktop host with a fake model: a run, a skipped time, payments denied.
- `internal/standalone`: the phone's commands with a fake model: a run with its notice, locked, in use, off, a payment denied, run now (not told), pause, delete; when the background screen counts as ready (`TestScheduleBackgroundReady`, without a phone that has background displays).
- Android: unit tests of the rules (`ScheduleRulesTest`) and a debug build. Not yet tried on a real phone with a real model.

# Reading a list in one call

`phone_read_list` turns the common "read these screens and summarise them" task into one agent call. The agent names the list, how far to read, and its time budget. The gateway scrolls inside that list, collects the text exposed after each screen settles, removes the contiguous overlap with the previous screen, and returns compact page-labelled text. The outer agent summarises that text; Pinky does not embed a model.

## Pipeline

The read is a loop around the action pipeline (`internal/gateway/read_list.go`); it adds no input path of its own.

1. Check the lease as an action does (`gate`: Stop, pause, takeover, an open approval, a locked phone, watch-only), take the action lock, wait out any settle the last action owes (`settleOwed`) and read the screen. If no `ref` was passed, choose the largest list or scrollable element that covers at least a quarter of the screen. Failing that, take a column of rows (`rowColumn`): at least six elements from the tree (not OCR, fields, images or tabs), each at least half the screen wide and at most an eighth of it high, sharing their left edge and width and lying one below another, together covering a quarter of the screen. The read then scrolls the box around them as a `scroll_area`. iOS 27 lists Settings' rows but no list or scrollable element around them (GitHub issue #148); a grid of icons, chat bubbles or a short form is never such a column. Failing that too, and outside the system's own apps (the home screen, Control Center), take the screen's content (`contentArea`): the full width less an eighth at the top and bottom, the keyboard and system bars, when at least six rows of text, links or OCR text spread over half of it. WebDriverAgent drops the containers no one can reach (a web view, a table, a scroll view), and a web page's lines differ in width, so a bank's statement page in a web view had neither a list nor a column of rows (field test, iPhone 13 mini, 2026-11-22). The message says which area was scrolled, so the agent can pass `ref` or `scroll_area` for another part. A grid of buttons or a short form is still `not_found`.
2. Collect labelled elements whose centres are inside the list, ordered by y and then, within a row, by x (`byRows`: elements whose centres are within a quarter of the shorter one's height share a row, so a cell, its value and a badge read in the same order on every screen however the scroll offset rounds to pixels), from the agent's view of the screen (`agentView`: blocked apps hidden, one-time codes masked).
3. Each screen is read once. With `ocr: "auto"` the first screen is read with the text OCR finds and the tree misses, as the pipeline reads the screen after each swipe, and those lines are dropped again where the tree says enough about the list; reading the tree first and again with OCR cost two reads, 20–40 s each on an iPhone through WebDriverAgent over Wi-Fi. `ocr: "all"` always asks for pixel-only text; `ocr: "off"` keeps the tree-only path. The screen after each swipe is the action pipeline's read, which adds the text OCR finds and no element shows (on an iPhone, on every read); with `ocr: "auto"` those lines are dropped again where the tree says enough (`autoOCR`), so every screen is read like the first. Before, an iPhone's second screen of Settings had General's badge from OCR that the first lacked, and the read stopped at a gap (GitHub issue #148).
4. Each swipe is a `phone_swipe` action (`act`): `budgetBefore` and `lockAct`, `settleOwed`, `prepare` on the latest settled screen with the start inside `swipeArea` (so never on the keyboard, issue #71), `ocrForVerdict` and `evaluate`, the input, `settleAction`, the read after it and `recordStep`. Every swipe starts from the read before it (`Call.reuse`), as `phone_batch` steps do: the first from the read of the first screen, unless a settle is still owed, and later ones from the read that ended the swipe before. Before each swipe the read tries a few lanes across the list on the latest screen (`listLane`) and sends the first the rules let through; the pipeline judges it again on its own read.
5. The read never opens an approval (`Call.noAsk`): a swipe the rules would ask about returns `blocked_by_policy` with the rule, and under the "Approve every action" preset it says so in its own words. The agent swipes with `phone_swipe`, which asks, and reads on.
6. Drag a configurable 30–80% of the visible list height for one second. The deliberate drag avoids the extra inertia of a short fling, which can otherwise skip more than a viewport.
7. Compare adjacent viewports by contiguous line runs, not a global text set. Reading towards the end, lines above the shared run that the last screen did not have are a gap (`unseenAbove`): content may have been added there or skipped. When the run is three lines or more, these do not count: OCR lines, fragments of lines already seen (a row cut off by the bar above the list), lines the last screen showed outside the list (a navigation bar's title) and lines within one row of the list's top edge. Rows that were not on the last screen further down still are a gap. A read that stops at a gap says in its message how many lines the screens shared and which lines above them were new, or that they shared no run at all. This removes ordinary scroll overlap while keeping legitimate duplicate titles at different positions. Fixed controls are tracked separately so a changing nested-list boundary does not make the same toolbar appear as new text.
8. If continuous video or decoration keeps the pixels moving, compare two element viewports and accept the result when their text and order are stable. The settle stays owed, so the next action waits for it. A check is read only while the call has time for a read as slow as its slowest; otherwise the read stops at `deadline` with what it has.
9. Stop at an end marker, two unchanged screens, the per-call screen limit, the time limit, output limit, a gap with no overlap, lock, blocked app, app/list change, or an unstable screen, returning what was read with the status that stopped it. Stop, a pause or a takeover end the call with nothing more, as they end `phone_batch`. An end marker counts only on the list's last row when reading downwards in the list: "No more results" in the middle of a screen, or at the bottom while reading back up, ends nothing. At an end, an accessibility tree may regroup or reorder an overlapping row; Pinky subtracts equal text by multiplicity and preserves extra occurrences before declaring the list complete.

`max_ms` and the call's 45 s are soft: the read starts no swipe that may not finish in time, and never cancels one already sent. What a swipe needs follows how fast the phone reads (`swipeNeeds`): as long as its longest swipe so far, with settling and the read after it, and at least the slowest read of the call for each read it takes (the read after it; the screen it starts from, when it cannot start from the last read; that screen again with OCR, when `ocrForVerdict` must look under the finger), and at least 5 s for the call budget.

On a phone whose reads take a tenth of the call or more (`slowRead`), the read adapts:

- When there is time for the swipe but not for the read after it, the swipe still goes out (`Call.unread`: no settle wait, no read; the settle stays owed) and the call stops at `deadline` with a continuation that reads that screen first. Else a continuation whose first read takes most of the call would never get further: in the field test it came back `uncertain` with 0 screens.
- A read the call's time cuts short, before or after a swipe, ends at `deadline` with what was read and a continuation, never `internal` or `uncertain` (`Result.outOfTime`). The continuation tells by the overlap whether the swipe skipped anything.
- A continuation whose first read outlasts the call reads nothing, stops at `deadline` and hands back a fresh continuation for the same place.
- The message says how long a read took, and when a continuation made no progress, suggests `phone_swipe` and `phone_observe` instead.

When the screen cannot be read after a swipe for another reason, the result is `uncertain` and has no continuation.

A continuation reads the screen the last call ended on again. Its lines all stay at their places, so the fixed-control filter keeps none of them, and the read keeps the cursor's lines as the screen to find the next screen's overlap with; before, it forgot them and took the whole next screen as new.

Every swipe is a step in the task's history, with the screen it settled on (text from a blocked app the person allowed for the task left out, as for any step). The call itself adds no step. A partial result carries a continuation: an opaque id (`rl_` and 16 random characters) for a cursor kept on the lease, so it goes with the task and phone, holds for 15 minutes and is used once. The cursor holds the app, the list's place and tree path (which agents never see), the scroll area, the direction and the last 80 lines and fixed controls; it is refused in another app or for another list, area or direction. `max_screens` is a fresh budget on each resumed call. It is not a replay of the task recording: reads use only the element tree, local OCR from policy-checked live frames, and the existing screen-capture path. A task MP4 is never used as input.

## Platform support

- Android through scrcpy works on the phone's display. On Android 11+ background work, the list has no `screencap`; OCR uses the existing live-video frame path.
- iPhone and iPad on iOS 27+ through CoreDevice use the same gateway path.
- iOS 17.4–26 needs WebDriverAgent for swipes. Without that helper, the phone can be watched and apps opened, but this tool returns `unsupported` at the swipe.
- Linux without a local OCR engine can still use the tree-only path. Pixel-only content is not guessed; use `ocr: "all"` on macOS or Windows, where the local recognizer is available.
- A page that does not expose a list or scrollable element can be read with an explicit `scroll_area` rectangle in screen pixels. Without an element, an area, a column of like rows or a screen of text (above), the tool reports `not_found`. It never scrolls the content of a system app such as the home screen, where a swipe changes pages rather than reading on.

## Safety

- Every swipe is an action through the pipeline, so the lease, lock, takeover, open approval, watch-only, read-only, app-scope and policy checks apply to it as to `phone_swipe`, before and after it. Reading the first screen is allowed wherever `phone_observe` is.
- A blocked app contributes no text unless the person allowed it for this task. When allowed, text may be read while screenshots and recordings remain withheld, and its text stays out of stored task history.
- One-time codes are masked before collection.
- A swipe that would start on a protected control stops the read instead of asking. Where the tree shows nothing under the start, the pipeline checks OCR (`ocrForVerdict`), so a pixel-only control cannot be mistaken for blank space.
- `max_ms` is at most 45 seconds. A partial result is explicit and must not be described as a complete list unless `complete: true`.

Tests: `read_list_test.go` (merging, fixed controls, end markers, gaps, continuation, deadlines, takeover, a web page without a list, the home screen left alone, `contentArea`), `read_list_settings_test.go` (iOS Settings' home page as the WebDriverAgent path reads it, screen after screen, with and without OCR, and a swipe that skips rows) and `read_list_pipeline_test.go` (Stop and pause mid-read, an open approval, watch-only, read-only, an agent limited to other apps, "Approve every action", continuations refused in another task, phone or app and used only once, end markers that do not count, the settle a swipe owes, the call budget, an unreadable screen after a swipe, each screen read once, and on a slow phone: the deadline with a continuation, continuations that read on, the swipe that goes out unread, a first read that outlasts the call). None needs a phone.

## Simulator measurement

Measured on 2026-11-10, macOS, an iPhone 16 Pro Simulator running iOS 18.6 with WebDriverAgent 16.15.3. Each run reset the app and started at the top of the logged-in `tech.ycyx.dev.yinchao` Explore feed:

| Path | Work | Pinky time |
| --- | --- | --- |
| Existing `phone_swipe` loop | 20 requested screens; 19 full action pipelines | 1:47.306 |
| `phone_read_list` gateway path | 3 screens, 2 swipes, 46 new text lines; stopped at the explicit end | 16.222 s |

The existing loop averaged 5.648 s per action. `phone_read_list` used one tool call and its Pinky-side elapsed time was 6.61 times shorter. The list read also stopped as soon as the feed exposed its end instead of asking for 20 screens regardless. This comparison excludes the outer agent's thinking time: the old flow also has a model round trip between screens, while the new flow needs only the final summary.

The gateway comparison is in `internal/gateway/read_list_sim_test.go` behind the `wdasim` build tag. It is a manual smoke test: open Explore at the top and set `PINKY_TEST_EXPLORE_READY=1`; without that prerequisite it skips instead of treating stale app state as evidence. The baseline deliberately keeps reading 20 times even after the feed reaches its end, showing the work the dedicated scan avoids. No Android device was used for this measurement; Android support follows the shared gateway and driver path but still needs a device run.

## Fixed-content benchmark

`internal/gateway/read_list_fixture_sim_test.go` serves a fixed 100-item mobile page from a local test server and runs the same first seven screens through both paths, with a 30% and one-second swipe:

| Path | Work | Pinky time | Calls |
| --- | --- | ---: | ---: |
| Existing `phone_swipe` loop | 7 requested screens, 6 actions | 52.312 s | 6 |
| `phone_read_list` | 7 screens, 6 swipes | 38.745 s | 1 |

The fixed fixture was 1.35 times faster on Pinky's side and preserved the first five visible article titles once each. The same page through the public MCP tool (`internal/mcp/read_list_sim_test.go`) returned seven screens and 13 new lines in 41.098 s and preserved each of the first five titles once. This run is short but fully repeatable. The larger end-to-end gain on personal feeds comes from removing the model round trip after every screen, which these gateway timers deliberately exclude.

## Public mobile-web feeds

The same comparison was also run against two pages in Safari where the accessibility tree exposes the content but no platform list element. In those cases the test passes an explicit `scroll_area`; there is no site-specific code. Every old and new path used a fresh URL navigation, the same rectangle, the same viewport travel and the same one-second drag. This removes gesture speed as an explanation for the result:

| Site | Common coverage | Existing swipe loop | `phone_read_list` | Calls |
| --- | --- | ---: | ---: | ---: |
| Bilibili home feed | 4 screens, 3 swipes, 30% travel | 17.004 s | 12.226 s | 3 → 1 |
| 36Kr mobile article feed | 7 screens, 6 swipes, 60% travel | 44.252 s | 29.479 s | 6 → 1 |

The Pinky-side speedups were 1.39 times on the short Bilibili range and 1.50 times on the longer 36Kr range. The old times count the full per-action read, policy, swipe, settle, post-read and diff pipeline. A longer 36Kr run stopped safely at a content gap after seven screens; the seven-screen result above is the final contiguous range rather than a claim about every article below it. Times exclude the outer model's thinking time and the initial screen read common to both paths. The larger product benefit is the removed agent round trips: six actions become one call, while the transcript is already kept in order and deduplicated.

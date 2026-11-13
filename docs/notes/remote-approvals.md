# Answering approvals away from the computer

People step away from the computer while an agent works, and a held step (a payment, a send, a deletion) then times out as Deny after 60 s. [DESIGN.md](../DESIGN.md) lists "Approve from a notification, a phone push or a chat app" for M4. This note compares the ways in, sets out the threat model, and picks a first version.

Status: design and first version (approvals in the browser, through Tailscale, confirmed with a passkey). Off by default.

## What must hold

These come from DESIGN.md's threat model and the approval center (`internal/approval`), and no way in may weaken them:

1. **No agent approves itself.** Not an agent on this computer (it can reach 127.0.0.1, read the user's files, maybe see the screen), not an agent driving a phone on which the approvals page is open, not a page or program on the tailnet.
2. **The person sees what the desktop card shows**: the screenshot with the target and the finger's path, the title and detail, the rule that asked, the app, the phone, the agent, the time left.
3. **Secrets in memory only.** Nothing on disk gives a local process a way in, as with the web mirror's tokens.
4. **No Tailscale Funnel**, nothing on the public internet.
5. **Timeouts unchanged.** The deadline is the gateway's (60 s from when the card opens); no answer, or an answer that comes later, means Deny. Nothing remote extends it.
6. **Two places answering**: the first answer wins, the other is told it was already answered.

## Ways in

| | Approvals page next to the web mirror, over Tailscale | Pinky for Android / iPhone | Desktop notifications with actions |
| --- | --- | --- | --- |
| Works away from the computer | Yes, on any phone or tablet in the tailnet | Yes | No: only at the computer |
| What exists | The web mirror's listener, `tailscale serve` set-up, owner-login check, HTTPS through Tailscale | The Android companion runs on the phones Pinky drives; no iPhone app | MyGo has no notification actions (DESIGN.md "What we ask upstream" 3) |
| Shows the card | Yes: HTML, the screenshot as an image | Yes, with a new protocol | Only a title |
| Proof that the person answered | Passkey with user verification (Face ID, fingerprint, the phone's passcode) | Biometric prompt in the app | A click |
| Cost | One package, one page, a Settings section | A new app or a big companion change, store review, a pairing protocol | Upstream work first |
| Risk | Page reachable from the tailnet; mitigated below | The companion lives on phones agents drive | Low |

Desktop notifications do not help someone away from the computer, and a native app is a project of its own (and the Android companion sits on the very phones agents drive). The page reuses what the web mirror already set up and what the person already did (`tailscale serve`), so it is the first version. A push to wake the phone (Web Push, a chat app) can come later on top of the same page: see "Later".

## Threat model of the approvals page

| Threat | Mitigation |
| --- | --- |
| A process on this computer calls the page's API on 127.0.0.1, with any headers it likes (`Tailscale-User-Login`, `X-Forwarded-For`) | Headers are never trusted alone. Answering needs a passkey signature made with user verification on a phone the person added; the passkey's private key never leaves that phone's authenticator. Adding a phone needs the person's click in Pinky's window (below). |
| A local agent reads the "add a phone" link (files, clipboard, the screen) and adds its own passkey, a software one that claims user verification | The link only starts a request. Pinky's window then shows it with a six-digit code that the phone shows too, and adds it only when the person clicks Allow there: the same click that answers an approval in the panel. A link works once and for 5 minutes. Pinky's window is where approvals are answered today, so this adds no new trust. |
| An agent drives a phone on which the approvals page is open and taps Allow | User verification alone is not enough: Face ID can pass passively while the person merely looks at the phone, and another agent's card can be open while this agent drives. So when the person allows a browser in Settings they also say which phone connected to Pinky it runs on, or "Another device". While that phone has an agent's task (`DeviceView.TaskID`), and for 30 seconds after one ended (`DeviceView.LeaseEnded`), the page gets no cards and no screenshots there, and the server refuses the challenge and the answer (`busy`). The page fetches a challenge only when the person taps Allow or Deny (a second tap opens the passkey sheet), the challenge records the phone's `DeviceView.LeaseGen`, and the answer is refused if a task started or ended on the phone since, so an agent that taps Allow and lets go of the phone at once gains nothing. This is read right before `Decide`, not under one lock with it: a task that starts in that instant did not tap. A phone Pinky no longer knows counts as busy, and Settings cannot pick one it does not know. When the person picks Another device for a browser that says it runs on an iPhone or an Android phone while one is connected, Settings warns. Answer from another device, or in Pinky's window. Besides, Allow needs Face ID, a fingerprint or the passcode in the system's passkey sheet, and Pinky refuses to type into password fields. |
| Another person or device on the tailnet | Every request must come through `tailscale serve` from the login this computer is signed in as (`Tailscale-User-Login` against `Self.UserID`'s login), as the web mirror's control link requires; a tagged computer allows none. The session token and the passkey are still needed. |
| A web page in the person's browser calls the API | The API takes the session token in an `Authorization` header (never a cookie), so another site's form or `fetch` has nothing to send; a passkey works only on the origin it was made for. The page's CSP allows only its own origin. |
| Funnel | Requests with `Tailscale-Funnel-Request` are refused, as for the mirror (the mirror's handler wraps this one). |
| DNS rebinding, or a page on another port of this computer's tailnet name | The mirror's host check: requests must name this computer's tailnet name. The passkey's origin and RP id come from `tailscale serve`'s own state when the phone is added (the request's `Host` must match it), are stored with the phone, and every answer is checked against those stored values, never against what the request names. |
| Settings shows text a browser wrote | Settings names the browser from a fixed list parsed from its User-Agent ("iPhone · Safari"), never free text, and shows the code largest. One browser asks at a time: a new link turns down the request waiting. |
| The pairing's id leaks through Settings | Settings names a request by a counter. The page's id is its own secret, and `pair/status` also needs a second secret that only the page gets with the code. `pair/begin` without the right link, and `pair/finish` and `pair/status` per pairing, are limited to 30 calls a minute, so made-up calls cannot keep the real page out. Allowing a request gives the page another 5 minutes to pick its session up. |
| A copied passkey | A sign counter that the passkey keeps must count up; one that does not is refused (passkeys that keep none, as Apple's, stay at 0). |
| A session token kept for long | Sessions end after 12 hours unused and after 7 days anyway; the phone is then added again. |
| A replayed or reused answer | Each answer signs a fresh challenge that names the approval and the decision, works once, for this session only, and expires with the approval. The gateway's approval ids are single-use too. |
| The screenshot leaks | The page shows the approval's screenshot only, which is the one the panel shows (one-time codes covered, as there), only to an added phone with its session token, and only for cards the page may answer; nothing is cached (`Cache-Control: no-store`). |
| Secrets on disk | Session tokens, passkey public keys, add-a-phone links and pending requests live in memory only. Quitting Pinky forgets the added phones; the person adds them again. Turning the switch off, or the web mirror off, forgets them too. |
| Two answers | The approval center takes the first decision and refuses the next (`ErrNotOpen`); the page then says the step was already answered, or that time ran out. The panel on the computer closes as it does for any answer. |
| Slow answer | The deadline is the gateway's. A passkey sheet left open past it signs a challenge that is no longer good, and the step has already been denied. |

### What the page can answer

Only an action's approval (an Ask action) and an agent's own question (`phone_request_approval`), with **Allow once** and **Deny**. Not:

- an agent's first connection (it needs a scope the person picks),
- opening a blocked app (`Unblock`, which lets the agent use that app for the rest of the task),
- handoffs, unlocking, phone setup,
- Allow for this task, Always allow, Take over, Deny with a note.

The page decides by the approval's kind (`NeedView.Approval`), not its rule's name. Those stay in Pinky's window; the page lists them, without their screenshot, as "Answer this on the computer". Fewer choices remotely keep a hurried tap from granting more than one step.

### Residual risk

- Someone holding the person's unlocked phone with the page open can answer (they could also open Pinky's window at the computer). The passkey asks for Face ID, a fingerprint or the passcode on every answer.
- A passkey made on an iPhone may sync to the person's other Apple devices, the Mac included. Using it still needs Touch ID or the login password there, which an agent does not have; the sign of it is the same user verification.
- An agent that can click in Pinky's window can also click Allow in the panel. That is DESIGN.md's "agent with shell access" row and is unchanged by this.

## First version

Settings › Phones › Web mirror gets **Answer approvals in the browser**, off by default, with the mirror on and Tailscale serving it. Then:

1. **Make a link** next to **Add a phone** makes a one-time link, `https://<computer>.<tailnet>.ts.net:<port>/approvals#pair=<token>`, good for 5 minutes. The person opens it on their phone (copy it to the phone, or type it).
2. The page asks the phone to make a passkey for this computer's tailnet name (`navigator.credentials.create`, user verification required, a resident key so the phone keeps it). It sends the passkey's public key (`getPublicKey()`, P-256 ES256) and shows a six-digit code.
3. Settings shows "A browser asks to answer approvals" with the Tailscale login, the browser and, largest, the same code; **Which phone is this browser on?** with the phones connected to Pinky and Another device; and **Deny**, plus **Allow** once a phone is chosen. Allow adds the phone; the page then gets its session token, which it keeps in the browser's local storage.
4. The page lists the open approvals, refreshing every 2 s while it is shown. A card shows the phone, the agent, the title and detail, the rule, the app, the time left, and the screenshot with the target box and the finger's path.
5. **Allow** or **Deny**: the tap fetches a challenge for that approval and decision, and the button asks for a second tap; the phone's passkey sheet asks for Face ID, a fingerprint or the passcode; the server checks the signature (P-256 over the authenticator data and the hash of the client data), the challenge, the origin, the RP id hash and the user-verified flag, then calls the gateway's `Decide`, exactly as the panel does.
6. Settings lists the added phones with **Remove**.

Code:

- `internal/webapproval`: the handler, the passkey check, the pairing and sessions; it reaches the gateway through an interface with `Needs`, `Devices` (read only, for the busy rule) and `Decide`, so it cannot hand back, unlock or stop.
- `internal/webmirror`: one additive option, `Approvals http.Handler`, served under `/approvals` behind the mirror's own checks (Funnel, host). The mirror's `Source` stays without approvals.
- `ui`, `cmd/pinky`: the switch, the requests to allow and the list of phones; the page's words in `cmd/pinky/locales`.
- `internal/gateway`: one additive field, `NeedView.Approval`, the approval's kind. No change to `internal/approval`.

## Tested (2026-11-12)

- `internal/webapproval` against the real gateway on a demo phone, served through `webmirror`'s handler with a fake `tailscale serve` (headers) and a software P-256 passkey: Allow and Deny reach the agent; refused when not through Tailscale, from another tailnet login, while off, through Funnel, with a reused or made-up link, a passkey without user verification, a phone turned down in Settings, no or a made-up session, another origin; answers that do not count (no user verification, another key, a used challenge, another phone's session, a removed phone); a Deny challenge answers Deny; two places answering (the window first, the page is told); the timeout unchanged (a challenge never outlives the approval, a late answer is "gone"); cards the page may not answer (new agent, Unblock, unlock) are listed without their screenshot and refused; the screenshot as JPEG with `no-store`. After the security review: a browser on a phone an agent holds is refused at the challenge and, when the task starts after it, at the answer, while another device answers; no origin from `tailscale serve`, another port, and an answer signed for another port are refused; `pair/status` needs the page's secrets, not the id Settings shows; a browser's own text is never shown and a new link turns down the waiting request; sessions expire when idle and when old; `pair/*` is rate-limited; a sign counter that does not count up is refused. After the re-review: a task that starts and ends between the tap and the answer is refused (the lease count changed), as is a challenge within 30 s of a task's end; the gateway counts leases (`LeaseGen`, `LeaseEnded`); a busy browser gets no cards and no screenshots while another device does; an unknown phone cannot be picked and a forgotten one is busy; a request allowed at the end of its time still gives its session; made-up `pair/status` calls do not keep the real page out; Settings keeps the chosen phone by id and warns when Another device is picked for a browser of a kind that is connected.
- `ui`: the Settings section with fakes (`TestWebApprovalsSettings`), and screenshots in light and dark, English, Chinese and Japanese (`PINKY_SCREENSHOTS=dir go test -run TestWebApprovalsScreenshots ./ui`).
- Not tested: a real passkey in Safari on an iPhone or in Chrome on Android, a real `tailscale serve`, a real phone.

## Later

- **A push that wakes the phone.** The page answers only while it is open. Web Push (an installed web app on iOS 16.4+, Chrome on Android) would carry a content-free "Pinky needs you" through Apple's or Google's push service; the card and the answer stay on the tailnet. A chat app would do the same with its bot.
- Desktop notifications with Allow / Deny once MyGo has notification actions.
- Allow for this task from the page, with the panel's wording.

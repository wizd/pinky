# App guides

Pinky's own agent (Ask Pinky on the computer, Pinky on Android and on the iPhone) reads a short guide when a task opens an app it has one for. A guide tells it how the app is laid out and where things are, so it takes fewer wrong turns. Each guide is one Markdown file in this folder; anyone can add one or fix one.

## Format

```markdown
---
app: WeChat                          # the name the guide goes by
names: [WeChat, 微信, Weixin]         # the app's names on phones, in any language
android: [com.tencent.mm]            # Android package names
ios: [com.tencent.xin]               # iPhone bundle ids
---
- Bottom tabs: 「微信」 Chats, 「通讯录」 Contacts, …
- …
```

- The file name is the guide's id: lowercase letters, digits and hyphens (`google-maps.md`).
- `android` and `ios` are how Pinky finds the guide: the app on the phone's screen has one of these ids. List every package or bundle id the app ships under (a vendor's own Clock, a global edition). `names` is used only where the phone does not say the app's id. A guide needs at least one id, and no id may be in two guides.
- The body is a list of short lines in plain English. Write the app's own words for buttons and tabs as the app shows them, in 「」 for Chinese and Japanese apps, followed by their English meaning.

## What goes in

- Facts about the app: its tabs and main screens, where search is, where a common feature lives, what a screen shows, pop-ups that come up often, and how the app differs between its versions or phone makers.
- Keep it short: at most 1,000 characters for the body. Every line costs the person tokens on every task in this app. Leave out what any agent sees on the screen anyway.
- Write what you know from using the app. If you are not sure a label or a place is right, leave it out. Do not copy another project's texts.

## What stays out

- Rules for the agent: when to ask the person, what not to do, when to stop. Pinky's own rules and approvals decide those, for every app alike. A guide may say that a step asks for a payment password; it never says how to get past it.
- Anything that helps get around Pinky's rules, an app's security, a sign-in, a payment confirmation, a captcha or a limit the app sets.
- Personal data, accounts, phone numbers, addresses, links to sign in.
- Instructions to the model ("ignore", "always", "you must"). A guide describes the app; it does not command.

## Checking

`go test ./guides` reads every guide: its front matter, the length of its body, that its ids are unique, and that it holds none of the words that belong to Pinky's rules. `go test ./internal/pilot -run Guide -v` prints what the guides add to a task.

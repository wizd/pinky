# Phones away from home

Pinky can use an Android phone that is not on the computer's network: the computer at home, the phone with you on mobile data or on another Wi-Fi. It reaches the phone through [Tailscale](https://tailscale.com), a free app that keeps your own devices connected to each other privately wherever each one is. Agents can be elsewhere too: see [Remote agents](mcp.md#over-http).

iPhones need a cable for now. An Android 10 phone, which has no Wireless debugging, can use the same way on the computer's own Wi-Fi: see [Wi-Fi without Wireless debugging](#wi-fi-without-wireless-debugging).

## What you need

- Tailscale on the computer that runs Pinky and on the phone, signed in to the same account (the same *tailnet*). Personal use is free.
- No other VPN on the phone while you use it this way. Android runs one VPN at a time: turning on another VPN or a proxy app that works as one turns Tailscale off.
- An Android phone (Android 10 or later) that Pinky already uses: you set it up once while it is near the computer, by cable or on the same Wi-Fi.
- The computer awake while you are out ([below](#keeping-the-computer-awake)).

## Setting it up

1. **On the computer**, install Tailscale ([download](https://tailscale.com/download)) and sign in. On a Mac, either app works (from the website or the App Store). On Linux, `sudo tailscale up`.
2. **On the phone**, install Tailscale from Google Play, open it and sign in to the same account. Allow the VPN when Android asks. Turn off any other VPN or proxy app first, as Android runs one VPN at a time.
3. **In Pinky**, with the phone connected by cable or Wi-Fi, open **Settings › Phones**, and under the phone, next to **Use away from home**, click **Turn on…**. Pinky says what this opens (below) and asks first. It then has the phone's adb listen on the network (`adb tcpip 5555`) and connects to the phone at its Tailscale address.

That's all. Pinky now keeps a connection to the phone through Tailscale and uses it whenever the phone has no connection on the computer's own network: the phone stays in Pinky's list as you leave, with **Tailscale** under its name instead of Wi-Fi or USB.

**Add phone › Android › Phone not on this network? Use Tailscale** shows the same steps, says what is missing on the computer, and lists the Android phones on your tailnet with a **Connect** button each. A phone that is not set up yet can also be connected there at the port its Wireless debugging screen shows, while it is on a Wi-Fi network with Wireless debugging on.

### Without Tailscale on the computer

If the computer reaches your tailnet another way, such as a proxy with a Tailscale outbound, it needs no Tailscale app of its own. The phone still runs Tailscale. Under the phone in **Settings › Phones**, **Use away from home** says it needs Tailscale on this computer; click **Enter address…** and type the phone's tailnet address, which Tailscale on the phone shows (such as 100.101.2.2).

Before it turns on, Pinky checks over the cable or Wi-Fi that the address is in Tailscale's ranges (100.64.0.0/10 or fd7a:115c:a1e0::/48) and on the phone's own VPN, then that this computer reaches the phone there and that what answers is that phone (it carries the mark Pinky leaves on it). Pinky can't see how this computer routes traffic to the address, and adb's own connection is not encrypted: the connection is only as private as your own route to the tailnet.

## What it opens, and how to turn it off

- **adb on the network.** After `adb tcpip`, the phone's adb accepts connections on port 5555 on every network the phone joins, not only through Tailscale: home Wi-Fi, a café's Wi-Fi, mobile data, IPv4 and IPv6. It lets in only the computers the phone already trusts (the ones you tapped **Allow** for); any other computer makes the phone ask, and you tap **Cancel**. The connection itself is not encrypted, except through Tailscale, which encrypts everything between your devices.
- **How long.** Until the phone restarts. After a restart Pinky can no longer reach the phone from afar, and Settings › Phones says so once the phone is near the computer again, with **Turn on again**.
- **Turning it off.** **Turn off** in Settings › Phones (it runs `adb usb` on the phone, which stops the listening, and forgets the phone's address); or restart the phone; or turn off USB debugging. While the phone is away, turning it off cuts Pinky off until the phone is back.
- **Removing a phone** from Pinky turns this off too, as **Turn off** does when Pinky can reach the phone, and drops its connection through Tailscale.
- **Tailscale itself.** Pinky reads Tailscale's status (`tailscale status --json`) and nothing else: it has no Tailscale credentials of its own and never changes Tailscale's settings.

Why not Wireless debugging? Android turns Wireless debugging off when the phone leaves Wi-Fi (on mobile data there is none) and, on Android 11 to 16, when the phone joins another Wi-Fi network; it takes a new port each time it comes on, and Pinky finds that port with mDNS, which does not cross a VPN. Wireless debugging's own connection is encrypted and paired, so Pinky uses it whenever the phone is on the computer's network, and the connection through Tailscale only when that one is gone.

## Keeping the computer awake

A sleeping computer cannot reach the phone. **Settings › Phones › Keep this computer awake for phones away** (on by default) keeps it from sleeping when idle while a phone is connected through Tailscale, as Pinky does anyway while an agent task runs. On a Mac it shows in `pmset -g assertions` as "Pinky: a phone is connected through Tailscale"; on Windows the app makes a power request with that reason, and on Linux it asks the desktop's portal. The display may still sleep.

`pinkyctl daemon`, which has no window, does the same, following the app's setting as it reads it at start: an assertion of the same name on a Mac, the same power request on Windows, and on Linux a systemd lock, which `systemd-inhibit --list` shows. That needs systemd, and logind's policy may refuse the lock to a process outside a login session; the daemon then logs "cannot keep the computer awake" and tries again 5 minutes later.

What it cannot stop:

- **A MacBook's lid.** Closing the lid puts a Mac laptop to sleep, unless it runs in clamshell mode (plugged in, with an external display). To keep a closed MacBook awake anyway, `sudo pmset -a disablesleep 1` turns sleep off altogether (`sudo pmset -a disablesleep 0` turns it back on); it is a system setting, so mind the battery.
- **Sleep when nothing runs.** On a Mac that should stay up while you are out, also turn on System Settings › Battery › Options › **Prevent automatic sleeping on power adapter when the display is off** (or Energy on a desktop Mac).

## Speed and data

Measured on 2026-11-08 with the Mi 10 Pro (Android 17) and a MacBook Pro at home, on a background display scrolling Settings once a second. "Swipe to picture" is the time from sending a swipe to the first frame that shows it.

| Where the phone is, and how Pinky reaches it | Stream | Frames a second | Swipe to picture (median / 90th percentile) | adb round trip |
| --- | --- | --- | --- | --- |
| Home Wi-Fi, Wireless debugging | 2.5 Mbit/s, 30 fps | 22.4–23.6 | 146–164 ms / 208–212 ms | |
| Home Wi-Fi, adb tcpip | 2.5 Mbit/s, 30 fps | 23.6–23.7 | 138–149 ms / 201–225 ms | |
| Home Wi-Fi, through Tailscale (a direct path over the home network) | 2.5 Mbit/s, 30 fps | 23.9 | 118 ms / 393 ms | 35–110 ms |
| A phone's 4G hotspot with a weak signal, through a Tailscale relay | 2.5 Mbit/s, 30 fps | frames in bursts, seconds apart | | 0.4–4.8 s; adb's connection dropped after 98 s |
| The same hotspot | 1 Mbit/s, 15 fps (Pinky's default through Tailscale) | frames in bursts every 2–4 s | | 1.4–4.6 s; the connection held |

- **At home**, Tailscale finds the direct path over the home network: as fast as Wi-Fi. Pinky still uses the local connection while it has one.
- **The phone's upload is what counts away from home**: the picture goes up from the phone. That hotspot passed 0.35 to 0.5 Mbit/s up (its ping to the internet: 116 ms on average), so any stream over that queued, and taps waited behind it. A usual 4G or 5G upload (several Mbit/s) carries Pinky's 1 Mbit/s with room to spare. Through Tailscale, Pinky streams at 1 Mbit/s and 15 frames a second instead of the 2.5 Mbit/s and 30 it uses on the local network.
- **Relayed or direct.** On this home network the computer's traffic leaves through a proxy gateway, so Tailscale could not open a direct path to the phone on the hotspot and relayed it through its Singapore server (32 ms from the computer). A direct path, when Tailscale can make one, saves that detour.
- **Moving between networks.** Coming home, the connection through Tailscale paused for about 5 s and carried on, and adb connected Wireless debugging again 2 s later. Leaving home, adb dropped the Wireless debugging connection as the phone changed networks, and Pinky went on through Tailscale. Android 17 kept Wireless debugging on when the phone joined the hotspot; Android 11 to 16 turn it off.

Data, as Tailscale counted it at home (the same on mobile data):

| What Pinky does with the phone | Data |
| --- | --- |
| Connected, no picture open | about 2 kbit/s each way: under 2 MB an hour |
| Picture open, screen still | 250 kbit/s: about 110 MB an hour |
| An agent tapping every 5 s (2.5 Mbit/s stream) | 2.3 Mbit/s: about 1 GB an hour |
| Scrolling every second (2.5 Mbit/s stream) | 6.6 Mbit/s: about 3 GB an hour, as the encoder spends what it saved while the screen was still |

At 1 Mbit/s, steady change takes about 450 MB an hour. The away-from-home tests above used about 55 MB of the hotspot's data.

## When it does not work

- **"Tailscale isn't connected on the phone"** (Settings › Phones, under **Use away from home**): the phone has no Tailscale address. Open Tailscale on the phone and sign in to the same account as on the computer. If it was on, another VPN or proxy app has most likely taken its place: Android runs one VPN at a time, and turning another one on turns Tailscale off. Turn that one off, then turn Tailscale on again. **Set up Tailscale** next to it opens the steps.
- **"Signed in to another tailnet than this computer's"**: Tailscale is on on the phone, but on another account than the computer's. Sign in to the same account on both; **Set up Tailscale** shows the tailnet the computer is on.
- **"Doesn't take connections from Pinky yet"**: the phone's adb does not listen on the network. It stops when the phone restarts. Bring the phone near the computer (cable or Wi-Fi) and turn **Use away from home** on again.
- **"Couldn't reach the phone through Tailscale"**: Tailscale on the phone is off or has no internet. Open Tailscale on the phone, and turn off any other VPN or proxy app there. Android may stop it to save power: in Android's settings, let Tailscale run in the background, or make it the always-on VPN (Settings › Network › VPN).
- **Slow, "connected through a Tailscale relay"**: the two devices could not reach each other directly, so their traffic goes through one of Tailscale's relay servers. `tailscale ping <phone>` on the computer says which way it goes. A home router or proxy that rewrites the computer's UDP traffic (a VPN gateway, Surge or Clash in gateway mode) often causes it: let Tailscale's traffic go direct there (UDP port 41641 and Tailscale's addresses, 100.64.0.0/10).
- **The phone shows up twice for a moment, or reconnects as you leave**: normal. Pinky moves from the local connection to the one through Tailscale; a task on the phone carries on.

## Wi-Fi without Wireless debugging

Android 10 has no Wireless debugging, so the phone pairs with no computer over Wi-Fi. Pinky moves it to Wi-Fi the older way, with the same adb listening on the network as above:

1. Plug the phone in and choose **Use Wi-Fi instead…**: in the ⋯ menu under its picture, in **Settings › Phones**, or at the end of **Add phone › Android**. The phone must be on the same Wi-Fi as the computer.
2. Pinky asks first and says what it opens: while it is on, any computer on the same network can try to connect to the phone. If the phone asks **Allow USB debugging?** when you are not connecting it yourself, tap **Cancel**, never **Allow**. The connection is not encrypted; use it only on Wi-Fi you trust.
3. Pinky has the phone's adb listen on the network (`adb tcpip 5555`), connects at the phone's Wi-Fi address and says when you can unplug it. If the phone cannot be reached there (another network, or one that keeps devices apart), Pinky stops the listening again and says so.

Pinky keeps the address and connects again whenever the phone is back on the network: after it slept, after the Wi-Fi dropped, and at a new address, which it finds by asking the network for the phone's adb (mDNS, `_adb._tcp`). It keeps a connection over Wi-Fi while the cable is in too, so pulling the cable does not cut the phone off.

When the phone restarts, its adb stops listening: the phone's page says so, instead of only "offline". Plug it in once and choose **Use Wi-Fi again**. **Stop the Wi-Fi connection** (the same menu, or Settings › Phones) turns it off for good: the phone's adb stops listening (`adb usb`), and Pinky reaches the phone only by cable. On a phone also used away from home, it ends that too.

Checked on a Pixel (2016, Android 10) on 2026-11-08: moved to Wi-Fi in 1.4 s, with no prompt on the phone (it trusted the computer over USB already); a dropped connection came back in 134 ms; unplugged, it went on over Wi-Fi at once; restarted, it was offline at once and said "plug it in once" as soon as it refused the connection (about 40 s later, once it was up); plugged in, **Use Wi-Fi again** took 1.6 s; **Stop the Wi-Fi connection** left nothing listening. On the Mi 10 Pro (Android 17, which takes the same switch), a phone at a new address was found with mDNS and connected again in 4.9 s.

## How it works

For developers; the code is in `internal/tailnet` and `internal/driver/android/network.go`, `tailnet.go` and `wifi.go`.

- `internal/tailnet` runs `tailscale status --json` (on PATH, then `/Applications/Tailscale.app/Contents/MacOS/Tailscale`, Homebrew's, the Windows and Linux install paths), at most every 3 s, and reads the state, the tailnet's name and each device's addresses, OS, online state and path (direct or relayed).
- The Android driver keeps the phones whose adb Pinky had listen on the network in `network-phones.json` in Pinky's config folder: the phone's id, its port, its Wi-Fi address (`driver.WiFiSwitch`) and its Tailscale node and address (`driver.Tailnet`).
- `List` starts a reconnect pass now and then (every 20 s while an address has no connection, every minute otherwise). For each kept address without a connection, a 3 s TCP probe goes first (the adb server would wait 75 s for an address nothing answers); then `adb connect`. A Wi-Fi address that does not answer, or refuses, is looked up again with mDNS for 6 s (a dozing phone answers in up to 5 s); a Tailscale address is tried only while Tailscale says the phone is online, and a new address for the same node is picked up. Failed addresses wait 10 s, doubling to 2 min.
- A kept phone with no connection at all is listed offline, with `driver.Info.Setup` `network_off` once its adb refused (it restarted: plug it in once) or `network_away` while it does not answer; the client words them.
- The phone's addresses come from the phone (`ip -o addr show`): the first IPv4 address of a `wlan` interface, and the VPN interface's addresses in 100.64.0.0/10, which carriers use too, matched against the tailnet's devices.
- `Away` says what keeps a phone from being set up (`driver.Away.Missing`): `tailscale_phone` when the phone has none of those addresses, or only one in 100.64.0.0/10 that the tailnet does not know, which another VPN may hold (Cloudflare WARP's are there); `tailscale_other` when one of them is in Tailscale's own IPv6 range (fd7a:115c:a1e0::/48) and the tailnet does not know it, so the phone's Tailscale is on another tailnet.
- A phone with several connections is listed once, by the best (USB, then the local network, then Tailscale), so it moves to the next when one goes; `driver.Info.Via` is `tailscale` when Tailscale is the one.
- Turning either on runs `tcpip:5555` over a connection the phone has (adbd restarts; the connection comes back in about 2 s; on Android 17 over Wireless debugging its reply may not get through, so the connection at the new address confirms it) and connects at the new address; off runs `usb:`. Removing the phone from Pinky turns both off.
- An address entered by hand (`driver.TailnetByHand`, `tailnetmanual.go`) is kept with `manual` set: without `tailscale status`, a TCP probe alone says whether the phone is reachable, and what answers must serve the mark Pinky left on the phone (`identity.go`). [notes/tailnet.md](notes/tailnet.md) has the checks.
- A phone reached through Tailscale streams at 1 Mbit/s and 15 frames a second (`tailnetBitRate`), unless Pinky's configuration sets a rate; the settle threshold (about 300 bytes a packet) and screenshots (`screencap`) do not depend on it.

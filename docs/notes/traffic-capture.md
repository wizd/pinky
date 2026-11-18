# Network traffic capture

Status: off by default; the owner's decisions are taken (below). Built: Pinky's own HTTP(S) proxy with a certificate authority made per install, its key in the macOS keychain (`internal/traffic`), the host blocklist, the in-memory capture store, redaction, the gateway wiring that runs the proxy for a phone during its tasks and serves a PAC file and the certificate whenever its switch is on (`internal/gateway/capture.go`), the `phone_traffic` MCP tool with its second ask for bodies, the per-phone switch and its setup in Settings › Phones, and the Android cable route behind a switch the person presses. Not built: routing an iPhone through Pinky without a Wi-Fi proxy (design below), WebSocket and HTTP/2 capture. Tried on a real phone on 2026-11-18, without changing its settings: a Pixel (Android 10) on Wi-Fi reached the proxy with curl's own proxy option. The PAC file and the certificate were served, a `CONNECT` to the setup port got `403`, HTTP and HTTPS with Pinky's certificate trusted (`--cacert`) were decrypted and recorded, an untrusted handshake failed and the next request to that host passed through undecrypted, `www.alipay.com` was tunnelled as blocked, a request to `127.0.0.1` got `502`, and a second phone on the same network (a Mi 10 Pro) was refused. Not tried yet: the phone's own Wi-Fi proxy set to the PAC file, the certificate installed in the phone's settings, and the cable route, which all change the phone's settings.

Agents sometimes need to know which API an app calls: to reproduce a bug, to check what a button sends, to find the endpoint behind a screen. `phone_traffic` gives them the HTTP and HTTPS requests the phone made during their task, with credentials redacted, after the person allowed it.

## Where the traffic comes from

Pinky captures it itself. There is no outside gateway, proxy app or Mac app to set up: the phone sends its web traffic to an HTTP proxy that Pinky runs on the computer, and Pinky decrypts HTTPS with a certificate the person installed and trusted on the phone.

Other ways were weighed and left out:

- **A gateway or proxy app on the network** (Surge, Charles, mitmproxy). Needs third-party software and its own setup, and Pinky would read it through an API it does not control, often without bodies or status codes. Dropped: Pinky must capture the traffic itself.
- **A VPN on the phone** (`VpnService` on Android, a Network Extension on iOS). Sees every app without a proxy setting, but takes the phone's only VPN slot and, on iOS, needs an app with a Network Extension entitlement. Kept as a design for the iPhone (below), not built.
- **Packet capture** (`rvictl` on a Mac for an iPhone on USB, `tcpdump` with root on Android). Sees packets, not decrypted requests; no use for HTTPS.

## The proxy

`traffic.Proxy` is a plain Go HTTP/1.1 proxy built on the standard library:

- **HTTP**: requests in absolute form (`GET http://host/path`) are forwarded with `http.Transport` and recorded.
- **HTTPS**: `CONNECT host:443`. Pinky answers `200`, peeks at the first byte, and when it is a TLS handshake (`0x16`) it finishes the handshake itself with a leaf certificate for the host, signed by Pinky's CA, then reads the HTTP/1.1 requests inside, forwards each over a fresh TLS connection to the real host (verifying it against the system roots) and records it. ALPN offers only `http/1.1`.
- **Not decrypted**: when the phone refuses Pinky's certificate (the app pins its certificate, or the phone does not trust Pinky's CA), the handshake fails. Pinky marks the host as pinned for 10 minutes; the next `CONNECT` to it becomes a raw tunnel, recorded as one "not decrypted" request with only its host and sizes. Anything after `CONNECT` that is not TLS is tunnelled raw too. The pinned set is a heuristic: one failed handshake for another reason (a phone going to sleep mid-handshake) makes a host pass through undecrypted for 10 minutes. Serving the certificate clears the set, so a person who just installed it does not wait.
- **Blocked hosts, never decrypted**: a `CONNECT` to a host on the blocklist becomes a raw tunnel before any handshake, even for a phone that trusts Pinky's CA, and is recorded only as `https://host`, marked blocked, with no sizes, headers or bodies. Plain HTTP to such a host is passed on and recorded the same way. The blocklist is `traffic.BlockedHosts`, a built-in list of domains of banking, payment, brokerage, crypto-exchange and password-manager services in China and elsewhere (Alipay, WeChat Pay, UnionPay, the big Chinese banks, PayPal, Stripe, 1Password, Bitwarden, LastPass, …), plus the hosts the person adds for the phone under Never decrypt in its setup (`Gateway.SetTrafficBlocklist`, saved in `traffic_blocklist`). A host matches a domain when it is the domain or one of its subdomains. A domain an app uses that is not on the list is decrypted as usual: the list is a floor, and the person adds what it misses.
- **The name is checked three times**: the `CONNECT` host, the TLS handshake's server name (SNI) and the `Host` of every decrypted request. An app can `CONNECT` to an IP address or a harmless name and name the bank only later:
  - The SNI is read before any certificate is signed (`GetConfigForClient`). When it is blocked, Pinky signs nothing and sends nothing: the handshake bytes it read (kept up to 64 KB) are replayed into a raw tunnel to the target, so the app talks to the real server, and the request is recorded as the SNI's host only, blocked.
  - An SNI that names another host than a `CONNECT` given as a name is refused (the connection closes, recorded as failed with a note); it does not mark the host as pinned. A `CONNECT` to an IP address may carry any SNI.
  - A decrypted request whose `Host` is blocked: the name shows up only inside the decrypted stream, so that one request has been decrypted in memory. It is passed on, recorded as its host only (no URL, headers or bodies), the connection ends, and for 10 minutes every `CONNECT` to that address is tunnelled raw and recorded as the blocked host.
- **The setup files**: a request to the proxy itself (`http://<proxy>/`) gets a short page; `/pinky-ca.crt` serves the CA as DER (`application/x-x509-ca-cert`, which both iOS Safari and Android's browser offer to install), `/pinky-ca.pem` as PEM, and `/proxy.pac` the proxy auto-config file (below).
- **Only the phone**: every connection's remote address is checked against the phone Pinky expects (`Proxy.Client`). The setup files go to a phone whose switch is on; the proxy takes a phone only while its task runs (and on the computer's loopback, the phone routed over its cable). Any other address gets the connection closed before a byte is read, and is counted (`Rejected`); the refusal is logged at most once a minute.
- **Nothing on this computer**: the proxy's own dial checks the address it actually connects to, after DNS (`net.Dialer.Control`), so a name that resolves there is caught too. It refuses loopback, unspecified (`0.0.0.0/8`, `::`), link-local (including `169.254.169.254`, the cloud metadata address), multicast and broadcast addresses, and every address of this computer's own interfaces (read again at most every 10 seconds); the phone gets `502`. Private LAN ranges (`10/8`, `172.16/12`, `192.168/16`, `fc00::/7`) stay allowed: the phone reaches them directly anyway, outside the proxy, and apps use them (a printer, a NAS, a TV), so refusing them would break apps and protect nothing.
- **Limits**: request headers at most 1 MB (`431` past it); a connection whose reads stall for 2 minutes, headers or body, is closed (15 seconds for a TLS handshake); a server that does not start its answer in 2 minutes fails the request (`ResponseHeaderTimeout`, response headers at most 1 MB); at most 512 connections at once, past which new ones are closed and counted as refused.
- **Only when asked**: what the proxy forwards is recorded only when `Proxy.Record` says so for that phone: while it has a task and the person does not hold it.

### The certificate authority

`traffic.LoadCA(dir, keys)` reads or makes the CA. The certificate is `traffic-ca.crt` (PEM, mode `0600`) in Pinky's data directory (`~/Library/Application Support/Pinky` on a Mac). The private key is kept:

- **on macOS, in the login keychain**, as the generic password `traffic-ca-key` of Pinky's keychain service (`secret.TrafficCAKeyAccount`), read without a prompt like the model keys. A Pinky built from source with another signature may get the system's "wants to use confidential information" prompt the first time;
- **on Linux and Windows, and for `--fake` phones**, as `traffic-ca.key` (PEM, mode `0600`) next to the certificate, readable by the person's user only.

An earlier Pinky's `traffic-ca.key` moves into the keychain at the first start on macOS and the file is deleted, so the CA the phones trust keeps working. When the keychain cannot save the key, the proxy does not start and the setup says Pinky couldn't read or make its certificate; the key never falls back to a file on macOS. The key is never in the repository and never leaves the computer; only the public certificate goes to the phone.

- ECDSA P-256, valid 1 year, named "Pinky Traffic CA" with a short random suffix so that two installs are told apart on a phone. It can sign only leaf certificates (`MaxPathLen 0`).
- **Renewal**: a CA within 30 days of its end (`traffic.RenewBefore`) is replaced, at start or while Pinky runs (each capture pass checks). The setup then says Pinky made a new certificate and the phone has to install it (steps 2 to 4) and remove the old one; it shows the end date the rest of the time. The warning lasts until Pinky restarts. A phone that keeps only the old certificate sees HTTPS as not decrypted until it installs the new one.
- The CA is marked for server authentication only (`ExtKeyUsage: ServerAuth`), so it cannot sign client or code-signing certificates the phone would honour.
- Leaf certificates are made on the fly per host (ECDSA P-256, valid 24 hours but never past the CA, the host as DNS name or IP SAN) and kept in memory, at most 1,024; one within an hour of its end is made again.
- Settings › Phones shows the CA's name, SHA-256 fingerprint and end date, so that the person can compare it with what the phone shows, and the steps to remove it from the phone: on an iPhone, Settings › General › VPN & Device Management › Pinky's profile › Remove Profile; on Android, Settings › Security & privacy › More security settings › Encryption & credentials › User credentials › Pinky's certificate › Remove.

Removing the certificate file (or the keychain item) makes a new CA at the next start; the phones then have to install the new one.

## How the phone reaches the proxy

Pinky listens only on the address the phone reaches, at two ports there:

- **The setup port, 9278** (the proxy's plus one), whenever Pinky runs and the phone's Read network traffic switch is on. It serves only `/proxy.pac` and the certificate, to the phone's address only. It proxies nothing and records nothing: a `CONNECT` or a request for another host gets `403`.
- **The proxy port, 9277**, only while an agent's task runs on the phone. Outside tasks nothing listens there.

- **The phone's addresses**, from what Pinky already knows (`driver.NetAddresser`): on Android, `ip -o addr show` over adb; on an iPhone, the network addresses usbmuxd lists for a phone paired over the network. Read again at most every 30 seconds.
- **The listening address**: the computer's own address on the network the phone is on (the interface whose subnet contains the phone's address), or the computer's tailnet address when the phone's is a Tailscale one (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`). Carriers use `100.64.0.0/10` too (CGNAT), so an address there counts as the tailnet's only when Tailscale on this computer lists it as a peer (`Options.TrafficTailnet`, read only when the computer has a Tailscale address). Never `0.0.0.0`; link-local addresses are skipped. With no match, nothing listens and the setup says "Pinky doesn't know an address of the phone on a network this computer is on".

### The PAC file

The phone's Wi-Fi proxy is set to Automatic with the proxy setup link, `http://<computer>:9278/proxy.pac`:

```js
function FindProxyForURL(url, host) {
  if (isPlainHostName(host) || dnsDomainIs(host, ".local")) {
    return "DIRECT";
  }
  return "PROXY 192.168.1.5:9277; DIRECT";
}
```

During a task the phone's requests go to the proxy. Outside tasks the proxy port refuses the connection, and the phone falls back to `DIRECT`: a refused or timed-out proxy is skipped, an HTTP error is not, which is why the PAC file is served from another port than the proxy's. Hostnames without a dot and `.local` names always go directly.

When the PAC URL itself cannot be fetched (the phone on another network, the computer asleep or Pinky not running), what the phone does is up to it. Neither case has been tried on a real phone yet; from Apple's and Android's documentation and reports:

- **iOS** keeps the last PAC file it fetched for a while and fetches it again on network changes. With none, it has been seen both to go direct and to fail requests until the PAC URL answers. Treat it as possibly offline.
- **Android** fetches the PAC file through its PacProxyService; when that fails it goes direct.

The setup says so: set the proxy back to Off when the feature is no longer needed.

What the person does on the phone, shown in the setup with the proxy setup link and the certificate link:

- **iPhone**: Settings › Wi-Fi › ⓘ › Configure Proxy › Automatic, with the proxy setup link as the URL. Open the certificate link in Safari, install the profile in Settings › General › VPN & Device Management, then turn on full trust in Settings › General › About › Certificate Trust Settings. Two separate steps; installing alone is not enough.
- **Android**: the Wi-Fi network's proxy set to Proxy Auto-Config with the proxy setup link as the PAC URL, then the certificate installed in Settings › Security › Install a certificate › CA certificate. Apps targeting Android 7 (API 24) or later trust only system CAs unless they opt in with `networkSecurityConfig`; most store apps do not, so their HTTPS shows as not decrypted. The browser, debug builds and apps that opt in show in full. The setup says so plainly. Trusting it system-wide needs root.

The setup files are served whenever the switch is on, not only while the setup shows in Settings, so a phone can fetch the PAC file at any time; leaving the settings page closes nothing.

### Android over the cable

A switch in the phone's setup, "Route over the cable during tasks", off by default and pressed only by the person (`Gateway.SetTrafficRoute`; no agent tool reaches it). With it on, when a task starts on the phone Pinky:

1. reads the current `settings get global http_proxy` and saves it, with the port, in the history database (`traffic_routes`) before changing anything;
2. runs `adb reverse tcp:9277 tcp:9277`, so that `127.0.0.1:9277` on the phone reaches the proxy on the computer's loopback;
3. sets `settings put global http_proxy 127.0.0.1:9277`.

`adb reverse` alone routes nothing: the phone's proxy setting is what sends apps' traffic to it. Requests then arrive from `127.0.0.1`, which the proxy accepts only while a route exists, as that phone.

Pinky's own setting (`127.0.0.1:<port>`, `localhost` or `::1`) is never saved as the one to put back: when the phone still points at Pinky (an earlier route was never undone), the value noted for that earlier route is kept, and with none, no proxy.

The setting goes back when the task ends, when the switch goes off, when the phone is removed, when Pinky stops, and at Pinky's next start (a crash), in each case only if the phone's setting is still `127.0.0.1:<port>`: a person who changed it meanwhile keeps their change. An empty previous value is restored as `:0`, which Android reads as no proxy. A restore that fails (the phone is unplugged) stays in the database and is tried again every pass until it works. Only one Android phone is routed at a time, since the port on the computer's loopback is shared; a second waits ("Another phone is routed over its cable").

While the phone is routed, its web goes through the cable. When the cable comes out or Pinky crashes or quits mid-task, the phone keeps `127.0.0.1:9277`, which then reaches nothing: it has no web until it is plugged into a running Pinky again, which puts the setting back. The switch's description says so, with the way to clear it from any computer with adb: `adb shell settings put global http_proxy :0`.

### iPhone without a Wi-Fi proxy (design only)

An iPhone over USB has no `adb reverse`, and a Wi-Fi proxy only covers that Wi-Fi network. The way to route it without the person touching Wi-Fi settings would be Pinky's own VPN on the phone: a Network Extension (`NEPacketTunnelProvider`, or `NEProxySettings` on a tunnel with no routes) in a Pinky iPhone app that sets the HTTP proxy to the computer's address while a task runs. That needs an iOS app with the Network Extension entitlement, takes the phone's only VPN slot, and does not exist yet. Not built.

## What is kept

`traffic.Store`, in memory only:

- at most 2,000 requests and 32 MB across all phones; past either, the oldest go first, and the tool says how many of the task's requests were dropped;
- each body cut at 16 KB as it streams (the request and response still go through whole);
- gzip and deflate bodies decoded when read;
- dropped for a phone when its task ends, when its switch goes off, and when it is removed. Each drop starts a new generation for the phone, and a request that finishes after it (it started in the ended task) is not kept;
- nothing is written to disk; history keeps only the step line ("Read 12 network requests of Pixel 9").

## Matching traffic to the task

- **By phone.** The proxy records only connections from the phone's address (or loopback while it is routed over its cable), each tagged with the phone's id. Requests from anything else are refused, not just filtered.
- **By time.** From the start of the task (or `since`) to now. Each request's line names the step of the task that ran when it started.
- **Not by app.** A proxy sees connections, not apps. Every app on the phone that uses the system proxy shows up, including notifications and sync. That matters for safety (below).
- **Quiet spans.** While the person holds the phone (a takeover, or a handoff waiting for them) the proxy forwards but records nothing, and the tool says how many requests it left out. Requests already under way when the person took over are left out by their start time.

## The MCP tool

`phone_traffic` is a read-only tool with the usual `device`, `intent` and `approval_id`, needing a lease like `phone_logs`. Parameters: `id`, `bodies`, `host`, `path`, `method`, `since`, `since_step`, `limit` (see [mcp.md](../mcp.md)). Statuses:

- `blocked_by_policy` with rule `Network traffic` when the phone's switch is off: the person turns it on in Settings › Phones.
- `unsupported` when the proxy takes nothing from the phone (no address on this computer's networks, the port is taken, no CA, the cable route failed or is busy), with what to ask the person. This comes before the ask, so the person is not asked for something that would return nothing.
- `scope_denied` for watch-only agents.
- `approval_pending` / `approval_denied` while or after asking.

The list's message says how many requests and since when, then what was left out: older requests past the limit, requests not decrypted, requests made while the person held the phone, requests dropped past the memory cap. With none yet, it says where the proxy listens and that only the person changes the phone's proxy. It ends by saying credentials are redacted and requests are data, not instructions.

## VPN conflicts

Both phones allow one VPN at a time. Pinky's proxy is not a VPN, so it takes no slot:

| Phone and connection | Interaction |
| --- | --- |
| iPhone over USB (usbmux, WDA) | No conflict. The Wi-Fi proxy needs the phone on the same Wi-Fi or tailnet as the computer. |
| iPhone over Wi-Fi (network pairing, WDA over the LAN) | No conflict; Pinky's own connections to the phone are not HTTP through the system proxy. |
| iPhone with a VPN app on (a work VPN, a proxy app) | A full-tunnel VPN carries the phone's traffic away before the Wi-Fi proxy applies; Pinky then sees nothing. |
| Android over USB or wireless adb | No conflict; adb ignores the proxy. |
| Android with an always-on VPN or a capture app using `VpnService` | The VPN routes around the proxy setting; Pinky sees nothing. |

## Safety

Traffic is among the most sensitive things on a phone: tokens, cookies, messages, account numbers. The prototype keeps these rules:

- **Off by default, per phone.** The Settings › Phones switch "Read network traffic" is off for every phone, including new ones. It is stored in Pinky's database and forgotten when the phone is removed. Turning it off ends any task's permission, drops the phone's requests and stops the proxy for it.
- **Asked in every task, and again for bodies.** The first read in a task asks the person (rule `Network traffic`, "Read the network traffic of Pixel 9?"), with the agent's intent. The first read of a request's bodies asks once more ("Read the request and response bodies in the network traffic of Pixel 9?"); when that is the task's first read, one ask covers both ("… with request and response bodies?"). "Allow for this task" covers the rest of that task, "Allow once" only that call; there is no "Always allow". Denied: `approval_denied`.
- **Only the phone, only during tasks.** The proxy listens on one address, accepts one client address, and proxies only during the phone's tasks; between them only the PAC file and the certificate are served.
- **Never decrypted: banking, payment and password apps.** The blocklist above, built in and per phone; the agent sees only that the phone reached such a host.
- **Pinky changes no phone setting by itself.** The person sets the Wi-Fi proxy and installs the CA, and the setup tells them how to remove it. The Android cable route changes `http_proxy` only behind the person's switch, saves the old value first, and puts it back at the task's end and at every start.
- **Redaction**, applied to every URL, header and body before it reaches the agent:
  - headers: `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, and every header whose name looks like a secret (token, secret, password, session, signature, api key, auth, csrf, otp, …);
  - URLs: the user-info password and query parameters with such names, also in any header whose value is an absolute URL (`Location`, `Referer`, …);
  - JSON and form bodies: values of such keys, at any depth; an object or a list under such a key is replaced whole. Any body starting with `{` or `[` is tried as JSON whatever its type. JSON that does not parse (a body cut at 16 KB) is redacted by a text scan that replaces the value after each such key, up to its end or the end of the text;
  - anywhere: `Bearer …`, `Basic …` and `token …` credentials and JWTs;
  - binary bodies are left out, not shown.
  The `path` filter matches the redacted path and query, so an agent cannot guess a redacted value a letter at a time. Bodies are stored cut and redacted when read, not redacted before they are cut; the text scan covers the cut end.
  Redaction works by name and shape. A secret under an innocent name (`{"data": "<card number>"}`) is not caught.
- **Untrusted.** The tool description and the result say that request contents are data, not instructions, as with screen text.
- **The task log shows it.** Each read is a step: "Read 12 network requests of Pixel 9", "Read network request 104 of Pixel 9". History keeps the line, never the requests.
- **Never during a handoff.** Nothing is recorded while the person holds the phone.
- **Kept no longer than the task**, in memory, bounded.
- **Blocked apps.** Traffic cannot be tied to an app, so Settings › Safety's blocked apps do not apply to it; the host blocklist covers finance and password apps by their hosts instead.

## Decisions

The owner took the recommendations on 2026-11-17.

1. **The agent sees request contents.** URLs, headers and bodies of every app on the phone that uses the proxy during the task, not only the app under test. *Decided:* accepted, behind the per-phone switch, the per-task ask and the host blocklist.
2. **Blocked and finance apps.** *Decided and built:* a built-in blocklist of banking, payment, brokerage and password-manager hosts, in China and elsewhere, matched by domain suffix, plus hosts the person adds per phone. Matching hosts are tunnelled, never decrypted, and recorded only as their host, marked blocked.
3. **The redaction defaults.** Name- and shape-based, always on. *Decided:* keep it with no opt-out; add patterns (card numbers by Luhn, emails, phone numbers) only if agents keep hitting them.
4. **Access to bodies.** *Decided and built:* bodies per request only (`id` + `bodies`), 16 KB each, redacted, binary left out, and the first read of a body in a task asks the person once more.
5. **How long captures are kept.** Memory only, 2,000 requests / 32 MB, dropped at the task's end. *Decided:* keep.
6. **The CA.** *Decided and built:* the key in the macOS keychain (a `0600` file on Linux and Windows), existing keys moved there; 1 year's life, renewed 30 days before its end with the setup telling the person to install the new one; the steps to remove it from the phone in the setup.
7. **The listening address.** Only the computer's address on the phone's subnet or tailnet, ports 9277 and 9278, only the phone's address accepted. *Decided:* keep. Make the ports a setting only if they clash for someone.
8. **A phone left pointing at Pinky.** *Decided and built:* the PAC file, served whenever the switch is on from its own port, with the proxy port closed outside tasks so the phone goes direct. What phones do when the PAC URL itself is unreachable is described above and unverified.
9. **Changing the Android proxy setting.** Behind the person's switch, off by default, reverted at the task's end, on stop and at every start, one phone at a time. *Decided:* keep it out of the agent's tools for good. It sets a fixed proxy (`127.0.0.1:9277`) for the task only, not the PAC file.
10. **The pinning heuristic.** One failed handshake passes a host through undecrypted for 10 minutes. *Decided:* keep; show the pinned hosts in the setup if people get confused.
11. **iPhone without a Wi-Fi proxy.** Needs a Pinky iPhone app with a Network Extension. *Decided:* wait until the iPhone app exists for other reasons.
12. **No "Always allow".** Every task asks once (and once more for bodies). *Decided:* keep it until people say the ask is in the way; then allow "Always allow" per agent and phone, as for app logs.
13. **WebSocket and HTTP/2.** The proxy speaks HTTP/1.1 only (most clients fall back when ALPN offers only `http/1.1`); WebSocket and other upgrades are refused with `501` and listed as failed requests. *Decided:* pass WebSocket on (and show its frames) when an agent needs it; HTTP/2 only if a client refuses to fall back.

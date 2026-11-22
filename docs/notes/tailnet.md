# Tailscale command environment

On macOS, the Tailscale app binary also provides the CLI. With Tailscale
1.104.5, running `status --json` in a minimal desktop environment without
terminal variables printed `The Tailscale GUI failed to start: The operation
couldn’t be completed. (Tailscale.CLIError error 3.)` and exited successfully.
Pinky then reported `tailscale status: invalid character 'T' looking for
beginning of value`. Adding `TERM=dumb` to the same environment returned valid
JSON with `BackendState: Running`.

`runCommand` now supplies `TERM=dumb` on macOS when TERM is unset or empty.
This applies to both status and serve status, preserves an existing TERM and
the inherited environment, and does not change Pinky's own environment or
commands on other operating systems. `TestRunCommandTerminal` checks the
actual child environment without a shell that could supply its own TERM.
Run it with `go test ./internal/tailnet`.

The reproduction was checked against the local Tailscale app on macOS; no
phone was operated. This addresses reading the local Tailscale state, not
pairing or reaching a particular remote phone.

# Tailnet address entered by hand

Some computers reach the tailnet without Tailscale's own app, for example
through a proxy with a Tailscale outbound (sing-box). Pinky then has no
`tailscale status` to read, so **Use away from home** (Settings › Phones)
offers **Enter address…** next to "Needs Tailscale on this computer" for a
connected Android phone. The usual way, with Tailscale on the computer, is
unchanged; this is only the alternative when it is missing.

The driver (`driver.TailnetByHand`, `internal/driver/android/tailnetmanual.go`)
takes the address only when all of these hold:

1. It is an IP address in 100.64.0.0/10 or fd7a:115c:a1e0::/48, with port
   5555 or none (`parseByHand`; `driver.ErrNotTailnetAddr`). The dialog
   checks this as it is typed (`byHandProblem` in `ui/tailnet.go`).
2. It is the phone's own on its VPN (`addrOnPhone`), read over the cable or
   Wi-Fi connection with `ip -o addr show`: on a `tun*` interface, as with
   Tailscale's own app; or on none of the phone's interfaces while a VPN is
   up, as with a VPN app that runs Tailscale in userspace (sing-box's
   Tailscale endpoint gives `tun0` an address of its own, such as
   `172.19.0.1`, and answers at the tailnet address itself; reported in
   #180). A carrier's CGNAT address on `rmnet`, an address when no VPN is up,
   and another device's tailnet address while `tun*` shows the phone's own
   are refused (`driver.ErrAddrNotOnPhone`). In the userspace case nothing
   on the phone names its tailnet address, so check 3 alone tells this
   phone from another device there.
3. What answers there carries Pinky's mark for this phone (`identity.go`):
   the phone is marked first over the cable (or must pass the usual Wi-Fi
   check), and `sameAs` reads the mark back over the new connection without
   ever writing one (`driver.ErrNotThePhone`). It needs Pinky's phone checks
   (`Config.VerifyWiFi`).
4. A TCP probe of port 5555, not Tailscale's view of the phone, says whether
   this computer reaches it, before the adb server is asked to connect
   (`connectProbed`; `driver.ErrAddrUnreachable`). The reconnector does the
   same for a kept phone (`netLink.byHand`).

Any failure disconnects the address and, when adb listened only for this,
runs `usb:` again. The kept phone has `manual: true` in
`network-phones.json`; `driver.Away.Manual` reports it, and **Turn on again**
reuses the address. Pinky can't see how the computer routes traffic to the
address, and adb's own connection is not encrypted, so the dialog and the
row say the connection is only as private as the person's own route to the
tailnet.

Tests: `TestParseByHand`, `TestParseVPNAddrs`, `TestAddrOnPhone`, `TestSetAwayAt*` and
`TestByHandReconnect` in `internal/driver/android`, and `TestAwayByHand` and
`TestByHandProblem` in `ui`. Checked with these tests only, not yet with a
real phone behind a proxy's Tailscale outbound.

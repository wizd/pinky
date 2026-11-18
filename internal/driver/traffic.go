package driver

import (
	"context"
	"net/netip"
)

// NetAddresser is a Provider that knows a phone's addresses on the
// networks it shares with the computer (Wi-Fi, Tailscale), so that
// Pinky's traffic proxy takes connections from that phone only
// (docs/notes/traffic-capture.md). It reads them from the phone's
// connection; an empty list means the phone is on none Pinky knows.
type NetAddresser interface {
	PhoneAddrs(ctx context.Context, id string) ([]netip.Addr, error)
}

// ProxyRouter is a Provider that can point a phone's HTTP proxy at the
// computer through its USB connection: Android's adb reverse plus its
// global http_proxy setting. Only the person turns this on; the gateway
// puts the setting back when the task ends, when the phone is released
// or removed, when Pinky stops, and at the next start after a crash.
type ProxyRouter interface {
	// ProxySetting returns the phone's HTTP proxy setting as it is now,
	// "" when there is none.
	ProxySetting(ctx context.Context, id string) (string, error)
	// RouteProxy has the phone's port reach the computer's port, and sets
	// the phone's HTTP proxy to it.
	RouteProxy(ctx context.Context, id string, port int) error
	// UnrouteProxy puts prev back (from ProxySetting before RouteProxy)
	// when the phone's setting is still the one RouteProxy made, and
	// removes the reverse port.
	UnrouteProxy(ctx context.Context, id string, port int, prev string) error
}

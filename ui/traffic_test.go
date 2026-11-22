package ui

import (
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/gateway"
)

// A taken port is named with the address and what it keeps from working;
// the route's own port is shown while the phone is routed over another.
func TestTrafficProblemNamesThePort(t *testing.T) {
	got := trafficProblem(gateway.TrafficSetup{Problem: gateway.TrafficListenFailed, ProblemAddr: "192.168.1.5:9278"}, "Pixel 9")
	if !strings.Contains(got, "port 9278 at 192.168.1.5") || !strings.Contains(got, "Pixel 9's Wi-Fi proxy") {
		t.Errorf("listen failed: %q", got)
	}
	got = trafficProblem(gateway.TrafficSetup{Problem: gateway.TrafficRouteListenFailed}, "Pixel 9")
	if !strings.Contains(got, "127.0.0.1") || !strings.Contains(got, "cable") || !strings.Contains(got, "Wi-Fi proxy isn't affected") {
		t.Errorf("route listen failed: %q", got)
	}
	if p := trafficPortOf(gateway.TrafficSetup{Routed: true, RoutePort: 51234, Proxies: []string{"192.168.1.5:9277"}}); p != 51234 {
		t.Errorf("routed port %d", p)
	}
	if p := trafficPortOf(gateway.TrafficSetup{Proxies: []string{"192.168.1.5:9277"}}); p != 9277 {
		t.Errorf("proxy port %d", p)
	}
}

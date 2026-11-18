package history

import (
	"context"
	"slices"
	"testing"
)

func TestTrafficDevices(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if on, route, err := s.TrafficDevices(ctx); err != nil || len(on) != 0 || len(route) != 0 {
		t.Fatalf("new store: %v, %v, %v", on, route, err)
	}
	for _, id := range []string{"pixel", "iphone"} {
		if err := s.SetDeviceTraffic(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	// Turning it on twice keeps one row (and its route); off removes it.
	if err := s.SetDeviceTrafficRoute(ctx, "pixel", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeviceTraffic(ctx, "pixel", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeviceTraffic(ctx, "iphone", false); err != nil {
		t.Fatal(err)
	}
	on, route, err := s.TrafficDevices(ctx)
	if err != nil || len(on) != 1 || !on["pixel"] || len(route) != 1 || !route["pixel"] {
		t.Fatalf("on = %v, route = %v, %v; want only the Pixel", on, route, err)
	}
	// The route switch goes with the reading switch.
	if err := s.SetDeviceTraffic(ctx, "pixel", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeviceTraffic(ctx, "pixel", true); err != nil {
		t.Fatal(err)
	}
	if _, route, _ := s.TrafficDevices(ctx); len(route) != 0 {
		t.Fatalf("route kept after off: %v", route)
	}
	// Removing the phone forgets it.
	if err := s.ForgetDevice(ctx, "pixel"); err != nil {
		t.Fatal(err)
	}
	if on, _, err := s.TrafficDevices(ctx); err != nil || len(on) != 0 {
		t.Fatalf("after ForgetDevice: %v, %v", on, err)
	}
}

// Routes outlive the phone's settings until their setting is put back.
func TestTrafficRoutes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTrafficRoute(ctx, TrafficRoute{DeviceID: "pixel", Prev: "10.0.0.8:3128", Port: 9277}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTrafficRoute(ctx, TrafficRoute{DeviceID: "pixel", Prev: "", Port: 9278}); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetDevice(ctx, "pixel"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.TrafficRoutes(ctx)
	if err != nil || !slices.Equal(got, []TrafficRoute{{DeviceID: "pixel", Prev: "", Port: 9278}}) {
		t.Fatalf("routes %v, %v", got, err)
	}
	if err := s.DeleteTrafficRoute(ctx, "pixel"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.TrafficRoutes(ctx); len(got) != 0 {
		t.Fatalf("after delete: %v", got)
	}
}

package ios

import (
	"net/netip"
	"slices"
	"testing"

	"howett.net/plist"
)

func TestMuxAddrs(t *testing.T) {
	type props struct {
		SerialNumber   string
		ConnectionType string
		NetworkAddress []byte `plist:",omitempty"`
	}
	type entry struct{ Properties props }
	v4 := []byte{16, 2, 0, 0, 192, 168, 1, 23, 0, 0, 0, 0, 0, 0, 0, 0}
	v6 := make([]byte, 28)
	v6[0], v6[1] = 28, 30
	copy(v6[8:24], netip.MustParseAddr("fd7a:115c:a1e0::5").AsSlice())
	raw, err := plist.Marshal(map[string]any{"DeviceList": []entry{
		{props{SerialNumber: "00008110-AAA", ConnectionType: "USB"}},
		{props{SerialNumber: "00008110-AAA", ConnectionType: "Network", NetworkAddress: v4}},
		{props{SerialNumber: "00008110-AAA", ConnectionType: "Network", NetworkAddress: v6}},
		{props{SerialNumber: "00008110-BBB", ConnectionType: "Network", NetworkAddress: []byte{16, 2, 0, 0, 10, 0, 0, 9, 0, 0, 0, 0, 0, 0, 0, 0}}},
	}}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	got, err := muxAddrs(raw, "00008110-AAA")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr("192.168.1.23"), netip.MustParseAddr("fd7a:115c:a1e0::5")}
	if !slices.Equal(got, want) {
		t.Fatalf("addresses %v", got)
	}
	if got, _ := muxAddrs(raw, "00008110-CCC"); len(got) != 0 {
		t.Errorf("a phone not listed has %v", got)
	}
	if _, ok := sockaddrAddr([]byte{16, 2, 0}); ok {
		t.Error("a short sockaddr read")
	}
}

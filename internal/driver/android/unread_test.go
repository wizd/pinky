package android

import (
	"context"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

// TestUnreadWiFiConnection: a connection over the network whose phone does
// not answer when List asks what it is, as a Galaxy A12 whose Wi-Fi sleeps
// while it charges on its cable, is no phone of its own. It was listed
// online under its adb serial, an address or a name, next to the phone on
// its cable; the gateway opened it under that serial and kept it, one more
// phone for each port Wireless debugging took.
func TestUnreadWiFiConnection(t *testing.T) {
	const (
		serial = "R58M30ABCDE"
		guid   = "adb-" + serial + "-kIY2HE"
		named  = guid + connectSuffix
		addr   = "192.168.1.5:37317"
		props  = "SM-A127F\n13\n33\nGalaxy A12\n" + serial + "\npinky-boot=" + serial + "\npinky-booted=1\n"
		cable  = serial + "\tdevice usb:1-1 product:a12s model:SM_A127F device:a12s transport_id:1"
	)
	wifi := func(serial, state string) string {
		return serial + "\t" + state + " product:a12s model:SM_A127F device:a12s transport_id:2"
	}
	type phone struct {
		id, conn string
		state    driver.State
		detail   string
	}
	onCable := phone{serial, "usb", driver.StateOnline, ""}
	for _, tc := range []struct {
		name    string
		devices []string
		kept    bool // addr is where the paired phone was last connected
		want    []phone
	}{
		{"a cable and the phone's kept address", []string{cable, wifi(addr, "device")}, true, []phone{onCable}},
		{"a cable and the discovery's connection", []string{cable, wifi(named, "device")}, false, []phone{onCable}},
		{"a cable and an address nobody knows", []string{cable, wifi(addr, "device")}, false, []phone{onCable}},
		{"a cable and a connection adb lists as offline", []string{wifi(named, "offline"), cable}, false, []phone{onCable}},
		{"the phone's kept address alone", []string{wifi(addr, "device")}, true,
			[]phone{{serial, "wifi", driver.StateOffline, unreadDetail}}},
		{"the discovery's connection alone", []string{wifi(named, "device")}, false,
			[]phone{{serial, "wifi", driver.StateOffline, unreadDetail}}},
		{"an address nobody knows alone", []string{wifi(addr, "device")}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, p := newFakeADB(t)
			if tc.kept {
				if err := p.net.setPairedAddr(guid, addr); err != nil {
					t.Fatal(err)
				}
			}
			// Only the cable's connection answers.
			f.set(func(f *fakeADB) {
				f.devices = tc.devices
				f.props = map[string]string{serial: props}
			})
			infos, err := p.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(infos) != len(tc.want) {
				t.Fatalf("listed %+v, want %+v", infos, tc.want)
			}
			for i, w := range tc.want {
				if in := infos[i]; in.ID != w.id || in.Connection != w.conn || in.State != w.state || (w.detail != "" && in.Detail != w.detail) {
					t.Errorf("listed %+v, want %+v", in, w)
				}
			}
			// Once the phone answers there, the connection is the phone's.
			f.set(func(f *fakeADB) {
				f.props[addr], f.props[named] = props, props
			})
			infos, err = p.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(infos) != 1 || infos[0].ID != serial || infos[0].State != driver.StateOnline || infos[0].Name != "Galaxy A12" {
				t.Errorf("once it answers: listed %+v, want the Galaxy A12 online as %s", infos, serial)
			}
		})
	}
}

// TestUnreadWiFiConnectionNotOnline: a connection at an address that adb
// does not list as online, and that the driver cannot place, stays listed
// by its address with what adb says of it: the person may have to allow
// this computer on the phone.
func TestUnreadWiFiConnectionNotOnline(t *testing.T) {
	f, p := newFakeADB(t)
	f.set(func(f *fakeADB) {
		f.devices = []string{"192.168.1.5:5555\tunauthorized transport_id:3"}
	})
	infos, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != "192.168.1.5:5555" || infos[0].State != driver.StateUnauthorized {
		t.Errorf("listed %+v, want the address, unauthorized", infos)
	}
}

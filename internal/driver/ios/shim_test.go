package ios

import (
	"net"
	"strings"
	"testing"

	goios "github.com/danielpaulus/go-ios/ios"
)

// The installation proxy answers a Browse in chunks until one says
// Complete; an error in a chunk ends the read.
func TestBrowseAppInfos(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []map[string]any
		want   []string
		err    string
	}{
		{name: "chunks", chunks: []map[string]any{
			{"Status": "BrowsingApplications", "CurrentList": []any{map[string]any{"CFBundleIdentifier": "com.apple.calculator"}}},
			{"Status": "BrowsingApplications", "CurrentList": []any{map[string]any{"CFBundleIdentifier": "cc.diygod.pinky"}}},
			{"Status": "Complete"},
		}, want: []string{"com.apple.calculator", "cc.diygod.pinky"}},
		{name: "error", chunks: []map[string]any{{"Error": "APIInternalError"}}, err: "APIInternalError"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			phone, us := net.Pipe()
			defer us.Close()
			go func() {
				defer phone.Close()
				codec := goios.NewPlistCodecReadWriter(phone, phone)
				var req map[string]any
				if err := codec.Read(&req); err != nil || req["Command"] != "Browse" {
					return
				}
				// Only the keys the app list reads: every key is megabytes.
				opts, _ := req["ClientOptions"].(map[string]any)
				attrs, _ := opts["ReturnAttributes"].([]any)
				if len(attrs) == 0 || attrs[0] != "CFBundleIdentifier" {
					return
				}
				for _, c := range tc.chunks {
					if codec.Write(c) != nil {
						return
					}
				}
			}()
			infos, err := browseAppInfos(us, us)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, a := range infos {
				got = append(got, a.CFBundleIdentifier())
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("apps = %v, want %v", got, tc.want)
			}
		})
	}
}

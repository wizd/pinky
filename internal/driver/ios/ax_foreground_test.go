package ios

import "testing"

func TestInspectorForegroundIgnoresWebContent(t *testing.T) {
	const host = "com.example.browser"
	for _, web := range []string{
		"com.apple.WebKit.WebContent",
		"com.apple.WebKit.WebContent.EnhancedSecurity",
	} {
		for _, hostState := range []uint64{appForeground, 4} {
			t.Run(web+"/"+map[uint64]string{appForeground: "host foreground", 4: "host background"}[hostState], func(t *testing.T) {
				a := &axSession{apps: map[string]*axApp{
					host: {id: host, name: "Browser", state: hostState, seq: 1},
					web:  {id: web, name: web, state: appForeground, seq: 2},
				}}
				name, id := a.foreground()
				wantName, wantID := "Home Screen", springBoard
				if hostState == appForeground {
					wantName, wantID = "Browser", host
				}
				if name != wantName || id != wantID {
					t.Fatalf("foreground() = (%q, %q), want (%q, %q)", name, id, wantName, wantID)
				}
			})
		}
	}
}

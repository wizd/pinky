//go:build device

package ios

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRunnerHostProbe starts WebDriverAgent's test with an ordinary app as
// its test host: the app PINKY_RUNNERHOST names, which carries
// WebDriverAgentRunner.xctest in its PlugIns and is not an .xctrunner. It
// answers whether Pinky's iPhone app can be the helper too
// (docs/notes/iphone-app.md, "Pinky Helper inside the app"). The runner's
// log goes to stderr.
//
//	PINKY_DEVICE=<udid> PINKY_RUNNERHOST=<bundle id> go test -tags device -run TestRunnerHostProbe -v ./internal/driver/ios
func TestRunnerHostProbe(t *testing.T) {
	host := os.Getenv("PINKY_RUNNERHOST")
	if host == "" {
		t.Skip("PINKY_RUNNERHOST not set")
	}
	prev := wdaLog
	wdaLog = os.Stderr
	defer func() { wdaLog = prev }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	base, err := findEntry(os.Getenv("PINKY_DEVICE"), false)
	if err != nil {
		t.Fatal(err)
	}
	l, err := dialLinkEntry(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	bind := wdaBind(base, l.tunnel)
	c := newWDAUSB(base)
	if bind != "127.0.0.1" {
		c = newWDATunnel(l.tunnel)
	}
	run := startWDA(l.entry, base, wdaRunner{BundleID: host}, bind)
	defer run.stop()
	start := time.Now()
	for time.Since(start) < wdaStartTimeout {
		if alive, err := run.alive(); !alive {
			t.Fatalf("the test ended after %s: %v", time.Since(start).Round(time.Millisecond), err)
		}
		sctx, scancel := context.WithTimeout(ctx, 2*time.Second)
		st, err := c.status(sctx)
		scancel()
		if err == nil && st.Ready {
			t.Logf("WebDriverAgent ready in %s, hosted by %s: %+v", time.Since(start).Round(time.Millisecond), host, st)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("WebDriverAgent did not answer within %s", wdaStartTimeout)
}

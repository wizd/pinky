package ios

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// The links in these tests have no tunnel (no RSD), so every connection
// fails at once in go-ios, without reaching usbmuxd or a device.

// On a computer a failed remoteAXService is remembered for
// raxRecheckFailures, and Observe uses the inspector meanwhile. On the phone
// itself (OpenOverTunnel) nothing else can read the screen: every read asks
// the service again.
func TestRAXReadyRetriesOnPhone(t *testing.T) {
	cached := errors.New("remote accessibility: no answer")
	for _, tc := range []struct {
		name    string
		onPhone bool
	}{
		{"computer", false},
		{"phone itself", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Device{l: &link{tunnelOnly: tc.onPhone}, log: discard, raxErr: cached, raxFailedAt: time.Now()}
			_, err := d.raxReady(context.Background())
			switch {
			case err == nil:
				t.Fatal("connected without a tunnel")
			case !tc.onPhone && err != cached:
				t.Fatalf("err = %v, want the remembered failure", err)
			case tc.onPhone && err == cached:
				t.Fatal("returned the remembered failure instead of asking again")
			case tc.onPhone && d.raxErr != err:
				t.Errorf("raxErr = %v, want the new failure %v", d.raxErr, err)
			}
		})
	}
}

// On the phone itself the inspector is out of reach (it goes through
// lockdown over usbmuxd): a read whose remoteAXService failed says why
// instead of falling back.
func TestObserveOnPhoneDoesNotFallBack(t *testing.T) {
	d := &Device{l: &link{tunnelOnly: true}, log: discard}
	s, st, err := d.ObserveWithStats(context.Background(), driver.ObserveOptions{})
	if err == nil {
		t.Fatalf("read a screen without a tunnel: %+v", s)
	}
	if st.Source == "inspector" {
		t.Fatalf("fell back to the inspector: %v", err)
	}
	if st.RAXError == nil || !errors.Is(err, st.RAXError) {
		t.Errorf("err = %v, want it to wrap remoteAXService's failure %v", err, st.RAXError)
	}
	if !strings.Contains(err.Error(), "reading the screen") {
		t.Errorf("err = %v, want it to say the screen could not be read", err)
	}
}

func TestRAXAnswerWaits(t *testing.T) {
	// The phone itself waits longer for the first answer, and a read there
	// may wait for the warm-up's dial and then dial again: both must fit in
	// the gateway's call budget (45 s, gateway.Options.CallBudget).
	if raxAnswerWaitTunnel <= raxAnswerWait || 2*raxAnswerWaitTunnel >= 45*time.Second {
		t.Errorf("raxAnswerWait %s, raxAnswerWaitTunnel %s", raxAnswerWait, raxAnswerWaitTunnel)
	}
}

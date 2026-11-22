package gateway

import (
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// An app that comes to the front by itself. Android drivers report the
// person's touches on the phone itself (driver.PersonWatcher), and those
// take over. An iPhone reports none, so a person who opens another app on
// it mid-task went unnoticed: the agent's next read came back settled with
// that app in front (GitHub issue #181). The gateway cannot see the
// person, but it knows every input it sent and every call that may have
// changed the screen, so it can tell an app in front that none of them
// explains.
//
// It does not take over for that, as a touch does: a call, an alarm, a
// link that opened its app late, or an app that quit show the same thing,
// and pausing the agent on each would stop tasks that should go on. The
// read says so in its message instead (settled, with a note), and an
// action aimed at the screen the agent saw is not sent (stale): whoever
// brought it up, the app it aimed at is no longer in front.

// inputMark is what the gateway knew of input when it gave the agent a
// screen: the device's input count and the lease's count of calls.
type inputMark struct {
	inputs uint64
	calls  uint64
}

// switchedApp reports whether the app in front on cur, read at readAt,
// differs from the one on the screen the agent last received, with no input
// and no call since that Pinky knows could have brought it up: no input to
// the phone (the agent's, the person's on the mirror, an unlock), no call
// that may change the screen (own allows one, the call reading cur), no
// other call in flight, and none of that within Options.SwitchGrace. It
// names the two apps as the agent sees them (a blocked app is "blocked
// app"). Locked screens, screens that name no app, background displays and
// a phone the person controls never count.
func (g *Gateway) switchedApp(l *lease, cur *screen.Screen, readAt time.Time, own bool) (from, to string, ok bool) {
	if cur == nil || cur.Locked || noApp(cur) {
		return "", "", false
	}
	view, _ := g.agentView(l, cur)
	g.mu.Lock()
	defer g.mu.Unlock()
	d := l.dev
	prev := l.baseline
	if prev == nil || prev.Locked || noApp(prev) || l.bg != nil || d.person || l.inflight != 1 {
		return "", "", false
	}
	calls := l.calls - l.mark.calls
	if own && calls > 0 {
		calls--
	}
	if calls != 0 || d.inputs != l.mark.inputs || (g.opt.SwitchGrace > 0 && readAt.Sub(d.inputAt) < g.opt.SwitchGrace) {
		return "", "", false
	}
	from, to = appLabel(prev.App), appLabel(view.App)
	if from == "" || to == "" || sameApp(prev.App, view.App) {
		return "", "", false
	}
	return from, to, true
}

// aimed reports whether an action depends on the screen the agent saw:
// all but launching an app and pressing home.
func aimed(a Action) bool {
	return a.Tool != "launch" && !(a.Tool == "key" && a.Key == "home")
}

// switchedCause is what may have brought another app to the front.
const switchedCause = "the person may be using the phone, or a call, an alarm or a link that opened late brought it up"

// switchedNote goes with a read whose app came to the front by itself.
func switchedNote(from, to string) string {
	return fmt.Sprintf("%s is in front now instead of %s, and none of your calls opened it: %s. "+
		"If you did not expect it, do not act in it: call phone_handoff to ask the person whether you may go on.", to, from, switchedCause)
}

// switchedStale is the message of an action refused because its app was
// no longer in front.
func switchedStale(from, to string) string {
	return fmt.Sprintf("%s is in front now instead of %s, and none of your calls opened it, so the action was not sent. Nothing was done. "+
		"%s. Look at the screen below; if you did not expect it, call phone_handoff to ask the person whether you may go on.",
		to, from, capitalize(switchedCause))
}

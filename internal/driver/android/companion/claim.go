package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"
)

// FeatureClaim means the app takes claims (Subscription.Claim) and sends
// claim events (EventClaim): who drives the phone, Pinky on a computer or
// Pinky on the phone. An app without it answers claim with
// unknown_command.
const FeatureClaim = "claim"

// Who holds a claim (Claim.By).
const (
	ByComputer = "computer" // Pinky on a computer, through adb
	ByPhone    = "phone"    // Pinky on the phone: the app's own core
)

// CodeBusy is claim's error while another client holds the phone; the
// Error's Claim says who.
const CodeBusy = "busy"

// Claim is who drives the phone.
type Claim struct {
	By   string `json:"by,omitempty"`   // ByComputer or ByPhone
	Task string `json:"task,omitempty"` // the task it named, if any
	// Implicit is a desktop Pinky that does not claim: the app counts the
	// event subscription it holds while its agent works as its claim. It
	// has no task and cannot be stopped from the phone.
	Implicit bool `json:"implicit,omitempty"`
}

// requestWait bounds how long a request on a subscription waits for its
// reply.
const requestWait = 10 * time.Second

// Claim gives the phone to this subscription's connection for task: by is
// ByComputer from a computer and ByPhone from the app's own core, as the
// app checks. The claim holds until Release or until the subscription
// ends. While another client holds the phone the error is an *Error with
// CodeBusy, whose Claim says who. Subscribe to EventClaim on the same
// subscription to hear that Pinky on the phone stopped the claim
// (Event.Stopped). Needs FeatureClaim.
func (s *Subscription) Claim(ctx context.Context, by, task string) error {
	return s.request(ctx, "claim", map[string]any{"by": by, "task": task}, nil)
}

// Release ends this subscription's claim, if it holds one.
func (s *Subscription) Release(ctx context.Context) error {
	return s.request(ctx, "claim", map[string]any{"release": true}, nil)
}

// StopClaim ends a computer's claim, for the app's own core (Stop on the
// phone's pill); the computer hears it as a claim event with Stopped. It
// reports whether a claim ended: an implicit one cannot be stopped.
func (s *Subscription) StopClaim(ctx context.Context) (bool, error) {
	var r struct {
		Stopped bool `json:"stopped"`
	}
	if err := s.request(ctx, "claim", map[string]any{"stop": true}, &r); err != nil {
		return false, err
	}
	return r.Stopped, nil
}

// request sends a command on the subscription's connection and waits for
// its reply, which the reader hands over. The reply comes after the events
// sent before it, so whoever reads C must go on reading meanwhile.
func (s *Subscription) request(ctx context.Context, cmd string, params map[string]any, out any) error {
	req := map[string]any{"cmd": cmd}
	maps.Copy(req, params)
	ch := make(chan response, 1)
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()
	req["id"] = id
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}

	s.wmu.Lock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(requestWait))
	_, err = s.conn.Write(append(line, '\n'))
	_ = s.conn.SetWriteDeadline(time.Time{})
	s.wmu.Unlock()
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrNotRunning, cmd, err)
	}

	timer := time.NewTimer(requestWait)
	defer timer.Stop()
	select {
	case resp := <-ch:
		return resp.into(cmd, out)
	case <-s.done:
		// The reply may have come just before the end.
		select {
		case resp := <-ch:
			return resp.into(cmd, out)
		default:
		}
		return fmt.Errorf("%w: %s: the subscription ended", ErrNotRunning, cmd)
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("companion: %s: no reply in %v", cmd, requestWait)
	}
}

// replied hands a reply to the request waiting for it.
func (s *Subscription) replied(r response) {
	s.mu.Lock()
	ch := s.pending[r.ID]
	s.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- r:
	default:
	}
}

// into returns the reply's error, or decodes its result into out.
func (r response) into(cmd string, out any) error {
	if !r.OK {
		if r.Error != nil {
			return r.Error
		}
		return fmt.Errorf("companion: %s failed", cmd)
	}
	if out == nil || len(r.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Result, out); err != nil {
		return fmt.Errorf("companion: bad %s reply: %w", cmd, err)
	}
	return nil
}

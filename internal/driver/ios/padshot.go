package ios

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The passcode pad in a screenshot. Unlock looks for the pad through
// remoteAXService (keypadShown). When that service stops answering, the
// pad is looked for in a screenshot instead: on 2026-11-17 an iPhone 14
// Pro's remoteAXService left every new connection unanswered for minutes
// (deviceAPIVersion timed out), on the lock screen and across restarts of
// Pinky, while screenshots and input worked. Every unlock then gave up
// with ErrPINNotEntered, though the pad was showing.
//
// The screenshot comes from the screencapture service, never from the
// live video: the video leaves the pad's keys out (the person watching
// sees only the passcode dots), while the screenshot has them all.

// padShooter is a powerPhone that can also look for the pad in a
// screenshot; reachPad uses it when the accessibility look fails.
type padShooter interface {
	padInScreenshot(ctx context.Context) (bool, error)
}

// padLookWait bounds one look at a screenshot: taking it (about 0.5 s over
// USB) and reading it with OCR (about 0.1 s). Vision once took 28 s on a
// screenshot of the lock screen, and did not again; a read that runs out
// of time counts as no pad, and the next look waits for it to finish
// rather than start another.
const padLookWait = 4 * time.Second

var errPadReadBusy = errors.New("an earlier read of a screenshot is still running")

// padInScreenshot implements padShooter.
func (p devicePower) padInScreenshot(ctx context.Context) (bool, error) {
	d := p.d
	c, cancel := context.WithTimeout(ctx, padLookWait)
	defer cancel()
	png, err := d.ScreenshotPNG(c)
	if err != nil {
		return false, fmt.Errorf("screenshot: %w", err)
	}
	if !d.padReadMu.TryLock() {
		return false, errPadReadBusy
	}
	type result struct {
		lines []ocrLine
		err   error
	}
	done := make(chan result, 1)
	go func() {
		defer d.padReadMu.Unlock()
		lines, err := recognizeText(png, ocrOptions{Languages: ocrLanguages})
		done <- result{lines, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return false, fmt.Errorf("reading the screenshot: %w", r.err)
		}
		found := padInText(r.lines)
		if found {
			d.log.Info("iphone: passcode pad found in a screenshot", "device", d.info.ID)
		}
		return found, nil
	case <-c.Done():
		return false, fmt.Errorf("reading the screenshot took over %s", padLookWait)
	}
}

// padInText reports whether OCR found the digit keys 1 to 9, each on its
// own. 0 is left out: Vision missed it on an iPhone 14 Pro's pad. The
// digits do not depend on the phone's language, and a lock screen without
// the pad (a clock, a date, notifications) does not show all nine alone.
func padInText(lines []ocrLine) bool {
	var seen [10]bool
	for _, l := range lines {
		if t := strings.TrimSpace(l.Text); len(t) == 1 && t[0] >= '1' && t[0] <= '9' {
			seen[t[0]-'0'] = true
		}
	}
	for _, ok := range seen[1:] {
		if !ok {
			return false
		}
	}
	return true
}

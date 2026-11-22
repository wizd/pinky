package ios

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestNoteRead(t *testing.T) {
	var buf bytes.Buffer
	d := &Device{log: slog.New(slog.NewTextHandler(&buf, nil))}
	d.info.ID = "phone"
	ctx := context.Background()
	quick := ObserveStats{Source: "remoteax", Elements: 12, Complete: true}

	d.noteRead(ctx, 400*time.Millisecond, quick, nil)
	if buf.Len() != 0 {
		t.Errorf("a quick read was logged: %s", buf.String())
	}

	slow := quick
	slow.Trace = []string{"0s input 1.2s ago, trusted true", "4.1s walk: 12 elements, err <nil>"}
	d.noteRead(ctx, 5*time.Second, slow, nil)
	if got := buf.String(); !strings.Contains(got, "total=5s") || !strings.Contains(got, "walk: 12 elements") {
		t.Errorf("slow read logged as %q; want its total and trace", got)
	}

	buf.Reset()
	failed := ObserveStats{Source: "inspector", RAXError: errors.New("remote accessibility: no answer within 5s")}
	d.noteRead(ctx, time.Second, failed, nil)
	if got := buf.String(); !strings.Contains(got, "remoteax_err") || !strings.Contains(got, "no answer within 5s") {
		t.Errorf("a read without remoteAXService logged as %q; want why", got)
	}

	buf.Reset()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	d.noteRead(cancelled, 10*time.Second, failed, context.Canceled)
	if buf.Len() != 0 {
		t.Errorf("a read the caller gave up on was logged: %s", buf.String())
	}
}

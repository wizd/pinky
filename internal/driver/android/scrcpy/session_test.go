package scrcpy

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestSessionWithFakePhone(t *testing.T) {
	f, c := newFakePhone(t)
	f.failFirst = 2 // the server is not listening yet
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Start(ctx, c, "phone", Options{MaxSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.DeviceName != "Fake Phone" || s.Codec != CodecH264 {
		t.Errorf("header: name %q codec %#x", s.DeviceName, s.Codec)
	}
	f.mu.Lock()
	cmd, pushed := f.command, f.pushed
	f.mu.Unlock()
	if !strings.Contains(cmd, "app_process / com.genymobile.scrcpy.Server 5.0.1 scid=") || !strings.Contains(cmd, "max_size=1024") {
		t.Errorf("command = %q", cmd)
	}
	if pushed != len(ServerJar()) {
		t.Errorf("pushed %d bytes, want %d", pushed, len(ServerJar()))
	}

	p, err := s.ReadPacket()
	if err != nil || !p.Session || p.Width != 1080 {
		t.Fatalf("session = %+v, %v", p, err)
	}
	if p, _ = s.ReadPacket(); !p.Config {
		t.Fatalf("config = %+v", p)
	}
	if p, _ = s.ReadPacket(); !p.KeyFrame || p.PTS != time.Millisecond {
		t.Fatalf("key frame = %+v", p)
	}

	if err := s.Key(66, 0); err != nil {
		t.Fatal(err)
	}
	text, err := s.Clipboard(ctx)
	if err != nil || text != "copied" {
		t.Fatalf("clipboard = %q, %v", text, err)
	}
	if err := s.SetClipboard(ctx, "\u4f60\u597d", true); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.clipboard != "\u4f60\u597d" {
		t.Errorf("phone clipboard = %q", f.clipboard)
	}
	keys := append([]byte(nil), f.control...)
	f.mu.Unlock()
	if len(keys) != 28 || keys[1] != ActionDown || keys[15] != ActionUp {
		t.Errorf("key messages = %x", keys)
	}

	app, err := s.StartApp(ctx, "com.android.calculator2")
	if err != nil || app.Package != "com.android.calculator2" || app.Name != "Calculator" || app.Display != 0 {
		t.Fatalf("start app = %+v, %v", app, err)
	}
	if _, err := s.StartApp(ctx, "com.example.none"); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("missing app = %v", err)
	}

	s.Close()
	if err := s.Send(ResetVideo()); err == nil {
		t.Error("send after close succeeded")
	}
	if !errors.Is(s.Err(), ErrClosed) {
		t.Errorf("err after close = %v", s.Err())
	}
}

func TestListAppsWithFakePhone(t *testing.T) {
	_, c := newFakePhone(t)
	apps, err := ListApps(context.Background(), c, "phone")
	if err != nil || len(apps) != 1 || apps[0].Package != "com.android.calculator2" || !apps[0].System {
		t.Fatalf("apps = %+v, %v", apps, err)
	}
}

func TestStartFailsWhenServerExits(t *testing.T) {
	f, c := newFakePhone(t)
	f.failFirst = 1 << 30 // the server never listens
	go func() {
		// The server prints an error and exits.
		for {
			time.Sleep(20 * time.Millisecond)
			f.mu.Lock()
			sh := f.shell
			f.mu.Unlock()
			if sh != nil {
				io.WriteString(sh, "[server] ERROR: Could not create display\n")
				sh.Close()
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Start(ctx, c, "phone", Options{})
	if err == nil || !strings.Contains(err.Error(), "Could not create display") {
		t.Fatalf("err = %v", err)
	}
}

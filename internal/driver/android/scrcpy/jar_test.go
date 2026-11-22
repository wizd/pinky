package scrcpy

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func startClose(t *testing.T, ctx context.Context, f *fakePhone, opt Options) {
	t.Helper()
	s, err := Start(ctx, f.client(), "phone", opt)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func (f *fakePhone) state() (pushes []string, files map[string]int64, noJar []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.pushes), maps.Clone(f.files), slices.Clone(f.noJar)
}

func TestJarPushedOncePerPhone(t *testing.T) {
	f, _ := newFakePhone(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startClose(t, ctx, f, Options{})
	startClose(t, ctx, f, Options{NoVideo: true})
	if _, err := ListApps(ctx, f.client(), "phone"); err != nil {
		t.Fatal(err)
	}
	pushes, files, noJar := f.state()
	if len(pushes) != 1 || !strings.HasPrefix(pushes[0], sharedJarPath()+".") {
		t.Errorf("pushes = %q, want one to a temporary name", pushes)
	}
	if len(noJar) > 0 {
		t.Errorf("servers found no jar at %q", noJar)
	}
	// Only the shared jar stays: each run's link is gone.
	want := map[string]int64{sharedJarPath(): int64(len(ServerJar()))}
	if !maps.Equal(files, want) {
		t.Errorf("files = %v, want %v", files, want)
	}
}

func TestJarOfWrongSizePushedAgain(t *testing.T) {
	f, _ := newFakePhone(t)
	f.files[sharedJarPath()] = 1000 // cut short by something else
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startClose(t, ctx, f, Options{})
	pushes, files, _ := f.state()
	if len(pushes) != 1 {
		t.Errorf("pushes = %q", pushes)
	}
	if files[sharedJarPath()] != int64(len(ServerJar())) {
		t.Errorf("shared jar has %d bytes", files[sharedJarPath()])
	}
}

func TestStaleJarsRemoved(t *testing.T) {
	f, _ := newFakePhone(t)
	old := jarDir + "/pinky-scrcpy-1a2b3c4d.jar"                // a random name from before
	other := jarDir + "/pinky-scrcpy-4.0-0123456789ab.jar"      // another version's
	mine := jarDir + "/" + jarPrefix + jarTag + "-0badf00d.jar" // a run of this version
	unrelated := jarDir + "/scrcpy-server.jar"
	for _, p := range []string{old, other, mine, unrelated} {
		f.files[p] = 5
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startClose(t, ctx, f, Options{})
	_, files, _ := f.state()
	for _, p := range []string{old, other} {
		if _, ok := files[p]; ok {
			t.Errorf("%s stayed", p)
		}
	}
	for _, p := range []string{mine, unrelated} {
		if _, ok := files[p]; !ok {
			t.Errorf("%s was removed", p)
		}
	}
}

func TestConcurrentSessionsShareOnePush(t *testing.T) {
	f, _ := newFakePhone(t)
	f.pushDelay = 2 * time.Millisecond // all starts wait on the push
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const n = 6
	sessions := make([]*Session, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			sessions[i], errs[i] = Start(ctx, f.client(), "phone", Options{NoVideo: true})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	pushes, files, noJar := f.state()
	if len(pushes) != 1 {
		t.Errorf("pushes = %q, want 1", pushes)
	}
	if len(noJar) > 0 {
		t.Errorf("servers found no jar at %q", noJar)
	}
	// Each session ends on its own while the others run.
	for i, s := range sessions {
		if err := s.Send(ResetVideo()); err != nil {
			t.Errorf("session %d: %v", i, err)
		}
		s.Close()
		startClose(t, ctx, f, Options{NoVideo: true})
	}
	pushes, files, noJar = f.state()
	if len(pushes) != 1 || len(noJar) > 0 {
		t.Errorf("pushes = %q, no jar at %q", pushes, noJar)
	}
	want := map[string]int64{sharedJarPath(): int64(len(ServerJar()))}
	if !maps.Equal(files, want) {
		t.Errorf("files = %v, want %v", files, want)
	}
}

func TestJarPushOutlivesStart(t *testing.T) {
	f, _ := newFakePhone(t)
	f.pushDelay = 20 * time.Millisecond // 12 chunks: about 240 ms
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Start(ctx, f.client(), "phone", Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("start = %v, want a deadline", err)
	}
	// The push goes on; the next start waits for it instead of starting
	// another.
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startClose(t, ctx, f, Options{})
	pushes, _, _ := f.state()
	if len(pushes) != 1 {
		t.Errorf("pushes = %q, want 1", pushes)
	}
}

func TestJarWithoutLinks(t *testing.T) {
	f, _ := newFakePhone(t)
	f.noLinks = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startClose(t, ctx, f, Options{})
	pushes, files, noJar := f.state()
	// The shared jar, then the run's own as before.
	if len(pushes) != 2 || pushes[1] != f.command[len("CLASSPATH="):strings.IndexByte(f.command, ' ')] {
		t.Errorf("pushes = %q, command %q", pushes, f.command)
	}
	if len(noJar) > 0 || len(files) != 1 {
		t.Errorf("files = %v, no jar at %q", files, noJar)
	}
}

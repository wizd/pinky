package ios

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	goios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/xpc"
)

func TestLaunchInput(t *testing.T) {
	opts := func(in map[string]any) map[string]any { return in["options"].(map[string]any) }
	app, err := launchInput("com.apple.Preferences", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := opts(app)["payloadURL"]; ok {
		t.Error("an app launch carries a payload URL")
	}
	if got := app["applicationSpecifier"]; !reflect.DeepEqual(got, map[string]any{"bundleIdentifier": map[string]any{"_0": "com.apple.Preferences"}}) {
		t.Errorf("applicationSpecifier = %v", got)
	}
	link, err := launchInput(safariID, "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got := opts(link)["payloadURL"]; !reflect.DeepEqual(got, map[string]any{"relative": "https://example.com/"}) {
		t.Errorf("payloadURL = %v", got)
	}
}

// fakeService plays a CoreDevice service: each dial gets the next of its
// replies (the last one repeats), and it counts the dials, the writes of
// requests and the closed connections.
type fakeService struct {
	replies []func() io.Reader
	dialErr []error // the error of each dial, nil for none
	dials   int
	writes  atomic.Int32 // go-ios writes and closes on its own goroutines
	closed  atomic.Int32
}

func (f *fakeService) dial(goios.DeviceEntry, string) (*xpc.Connection, error) {
	i := f.dials
	f.dials++
	if i < len(f.dialErr) && f.dialErr[i] != nil {
		return nil, f.dialErr[i]
	}
	r := f.replies[min(i, len(f.replies)-1)]()
	return xpc.New(writeFunc(func(p []byte) (int, error) {
		f.writes.Add(1)
		return len(p), nil
	}), readOnly{r}, nil, closeFunc(func() error { f.closed.Add(1); return nil }))
}

type writeFunc func([]byte) (int, error)

func (w writeFunc) Write(p []byte) (int, error) { return w(p) }
func (writeFunc) Read([]byte) (int, error)      { return 0, io.EOF }

type closeFunc func() error

func (c closeFunc) Close() error { return c() }

type readOnly struct{ io.Reader }

func (readOnly) Write(p []byte) (int, error) { return len(p), nil }

// dropped is a stream the phone closed before it replied, as go-ios's
// HTTP/2 layer reports it.
func dropped() io.Reader {
	return errReader{fmt.Errorf("Read: could not read frame. %w", io.EOF)}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// reply is a stream that carries one CoreDevice reply.
func reply(body map[string]any) func() io.Reader {
	return func() io.Reader {
		var b bytes.Buffer
		if err := xpc.EncodeMessage(&b, xpc.Message{Flags: xpc.AlwaysSetFlag | xpc.DataFlag, Body: body, Id: 1}); err != nil {
			panic(err)
		}
		return &b
	}
}

// partial is a stream that closes in the middle of a reply's header.
func partial() io.Reader {
	b := reply(launched)().(*bytes.Buffer).Bytes()
	return io.MultiReader(bytes.NewReader(b[:6]), dropped())
}

var launched = map[string]any{"CoreDevice.output": map[string]any{
	"processToken": map[string]any{"processIdentifier": int64(42)},
}}

func newFakeService(f *fakeService) *cdService {
	s := coreDevice(goios.DeviceEntry{}, appService)
	s.dial = f.dial
	return s
}

// TestInvokeRepeatableRedials: a launch whose connection closes before the
// phone answers (it did once after devicectl installed an app) is sent
// again on a new connection, and the app opens.
func TestInvokeRepeatableRedials(t *testing.T) {
	f := &fakeService{replies: []func() io.Reader{dropped, reply(launched)}}
	s := newFakeService(f)
	defer s.Close()
	out, err := s.invokeRepeatable(context.Background(), featureLaunchApplication, "", nil)
	if err != nil {
		t.Fatalf("invokeRepeatable: %v", err)
	}
	if pid := toInt64(out["processToken"].(map[string]any)["processIdentifier"]); pid != 42 {
		t.Errorf("pid = %d", pid)
	}
	if f.dials != 2 || f.writes.Load() == 0 || f.closed.Load() != 1 {
		t.Errorf("%d dials, %d writes, %d closed; want 2 dials and the dropped connection closed", f.dials, f.writes.Load(), f.closed.Load())
	}
}

// TestInvokeRepeatableDropsTwice: when the second connection drops too, the
// error says the connection dropped, so the agent knows it may try again.
func TestInvokeRepeatableDropsTwice(t *testing.T) {
	f := &fakeService{replies: []func() io.Reader{dropped}}
	s := newFakeService(f)
	defer s.Close()
	_, err := s.invokeRepeatable(context.Background(), featureLaunchApplication, "", nil)
	if !errors.Is(err, driver.ErrConnDropped) || !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v; want ErrConnDropped wrapping the EOF", err)
	}
	if !strings.Contains(err.Error(), "dropped before it answered") || f.dials != 2 {
		t.Errorf("err = %q after %d dials", err, f.dials)
	}
}

// TestInvokeRepeatableDialDrops: a connection that closes while it is set
// up sent nothing, and is dialled again.
func TestInvokeRepeatableDialDrops(t *testing.T) {
	f := &fakeService{
		dialErr: []error{fmt.Errorf("connect %s: NewHttpConnection: could not read frame. %w", appService, io.EOF)},
		replies: []func() io.Reader{reply(launched)},
	}
	s := newFakeService(f)
	defer s.Close()
	if _, err := s.invokeRepeatable(context.Background(), featureLaunchApplication, "", nil); err != nil || f.dials != 2 {
		t.Fatalf("err = %v after %d dials; want success after 2", err, f.dials)
	}
}

// TestInvokeRepeatableOnlyWithoutReply: a request is sent once when the
// phone answered, with an error or with part of a reply before the
// connection closed, and when the caller gave up.
func TestInvokeRepeatableOnlyWithoutReply(t *testing.T) {
	refused := reply(map[string]any{"CoreDevice.error": map[string]any{
		"code": int64(1), "domain": "com.apple.dt.CoreDeviceError",
		"userInfo": map[string]any{"NSLocalizedDescription": "The app is not installed."},
	}})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []struct {
		name  string
		ctx   context.Context
		reply func() io.Reader
	}{
		{"refused", context.Background(), refused},
		{"partial reply", context.Background(), partial},
		{"cancelled", cancelled, dropped},
	} {
		f := &fakeService{replies: []func() io.Reader{c.reply, reply(launched)}}
		s := newFakeService(f)
		_, err := s.invokeRepeatable(c.ctx, featureLaunchApplication, "", nil)
		s.Close()
		if err == nil || errors.Is(err, driver.ErrConnDropped) || f.dials != 1 {
			t.Errorf("%s: err = %v after %d dials; want the first error after 1", c.name, err, f.dials)
		}
	}
}

// TestInvokeSendsOnce: invoke, for requests that are not safe to repeat
// (stopping a process, starting the video), never sends one again, and the
// next request dials a new connection.
func TestInvokeSendsOnce(t *testing.T) {
	f := &fakeService{replies: []func() io.Reader{dropped, reply(launched)}}
	s := newFakeService(f)
	defer s.Close()
	_, err := s.invoke(context.Background(), "com.apple.coredevice.feature.sendsignaltoprocess", "", nil)
	var nr *noReplyError
	if !errors.As(err, &nr) || errors.Is(err, driver.ErrConnDropped) || f.dials != 1 {
		t.Fatalf("err = %v after %d dials; want a no-reply error after 1", err, f.dials)
	}
	if _, err := s.invoke(context.Background(), featureLaunchApplication, "", nil); err != nil || f.dials != 2 {
		t.Fatalf("next request: err = %v after %d dials", err, f.dials)
	}
}

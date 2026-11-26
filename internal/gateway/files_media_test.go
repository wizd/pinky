package gateway

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/image/bmp"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

type mediaIPhoneDev struct {
	*demo.Device

	mu        sync.Mutex
	imported  map[string][]byte
	importErr error
	clipMime  string
	clipData  []byte
	clipErr   error
}

func (m *mediaIPhoneDev) ImportMedia(_ context.Context, name string, r io.Reader, _ int64) error {
	m.mu.Lock()
	err := m.importErr
	m.mu.Unlock()
	if err != nil {
		return err
	}
	b, rerr := io.ReadAll(r)
	if rerr != nil {
		return rerr
	}
	m.mu.Lock()
	if m.imported == nil {
		m.imported = map[string][]byte{}
	}
	m.imported[name] = b
	m.mu.Unlock()
	return nil
}

func (m *mediaIPhoneDev) SetClipboardImage(_ context.Context, mimeType string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clipErr != nil {
		return m.clipErr
	}
	m.clipMime = mimeType
	m.clipData = append([]byte(nil), data...)
	return nil
}

var (
	_ driver.MediaImporter  = (*mediaIPhoneDev)(nil)
	_ driver.ImageClipboard = (*mediaIPhoneDev)(nil)
)

type photoImportProvider struct{ dev *mediaIPhoneDev }

func (p photoImportProvider) Platform() driver.Platform { return driver.IOS }
func (p photoImportProvider) List(context.Context) ([]driver.Info, error) {
	return []driver.Info{p.dev.Info()}, nil
}
func (p photoImportProvider) Open(_ context.Context, id string) (driver.Device, error) {
	if id != p.dev.Info().ID {
		return nil, driver.ErrNotFound
	}
	return p.dev, nil
}

func makeTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 0xFF, G: 0x60, B: 0x20, A: 0xFF})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeTestBMP(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.RGBA{R: 0x20, G: 0xA0, B: 0xFF, A: 0xFF})
	var buf bytes.Buffer
	if err := bmp.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newMediaIPhoneGateway(t *testing.T) (*Gateway, *mediaIPhoneDev) {
	t.Helper()
	dev := &mediaIPhoneDev{Device: demo.NewDevice("iphone-1", "Wizard's iPhone", driver.IOS)}
	g, err := New(Options{
		Providers:      []driver.Provider{photoImportProvider{dev: dev}},
		TrustNewAgents: true,
		DataDir:        t.TempDir(),
		NoScreenshots:  true,
		DiscoverEvery:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g, dev
}

func TestPhoneFilesMediaImportAndClipboard(t *testing.T) {
	ctx := context.Background()
	g, dev := newMediaIPhoneGateway(t)
	s, _ := connect(t, g, "Upload cover photo")

	// List explains Photos push and clipboard.
	res := s.Files(ctx, FilesRequest{})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "Photos") || !strings.Contains(res.Message, "clipboard") {
		t.Fatalf("list message: %s", res.Message)
	}

	// Pull is still unsupported (no Pinky folder).
	if res := s.Files(ctx, FilesRequest{Action: "pull"}); res.Error != ErrUnsupported {
		t.Fatalf("pull: %+v", res)
	}

	// Upload a PNG -> hint mentions both push and clipboard.
	pngBytes := makeTestPNG(t)
	res = s.Files(ctx, FilesRequest{Action: "upload", Paths: []string{"cover.png"}, Body: bytes.NewReader(pngBytes)})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, `action: "push"`) || !strings.Contains(res.Message, `action: "clipboard"`) {
		t.Fatalf("upload hint: %s", res.Message)
	}

	// Upload a plain text file -> push fails before opening an approval.
	res = s.Files(ctx, FilesRequest{Action: "upload", Paths: []string{"note.txt"}, Body: strings.NewReader("hello")})
	mustStatus(t, res, StatusSettled)
	if res := s.Files(ctx, FilesRequest{Action: "push", Paths: []string{"note.txt"}}); res.Error != ErrUnsupported {
		t.Fatalf("push non-image: %+v", res)
	}
	if len(g.Needs()) != 0 {
		t.Fatalf("unexpected approval opened for non-image: %+v", g.Needs())
	}

	// Push the PNG -> asks the person (Files rule) and saves to Photos.
	done := make(chan *Result, 1)
	go func() {
		done <- s.Files(ctx, FilesRequest{Action: "push", Paths: []string{"cover.png"}, Intent: "Add cover photo"})
	}()
	ap := waitNeedKind(t, g, NeedApproval)
	if ap.Rule != filesRule || !strings.Contains(ap.Title, "cover.png") {
		t.Fatalf("approval = %+v", ap)
	}
	if err := g.Decide(ap.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	res = <-done
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "Photos library") {
		t.Fatalf("push media message: %s", res.Message)
	}
	dev.mu.Lock()
	gotImported := dev.imported["cover.png"]
	dev.mu.Unlock()
	if !bytes.Equal(gotImported, pngBytes) {
		t.Fatalf("imported bytes mismatch: len=%d want=%d", len(gotImported), len(pngBytes))
	}

	// Upload a BMP and copy it to the clipboard -> transcoded to PNG, asks approval.
	bmpBytes := makeTestBMP(t)
	res = s.Files(ctx, FilesRequest{Action: "upload", Paths: []string{"shot.bmp"}, Body: bytes.NewReader(bmpBytes)})
	mustStatus(t, res, StatusSettled)

	done = make(chan *Result, 1)
	go func() {
		done <- s.Files(ctx, FilesRequest{Action: "clipboard", Paths: []string{"shot.bmp"}})
	}()
	ap = waitNeedKind(t, g, NeedApproval)
	if ap.Rule != filesRule || !strings.Contains(ap.Detail, "clipboard") {
		t.Fatalf("clipboard approval = %+v", ap)
	}
	if err := g.Decide(ap.ID, AllowTask, ""); err != nil {
		t.Fatal(err)
	}
	res = <-done
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "clipboard") {
		t.Fatalf("clipboard message: %s", res.Message)
	}
	dev.mu.Lock()
	gotMime, gotClip := dev.clipMime, dev.clipData
	dev.mu.Unlock()
	if gotMime != "image/png" {
		t.Fatalf("clipboard mime = %q, want image/png", gotMime)
	}
	if _, err := png.DecodeConfig(bytes.NewReader(gotClip)); err != nil {
		t.Fatalf("clipboard payload is not valid PNG: %v", err)
	}

	// Locked phone reports StatusLocked cleanly.
	dev.mu.Lock()
	dev.importErr = driver.ErrLocked
	dev.mu.Unlock()
	res = s.Files(ctx, FilesRequest{Action: "push", Paths: []string{"cover.png"}})
	if res.Status != StatusLocked {
		t.Fatalf("locked push = %+v, want StatusLocked", res)
	}
	dev.mu.Lock()
	dev.importErr = nil
	dev.mu.Unlock()

	// Read-only scope and Read-only mode block clipboard.
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeReadOnly})
	if res := s.Files(ctx, FilesRequest{Action: "clipboard", Paths: []string{"cover.png"}}); res.Error != ErrScopeDenied {
		t.Fatalf("read-only scope clipboard: %+v", res)
	}
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeEverything})
	if err := g.SetReadOnly("iphone-1", true); err != nil {
		t.Fatal(err)
	}
	if res := s.Files(ctx, FilesRequest{Action: "clipboard", Paths: []string{"cover.png"}}); res.Status != StatusBlocked || res.Rule != "Read-only" {
		t.Fatalf("read-only mode clipboard: %+v", res)
	}
}

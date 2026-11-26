package ios

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"howett.net/plist"
)

func TestAppBuildArgs(t *testing.T) {
	args := appBuildArgs("/kit", "/dd", "UDID", "ABCDE12345", "PinkyLite", false)
	for _, want := range []string{"-project", filepath.Join("/kit", "Pinky.xcodeproj"), "-scheme", "PinkyLite", "id=UDID",
		"DEVELOPMENT_TEAM=ABCDE12345", "PINKY_BUNDLE_ID=pinky.abcde12345", "CODE_SIGN_STYLE=Automatic"} {
		if !slices.Contains(args, want) {
			t.Errorf("args lack %q: %v", want, args)
		}
	}
	if slices.Contains(args, "-allowProvisioningUpdates") {
		t.Errorf("provisioning updates without asking: %v", args)
	}
	if args := appBuildArgs("/kit", "/dd", "UDID", "T", "Pinky", true); !slices.Contains(args, "-allowProvisioningUpdates") {
		t.Errorf("no provisioning updates: %v", args)
	}
}

// TestLiteNextStep: after installing the lite build, the command line
// says what the phone needs: LocalDevVPN from the App Store, with Pinky's
// addresses, connected.
func TestLiteNextStep(t *testing.T) {
	if appSchemes[len(appSchemes)-1] != liteScheme {
		t.Errorf("the lite build is not the last tried: %v", appSchemes)
	}
	s := liteNextStep("Sam's iPhone")
	for _, want := range []string{"Sam's iPhone", "LocalDevVPN", LocalDevVPNStore, "Tunnel IP to 10.7.0.2/30", "Device IP to 10.7.0.1/32", "Connect"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q lacks %q", s, want)
		}
	}
}

func TestMarkHost(t *testing.T) {
	info := map[string]any{"UIBackgroundModes": []any{"audio"}}
	markHost(info)
	markHost(info) // twice adds nothing
	if info[wdaHostKey] != true {
		t.Errorf("no mark: %v", info)
	}
	if modes := info["UIBackgroundModes"].([]any); len(modes) != 2 || modes[0] != "audio" || modes[1] != "continuous" {
		t.Errorf("UIBackgroundModes = %v", modes)
	}
	if !wdaHost(info) {
		t.Error("wdaHost does not know the mark markHost writes")
	}
}

func writeXMLPlist(t *testing.T, path string, v any) {
	t.Helper()
	b, err := plist.Marshal(v, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHostApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Installing Pinky on the iPhone runs only on a Mac; Windows has no
		// executable bit and writes links with its own separators.
		t.Skip("the app bundle's executable bits and links are a Mac's")
	}
	dir := t.TempDir()
	runner := filepath.Join(dir, "WebDriverAgentRunner-Runner.app")
	test := filepath.Join(runner, "PlugIns", wdaTestBundle)
	fw := filepath.Join(test, "Frameworks", "WebDriverAgentLib.framework")
	if err := os.MkdirAll(filepath.Join(fw, "Versions", "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(test, "WebDriverAgentRunner"), []byte("exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Versions/A", filepath.Join(fw, "Current")); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(dir, "PinkyLite.app")
	writeXMLPlist(t, filepath.Join(app, "Info.plist"), map[string]any{"CFBundleIdentifier": "pinky.t.lite"})

	if err := hostApp(app, runner); err != nil {
		t.Fatal(err)
	}
	got := filepath.Join(app, "PlugIns", wdaTestBundle)
	if fi, err := os.Stat(filepath.Join(got, "WebDriverAgentRunner")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("test executable not copied as one: %v %v", fi, err)
	}
	if l, err := os.Readlink(filepath.Join(got, "Frameworks", "WebDriverAgentLib.framework", "Current")); err != nil || l != "Versions/A" {
		t.Errorf("framework link = %q, %v", l, err)
	}
	info, _, err := readInfo(app)
	if err != nil {
		t.Fatal(err)
	}
	if !wdaHost(info) || info["CFBundleIdentifier"] != "pinky.t.lite" {
		t.Errorf("Info.plist = %v", info)
	}
	// Again, over the copy already there.
	if err := hostApp(app, runner); err != nil {
		t.Fatalf("second hostApp: %v", err)
	}
	if err := hostApp(app, filepath.Join(dir, "none.app")); err == nil {
		t.Error("hostApp without a test bundle succeeded")
	}
}

func TestHostXctestrun(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "WebDriverAgentRunner_iphoneos.xctestrun")
	writeXMLPlist(t, src, map[string]any{
		"__xctestrun_metadata__": map[string]any{"FormatVersion": 1},
		"WebDriverAgentRunner": map[string]any{
			"TestBundlePath":           "__TESTHOST__/PlugIns/WebDriverAgentRunner.xctest",
			"TestHostPath":             "__TESTROOT__/Debug-iphoneos/WebDriverAgentRunner-Runner.app",
			"TestHostBundleIdentifier": "com.facebook.WebDriverAgentRunner.xctrunner",
		},
	})
	dst := filepath.Join(dir, "PinkyHost.xctestrun")
	if err := hostXctestrun(src, dst, filepath.Join("Debug-iphoneos", "PinkyLite.app"), "pinky.t.lite", "PinkyLite"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]any
	if _, err := plist.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	w := doc["WebDriverAgentRunner"]
	if w["TestHostPath"] != "__TESTROOT__/Debug-iphoneos/PinkyLite.app" || w["TestHostBundleIdentifier"] != "pinky.t.lite" {
		t.Errorf("test = %v", w)
	}
	if w["TestBundlePath"] != "__TESTHOST__/PlugIns/WebDriverAgentRunner.xctest" {
		t.Errorf("TestBundlePath changed: %v", w["TestBundlePath"])
	}
	if _, ok := doc["__xctestrun_metadata__"]["TestHostPath"]; ok {
		t.Error("the metadata got a test host")
	}

	empty := filepath.Join(dir, "empty.xctestrun")
	writeXMLPlist(t, empty, map[string]any{"__xctestrun_metadata__": map[string]any{}})
	if err := hostXctestrun(empty, dst, "x.app", "id", "exe"); err == nil {
		t.Error("an xctestrun without a test passed")
	}
}

// kitArchive is a tar.gz with a kit's top folder.
func kitArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range []*tar.Header{
		{Name: "pinky-iphone/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "pinky-iphone/Pinky.xcodeproj/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "pinky-iphone/Pinky.xcodeproj/project.pbxproj", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			tw.Write([]byte("{}"))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestIphoneKit(t *testing.T) {
	ctx := context.Background()
	oldVersion, oldURL := PinkyVersion, iphoneKitURL
	t.Cleanup(func() { PinkyVersion, iphoneKitURL = oldVersion, oldURL })
	t.Setenv(iphoneKitEnv, "")

	PinkyVersion = "0.0.0-dev"
	if _, err := iphoneKit(ctx, http.DefaultClient, t.TempDir(), t.TempDir()); !errors.Is(err, errNoKit) {
		t.Fatalf("a development build: %v; want errNoKit", err)
	}

	archive := kitArchive(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/pinky-iphone-2026.1112.9.tar.gz" {
			http.NotFound(w, r)
			return
		}
		w.Write(archive)
	}))
	defer srv.Close()
	PinkyVersion = "2026.1112.9"
	iphoneKitURL = func(v string) string { return srv.URL + "/pinky-iphone-" + v + ".tar.gz" }
	cache := t.TempDir()
	for i := range 2 {
		kit, err := iphoneKit(ctx, srv.Client(), cache, t.TempDir())
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if _, err := os.Stat(filepath.Join(kit, "Pinky.xcodeproj", "project.pbxproj")); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if hits != 1 {
		t.Errorf("downloaded %d times; want once", hits)
	}

	PinkyVersion = "2026.1112.10"
	if _, err := iphoneKit(ctx, srv.Client(), cache, t.TempDir()); err == nil {
		t.Error("a release without a kit gave one")
	}

	// A folder named by the environment is the kit itself.
	dir := t.TempDir()
	t.Setenv(iphoneKitEnv, dir)
	if kit, err := iphoneKit(ctx, srv.Client(), cache, t.TempDir()); err != nil || kit != dir {
		t.Errorf("kit = %q, %v; want %q", kit, err, dir)
	}
}

// TestIphoneKitChecksItsHash: with the release's kit hash known, a kit that
// hashes otherwise is never unpacked. A cached one is fetched again; a
// download is refused and not kept.
func TestIphoneKitChecksItsHash(t *testing.T) {
	ctx := context.Background()
	oldVersion, oldURL, oldSum := PinkyVersion, iphoneKitURL, PinkyKitSHA256
	t.Cleanup(func() { PinkyVersion, iphoneKitURL, PinkyKitSHA256 = oldVersion, oldURL, oldSum })
	t.Setenv(iphoneKitEnv, "")

	archive := kitArchive(t)
	sum := sha256.Sum256(archive)
	served := archive
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(served)
	}))
	defer srv.Close()
	PinkyVersion = "2026.1124.9"
	iphoneKitURL = func(v string) string { return srv.URL + "/pinky-iphone-" + v + ".tar.gz" }
	PinkyKitSHA256 = strings.ToUpper(hex.EncodeToString(sum[:])) // either case
	cache := t.TempDir()
	cached := filepath.Join(cache, "pinky-iphone-2026.1124.9.tar.gz")

	// The release's kit: downloaded once, then taken from the cache.
	for i := range 2 {
		if _, err := iphoneKit(ctx, srv.Client(), cache, t.TempDir()); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if hits != 1 {
		t.Fatalf("downloaded %d times; want once", hits)
	}

	// A cached kit that changed is fetched again, and the release's is used.
	tampered := kitArchive(t)
	tampered = append(tampered[:len(tampered):len(tampered)], 0)
	if err := os.WriteFile(cached, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := iphoneKit(ctx, srv.Client(), cache, t.TempDir()); err != nil {
		t.Fatalf("after the cache changed: %v", err)
	}
	if hits != 2 {
		t.Errorf("downloaded %d times; want the changed cache fetched again", hits)
	}
	if got, _ := os.ReadFile(cached); !bytes.Equal(got, archive) {
		t.Error("the cache doesn't hold the release's kit again")
	}

	// A download that isn't the release's kit is refused and not kept.
	os.Remove(cached)
	served = tampered
	dir := t.TempDir()
	if _, err := iphoneKit(ctx, srv.Client(), cache, dir); err == nil || !strings.Contains(err.Error(), "not the one it was released with") {
		t.Fatalf("a kit that isn't the release's: %v; want it refused", err)
	}
	if _, err := os.Stat(cached); !os.IsNotExist(err) {
		t.Error("the refused kit was kept in the cache")
	}
	if left, _ := filepath.Glob(filepath.Join(cache, "*")); len(left) != 0 {
		t.Errorf("the refused download left %v in the cache", left)
	}
	bad := sha256.Sum256(tampered)
	if _, err := iphoneKit(ctx, srv.Client(), cache, dir); err == nil ||
		!strings.Contains(err.Error(), hex.EncodeToString(bad[:])[:12]) || !strings.Contains(err.Error(), hex.EncodeToString(sum[:])[:12]) {
		t.Errorf("the refusal doesn't name both hashes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kit")); !os.IsNotExist(err) {
		t.Error("the refused kit was unpacked")
	}

	// A build that doesn't know the hash takes the kit as it comes, as before.
	PinkyKitSHA256 = ""
	if _, err := iphoneKit(ctx, srv.Client(), cache, t.TempDir()); err != nil {
		t.Errorf("without a known hash: %v", err)
	}
}

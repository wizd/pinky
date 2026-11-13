package ios

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"howett.net/plist"

	"github.com/DIYgod/pinky/internal/driver"
)

// Installing Pinky's iPhone app as WebDriverAgent's host, in place of
// Pinky Helper (docs/notes/iphone-app.md, "Hosting WebDriverAgent").
//
// Desktop Pinky has neither the app's Go toolchain for iOS nor its
// sources, so each release carries an iPhone app kit,
// pinky-iphone-<version>.tar.gz (ios/build.sh kit): the generated Xcode
// project, the Swift sources and the Go core already built for the phone.
// InstallHelper, once it has built WebDriverAgent's runner, builds the app
// from the kit with the person's team (bundle id pinky.<team id>, the shape
// policy knows for Pinky's own), puts WDA's test bundle into its PlugIns,
// marks it (wdaHostKey), signs it again and installs it by running WDA's
// test with the app as the runner, as it does with the helper. Anything
// that fails there leaves the app aside and installs Pinky Helper as
// before; a helper already on a phone goes on working either way, and
// Pinky prefers the app once it is there (pickRunnerApp).

// PinkyVersion is the version of this Pinky, whose iPhone app kit
// InstallHelper downloads; a development build has none to download.
var PinkyVersion = "0.0.0-dev"

// iphoneKitEnv names a kit to use instead of the release's: the archive or
// an unpacked folder (ios/build.sh kit), for development builds.
const iphoneKitEnv = "PINKY_IPHONE_KIT"

// iphoneKitURL is where a release's kit is; a var for tests.
var iphoneKitURL = func(version string) string {
	return "https://github.com/DIYgod/pinky/releases/download/v" + version + "/pinky-iphone-" + version + ".tar.gz"
}

// iphoneKitMax bounds an unpacked kit; the core alone is some 35 MB.
const iphoneKitMax = 256 << 20

// errNoKit: this Pinky has no iPhone app kit to build from.
var errNoKit = errors.New("this Pinky has no iPhone app kit")

// iphoneKit finds the kit and unpacks it into dir/kit, returning the
// folder with Pinky.xcodeproj. The release's is downloaded into cache once.
func iphoneKit(ctx context.Context, client *http.Client, cache, dir string) (string, error) {
	archive := os.Getenv(iphoneKitEnv)
	if archive != "" {
		if st, err := os.Stat(archive); err == nil && st.IsDir() {
			return archive, nil
		}
	} else {
		if strings.Contains(PinkyVersion, "dev") || PinkyVersion == "" {
			return "", errNoKit
		}
		archive = filepath.Join(cache, "pinky-iphone-"+PinkyVersion+".tar.gz")
		if _, err := os.Stat(archive); err != nil {
			if err := download(ctx, client, iphoneKitURL(PinkyVersion), archive); err != nil {
				return "", err
			}
		}
	}
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	kit := filepath.Join(dir, "kit")
	if _, err := untar(f, kit, iphoneKitMax); err != nil {
		os.RemoveAll(kit)
		if os.Getenv(iphoneKitEnv) == "" {
			os.Remove(archive) // fetched again next time
		}
		return "", fmt.Errorf("unpacking the iPhone app kit: %w", err)
	}
	if _, err := os.Stat(filepath.Join(kit, "Pinky.xcodeproj")); err != nil {
		return "", fmt.Errorf("the iPhone app kit has no Xcode project: %w", err)
	}
	return kit, nil
}

// download fetches url into path, through a file beside it.
func download(ctx context.Context, client *http.Client, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Pinky")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading the iPhone app kit: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading the iPhone app kit: GitHub answered %s", res.Status)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(res.Body, iphoneKitMax))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// appBundleID is the bundle id desktop Pinky gives Pinky's app it builds
// with team, the shape policy knows (policy.iPhoneTeamApp). The build
// without a packet tunnel adds ".lite" (ios/app/project.yml).
func appBundleID(team string) string { return "pinky." + strings.ToLower(team) }

// appSchemes are the kit's schemes, in the order InstallHelper tries them:
// the app with its own packet tunnel, which needs a paid team (a personal
// team cannot sign the Network Extension capability), then the one without.
var appSchemes = []string{"Pinky", "PinkyLite"}

// appBuildArgs builds the kit's scheme for the phone, signed for team.
// updates lets Xcode ask Apple for profiles (and the App IDs with their
// capabilities) and register the phone.
func appBuildArgs(kit, dd, udid, team, scheme string, updates bool) []string {
	args := []string{"build",
		"-project", filepath.Join(kit, "Pinky.xcodeproj"),
		"-scheme", scheme,
		"-configuration", "Debug",
		"-destination", "id=" + udid,
		"-derivedDataPath", dd}
	if updates {
		args = append(args, "-allowProvisioningUpdates", "-allowProvisioningDeviceRegistration")
	}
	return append(args,
		"CODE_SIGN_STYLE=Automatic",
		"DEVELOPMENT_TEAM="+team,
		"PINKY_BUNDLE_ID="+appBundleID(team))
}

// hostApp makes app WebDriverAgent's host: the test bundle from the
// runner's PlugIns into the app's, the mark Pinky finds it by, and the
// background mode Xcode's runner has (ios/build.sh's embed_wda does the
// same for a build from source).
func hostApp(app, runner string) error {
	src := filepath.Join(runner, "PlugIns", wdaTestBundle)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("the runner has no test bundle: %w", err)
	}
	dst := filepath.Join(app, "PlugIns", wdaTestBundle)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	info, format, err := readInfo(app)
	if err != nil {
		return err
	}
	markHost(info)
	b, err := plist.Marshal(info, format)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(app, "Info.plist"), b)
}

// markHost gives an app's Info.plist the host's mark and the background
// mode "continuous", keeping the modes it has.
func markHost(info map[string]any) {
	info[wdaHostKey] = true
	modes, _ := info["UIBackgroundModes"].([]any)
	for _, m := range modes {
		if m == "continuous" {
			return
		}
	}
	info["UIBackgroundModes"] = append(modes, "continuous")
}

// copyTree copies a folder of regular files, folders and symbolic links
// (a framework's), keeping the executable bits.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, target)
		case fi.IsDir():
			return os.MkdirAll(target, 0o755)
		}
		if err := copyFile(p, target); err != nil {
			return err
		}
		return os.Chmod(target, fi.Mode().Perm())
	})
}

// signHosted signs the test bundle in app and its frameworks with the
// identity whose certificate has this SHA-1, as Xcode signs a test bundle:
// no entitlements. resignApp then signs the app around them.
func signHosted(ctx context.Context, x xcode, identity, app string) error {
	test := filepath.Join(app, "PlugIns", wdaTestBundle)
	parts, _ := filepath.Glob(filepath.Join(test, "Frameworks", "*"))
	for _, p := range append(parts, test) {
		cmd := exec.CommandContext(ctx, "/usr/bin/codesign", "--force", "--sign", identity, "--timestamp=none", p)
		cmd.Env = x.env()
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("codesign %s: %v: %s", filepath.Base(p), err, strings.TrimSpace(string(b)))
		}
	}
	return nil
}

// hostXctestrun writes, from the runner's xctestrun, one that runs WDA's
// test with Pinky's app as its host: app, a folder in products, with
// bundle id id and executable exe. xcodebuild test-without-building
// installs the app with it and runs the test there.
func hostXctestrun(src, dst, appRel, id, exe string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var doc map[string]any
	format, err := plist.Unmarshal(b, &doc)
	if err != nil {
		return fmt.Errorf("reading %s: %w", filepath.Base(src), err)
	}
	host := "__TESTROOT__/" + filepath.ToSlash(appRel)
	found := false
	for k, v := range doc {
		t, ok := v.(map[string]any)
		if !ok || strings.HasPrefix(k, "__") {
			continue
		}
		found = true
		t["TestHostPath"] = host
		t["TestHostBundleIdentifier"] = id
		t["DependentProductPaths"] = []any{host, host + "/PlugIns/" + wdaTestBundle}
		t["ProcessNamesForCrashReportCollection"] = []any{exe}
		t["BundleIdentifiersForCrashReportEmphasis"] = []any{id}
	}
	if !found {
		return fmt.Errorf("%s names no test", filepath.Base(src))
	}
	out, err := plist.Marshal(doc, format)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, out, 0o644)
}

// certSHA1 is the SHA-1 of the certificate an app was signed with, which
// names the identity to sign it again with.
func certSHA1(ctx context.Context, x xcode, app string) (string, error) {
	tmp, err := os.MkdirTemp("", "pinky-cert-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	der, err := extractCert(ctx, x, app, tmp)
	if err != nil {
		return "", err
	}
	return sha1Hex(der), nil
}

// pinkyAppIn is the app Xcode built for scheme in a derived data folder.
func pinkyAppIn(dd, scheme string) string {
	return filepath.Join(dd, "Build", "Products", "Debug-iphoneos", scheme+".app")
}

// appInstall is what installApp needs from InstallHelper.
type appInstall struct {
	x                xcode
	xr               xcodeRunner
	out              io.Writer
	client           *http.Client
	cache, tmp       string // Pinky's WebDriverAgent folder, a scratch folder
	udid, team       string
	runner, products string // the built and signed runner, its Products folder
	local            bool
	testArgs         func(xctestrun string) []string // also tells the person
	start            func(args []string) (string, error)
}

// installApp builds Pinky's app from the kit, makes it WDA's host, signs
// it and installs it by starting WDA in it. It returns the app's bundle id
// and the profile that signs it. errNoKit and every other error leave the
// helper to InstallHelper.
func installApp(ctx context.Context, a appInstall) (string, provisioningProfile, error) {
	runnerRun := xctestrun(a.products)
	if runnerRun == "" {
		return "", provisioningProfile{}, errors.New("the runner's build has no xctestrun")
	}
	kit, err := iphoneKit(ctx, a.client, a.cache, a.tmp)
	if err != nil {
		return "", provisioningProfile{}, err
	}
	var app, scheme, tail string
	for _, s := range appSchemes {
		dd := filepath.Join(a.tmp, "app-"+s)
		fmt.Fprintf(a.out, "Building Pinky's iPhone app (%s) for team %s.\n", s, a.team)
		tail, err = a.xr.run(ctx, driver.HelperBuilding, appBuildArgs(kit, dd, a.udid, a.team, s, false), nil)
		if err != nil && ctx.Err() == nil && !a.local && needsProvisioning(tail) {
			tail, err = a.xr.run(ctx, driver.HelperBuilding, appBuildArgs(kit, dd, a.udid, a.team, s, true), nil)
		}
		if err == nil {
			app, scheme = pinkyAppIn(dd, s), s
			break
		}
		if ctx.Err() != nil {
			return "", provisioningProfile{}, ctx.Err()
		}
		fmt.Fprintf(a.out, "Xcode could not build %s: %s\n", s, xcodeErrors(tail))
	}
	if app == "" {
		return "", provisioningProfile{}, errors.New("Xcode could not build Pinky's iPhone app")
	}
	profile, err := embeddedProfile(app)
	if err != nil {
		return "", provisioningProfile{}, fmt.Errorf("reading the profile Xcode signed the app with: %w", err)
	}
	if !profile.runsOn(a.udid) {
		return "", provisioningProfile{}, fmt.Errorf("the profile Xcode signed the app with, %q, does not include the phone", profile.Name)
	}
	info, _, err := readInfo(app)
	if err != nil {
		return "", provisioningProfile{}, err
	}
	id, _ := info["CFBundleIdentifier"].(string)
	exe, _ := info["CFBundleExecutable"].(string)
	if id == "" || exe == "" {
		return "", provisioningProfile{}, errors.New("the app Xcode built has no bundle id")
	}
	cert, err := certSHA1(ctx, a.x, app)
	if err != nil {
		return "", provisioningProfile{}, err
	}
	if err := hostApp(app, a.runner); err != nil {
		return "", provisioningProfile{}, fmt.Errorf("putting WebDriverAgent into the app: %w", err)
	}
	if err := signHosted(ctx, a.x, cert, app); err != nil {
		return "", provisioningProfile{}, fmt.Errorf("signing WebDriverAgent in the app: %w", err)
	}
	if err := resignApp(ctx, a.x, app); err != nil {
		return "", provisioningProfile{}, fmt.Errorf("signing the app again: %w", err)
	}
	// The app beside the runner, so the xctestrun names both from
	// __TESTROOT__ (the Products folder).
	rel := filepath.Join("Debug-iphoneos", scheme+".app")
	dst := filepath.Join(a.products, rel)
	if err := os.RemoveAll(dst); err != nil {
		return "", provisioningProfile{}, err
	}
	if err := os.Rename(app, dst); err != nil {
		return "", provisioningProfile{}, err
	}
	// Beside the runner's xctestrun, read before this one is there: Xcode
	// resolves __TESTROOT__ to the folder an xctestrun is in.
	final := filepath.Join(a.products, "PinkyHost.xctestrun")
	if err := hostXctestrun(runnerRun, final, rel, id, exe); err != nil {
		return "", provisioningProfile{}, err
	}
	defer os.Remove(final)
	tail, err = a.start(a.testArgs(final))
	if errors.Is(err, errHelperUp) {
		return id, profile, nil
	}
	if err == nil {
		err = fmt.Errorf("the test ended before WebDriverAgent answered: %s", xcodeErrors(tail))
	}
	return "", provisioningProfile{}, err
}

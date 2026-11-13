package ios

import (
	"errors"
	"fmt"
	"io"

	goios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
)

// Lockdown services over the tunnel alone. A Device opened with
// OpenOverTunnel (Pinky's iPhone app, on the phone it drives) has no
// usbmuxd: the lockdown services the driver otherwise reaches through it
// are reached through their ".shim.remote" twins in the tunnel's service
// list, after an RSD check-in, and lockdown's values through
// com.apple.mobile.lockdown.remote.trusted, which needs no session since
// the tunnel is already paired and encrypted.

const (
	installProxyService   = "com.apple.mobile.installation_proxy"
	springboardService    = "com.apple.springboardservices"
	remoteLockdownService = "com.apple.mobile.lockdown.remote.trusted"
	shimSuffix            = ".shim.remote"
)

// maxBrowseChunks bounds the installation proxy's answer, as go-ios does:
// a phone that never says Complete must not keep the read going.
const maxBrowseChunks = 1000

// shimConn connects to a lockdown service's twin on the tunnel.
func (l *link) shimConn(service string) (goios.DeviceConnectionInterface, error) {
	return goios.ConnectToShimService(l.entry, service+shimSuffix)
}

// appInfos lists every app, as installationproxy's BrowseAllApps does:
// through usbmuxd, or through the installation proxy's shim on a Device
// opened over its tunnel alone.
func (l *link) appInfos() ([]installationproxy.AppInfo, error) {
	if !l.tunnelOnly {
		conn, err := installationproxy.New(l.base)
		if err != nil {
			return nil, fmt.Errorf("installation proxy: %w", err)
		}
		defer conn.Close()
		return conn.BrowseAllApps()
	}
	conn, err := l.shimConn(installProxyService)
	if err != nil {
		return nil, fmt.Errorf("installation proxy: %w", err)
	}
	defer conn.Close()
	return browseAppInfos(conn.Reader(), conn.Writer())
}

// appAttributes are the keys of each app the app list reads (readAppInfos,
// visibleApp, urlSchemes). Without them the proxy sends every key of every
// app, entitlements included: megabytes on a phone with many apps, and
// more once decoded, which Pinky's packet-tunnel extension, held to about
// 50 MB, cannot afford.
var appAttributes = []any{
	"CFBundleIdentifier", "CFBundleDisplayName", "CFBundleName", "ApplicationType", "Path",
	"CFBundleURLTypes", "SBAppTags", "LSApplicationLaunchProhibited",
}

// browseAppInfos asks an installation proxy for every app, with the keys
// the app list reads, and reads its answer, which comes in chunks until one
// says Complete.
func browseAppInfos(r io.Reader, w io.Writer) ([]installationproxy.AppInfo, error) {
	codec := goios.NewPlistCodecReadWriter(r, w)
	req := map[string]any{"Command": "Browse", "ClientOptions": map[string]any{
		"ShowLaunchProhibitedApps": true, "ReturnAttributes": appAttributes,
	}}
	if err := codec.Write(req); err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	var out []installationproxy.AppInfo
	for range maxBrowseChunks {
		var resp struct {
			Status      string
			Error       string
			CurrentList []installationproxy.AppInfo
		}
		if err := codec.Read(&resp); err != nil {
			return nil, fmt.Errorf("list apps: %w", err)
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("list apps: %s", resp.Error)
		}
		out = append(out, resp.CurrentList...)
		if resp.Status == "Complete" {
			return out, nil
		}
	}
	return nil, errors.New("list apps: the phone never finished its answer")
}

// iconLayout reads SpringBoard's icon layout (format version 2): through
// usbmuxd, or through springboardservices' shim.
func (l *link) iconLayout(client func(io.Closer)) (any, error) {
	var conn goios.DeviceConnectionInterface
	var err error
	if l.tunnelOnly {
		conn, err = l.shimConn(springboardService)
	} else {
		conn, err = goios.ConnectToService(l.base, springboardService)
	}
	if err != nil {
		return nil, fmt.Errorf("could not connect to '%s': %w", springboardService, err)
	}
	defer conn.Close()
	client(conn)
	codec := goios.NewPlistCodecReadWriter(conn.Reader(), conn.Writer())
	if err := codec.Write(map[string]any{"command": "getIconState", "formatVersion": "2"}); err != nil {
		return nil, err
	}
	var layout any
	if err := codec.Read(&layout); err != nil {
		return nil, err
	}
	return layout, nil
}

// lockdownValue reads one lockdown value: through usbmuxd with a session,
// or through the tunnel's trusted lockdown service.
func (l *link) lockdownValue(key string) (any, error) {
	if !l.tunnelOnly {
		conn, err := goios.ConnectLockdownWithSession(l.base)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		return conn.GetValue(key)
	}
	conn, err := l.shimConnPlain(remoteLockdownService)
	if err != nil {
		return nil, err
	}
	ld := goios.NewLockDownConnection(conn)
	defer ld.Close()
	return ld.GetValue(key)
}

// shimConnPlain connects to a service on the tunnel by its own name, with
// the RSD check-in shim services expect.
func (l *link) shimConnPlain(service string) (goios.DeviceConnectionInterface, error) {
	return goios.ConnectToShimService(l.entry, service)
}

//go:build ios

package ios

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	goios "github.com/danielpaulus/go-ios/ios"

	"github.com/DIYgod/pinky/internal/driver"
)

// OpenOverTunnel opens a Device over a CoreDevice tunnel stream the caller
// set up itself. It is for Pinky running on the phone it drives
// (ios/core): there is no usbmuxd there, so the stream comes from
// RemotePairing over the phone's own loopback, already through TLS-PSK.
// conn must not have started the CDTunnel exchange.
//
// Only the services reached through the tunnel's service list work on such
// a Device: video, input, screenshots, the accessibility tree from the
// remote AX service, the pasteboard, and the lockdown services that have a
// twin there (shim.go): the app catalog, the home screen's names and the
// lock state. The rest of what goes through lockdown over usbmuxd (vitals,
// the inspector fallback) fails.
//
// The display stream and its HID session start with the first input or
// view (live), not here: nobody watches the phone's own screen, and a stream
// started at open runs for streamIdle with nothing to show it to, about
// 500 KB/s through the same loopback as the first read (2026-11-12).
func OpenOverTunnel(ctx context.Context, info driver.Info, conn io.ReadWriteCloser, log *slog.Logger) (*Device, error) {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	params, err := tunnelHandshake(conn, requestedMTU)
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("tunnel handshake: %w", err)
	}
	t, err := newTunnel(info.ID, conn, params)
	if err != nil {
		conn.Close()
		return nil, err
	}
	base := goios.DeviceEntry{Properties: goios.DeviceProperties{SerialNumber: info.ID}}
	l := &link{udid: info.ID, base: base, tunnel: t, tunnelOnly: true}
	l.entry = t.Entry(base, nil)
	rsd, rc, err := l.handshake(ctx, true)
	if err != nil {
		t.Close()
		return nil, err
	}
	l.rsd = rsd
	l.entry = t.Entry(base, rsd)
	l.rsdConn = rc
	time.AfterFunc(rsdLinger, l.closeRSD)

	d := &Device{info: info, l: l, log: logTo(log), done: make(chan struct{}), two: twoScreens(info.Model)}
	if err := d.readDisplays(ctx); err != nil {
		l.Close()
		return nil, err
	}
	go d.idleLoop()
	go d.orientLoop()
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		d.raxReady(c)
		if pb := d.pasteboard(); pb != nil {
			pb.connect()
		}
	}()
	return d, nil
}

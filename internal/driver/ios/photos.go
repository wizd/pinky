package ios

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/image/bmp"
	"golang.org/x/image/webp"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

const (
	// maxImportMediaBytes caps a single image imported into iPhone Photos.
	maxImportMediaBytes = 50 << 20
	// maxClipboardImageBytes caps a single image placed on UIPasteboard.
	maxClipboardImageBytes = 25 << 20

	safariTunnelHitWait = 3500 * time.Millisecond
	safariLANHitWait    = 5 * time.Second
	safariImgServeWait  = 15 * time.Second
	safariRenderSettle  = 500 * time.Millisecond
	safariLongPress     = 650 * time.Millisecond
	safariMenuWait      = 2500 * time.Millisecond
	safariMenuPoll      = 150 * time.Millisecond
	safariSaveSettle    = 800 * time.Millisecond
	safariCloseSettle   = 250 * time.Millisecond
	safariRestoreSettle = 350 * time.Millisecond
)

// preparedImage is a validated image ready for iOS Safari or UIPasteboard.
type preparedImage struct {
	data        []byte
	contentType string // image/jpeg, image/png, image/gif, image/heic
	ext         string // .jpg, .png, .gif, .heic
}

// prepareImportImage reads, validates, and normalizes an image for iOS.
// JPEG, PNG, GIF and HEIC keep their original bytes; WebP and BMP are decoded
// and re-encoded as PNG so Safari and UIPasteboard always handle them as
// native photos.
func prepareImportImage(name string, r io.Reader, size int64) (preparedImage, error) {
	if size > maxImportMediaBytes {
		return preparedImage{}, fmt.Errorf("ios: %s is larger than %d MB", name, maxImportMediaBytes>>20)
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxImportMediaBytes+1))
	if err != nil {
		return preparedImage{}, fmt.Errorf("ios: read %s: %w", name, err)
	}
	if len(raw) == 0 {
		return preparedImage{}, fmt.Errorf("ios: %s is empty", name)
	}
	if len(raw) > maxImportMediaBytes {
		return preparedImage{}, fmt.Errorf("ios: %s is larger than %d MB", name, maxImportMediaBytes>>20)
	}
	if size >= 0 && int64(len(raw)) != size {
		return preparedImage{}, fmt.Errorf("ios: read %d bytes of %s, expected %d", len(raw), name, size)
	}
	return normalizeIOSImage(name, raw)
}

// normalizeIOSImage validates raw image bytes and converts WebP/BMP to PNG.
func normalizeIOSImage(name string, raw []byte) (preparedImage, error) {
	switch {
	case isHEIC(raw):
		return preparedImage{data: raw, contentType: "image/heic", ext: ".heic"}, nil
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		if _, err := jpeg.DecodeConfig(bytes.NewReader(raw)); err != nil {
			return preparedImage{}, fmt.Errorf("ios: %s is not a valid JPEG: %w", name, err)
		}
		return preparedImage{data: raw, contentType: "image/jpeg", ext: ".jpg"}, nil
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		if _, err := png.DecodeConfig(bytes.NewReader(raw)); err != nil {
			return preparedImage{}, fmt.Errorf("ios: %s is not a valid PNG: %w", name, err)
		}
		return preparedImage{data: raw, contentType: "image/png", ext: ".png"}, nil
	case bytes.HasPrefix(raw, []byte("GIF87a")) || bytes.HasPrefix(raw, []byte("GIF89a")):
		if _, err := gif.DecodeConfig(bytes.NewReader(raw)); err != nil {
			return preparedImage{}, fmt.Errorf("ios: %s is not a valid GIF: %w", name, err)
		}
		return preparedImage{data: raw, contentType: "image/gif", ext: ".gif"}, nil
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		img, err := webp.Decode(bytes.NewReader(raw))
		if err != nil {
			return preparedImage{}, fmt.Errorf("ios: %s is not a valid WebP: %w", name, err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return preparedImage{}, fmt.Errorf("ios: encode %s as PNG: %w", name, err)
		}
		return preparedImage{data: buf.Bytes(), contentType: "image/png", ext: ".png"}, nil
	case len(raw) >= 2 && raw[0] == 'B' && raw[1] == 'M':
		img, err := bmp.Decode(bytes.NewReader(raw))
		if err != nil {
			return preparedImage{}, fmt.Errorf("ios: %s is not a valid BMP: %w", name, err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return preparedImage{}, fmt.Errorf("ios: encode %s as PNG: %w", name, err)
		}
		return preparedImage{data: buf.Bytes(), contentType: "image/png", ext: ".png"}, nil
	default:
		return preparedImage{}, fmt.Errorf("ios: %s is not a supported image (JPEG, PNG, GIF, WebP, BMP or HEIC): %w", name, driver.ErrUnsupported)
	}
}

// isHEIC checks for an ISO BMFF ftyp box with a HEIF/HEIC major or compatible brand.
func isHEIC(b []byte) bool {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return false
	}
	boxLen := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if boxLen < 12 || boxLen > len(b) {
		boxLen = min(len(b), 64)
	}
	isBrand := func(s string) bool {
		switch s {
		case "heic", "heix", "hevc", "hevx", "mif1", "msf1":
			return true
		}
		return false
	}
	if isBrand(string(b[8:12])) {
		return true
	}
	// Compatible brands start at offset 16, 4 bytes each.
	for off := 16; off+4 <= boxLen; off += 4 {
		if isBrand(string(b[off : off+4])) {
			return true
		}
	}
	return false
}

// SetClipboardImage implements driver.ImageClipboard on CoreDevice iOS
// through dtpasteboardd (com.apple.coredevice.pasteboardservice).
func (d *Device) SetClipboardImage(ctx context.Context, mimeType string, data []byte) error {
	return d.gone(d.setClipboardImage(ctx, mimeType, data))
}

func (d *Device) setClipboardImage(ctx context.Context, mimeType string, data []byte) error {
	uti, ok := mimeToPasteboardUTI(mimeType)
	if !ok {
		return fmt.Errorf("ios: clipboard image type %q is not supported: %w", mimeType, driver.ErrUnsupported)
	}
	if len(data) == 0 {
		return errors.New("ios: empty image for clipboard")
	}
	if len(data) > maxClipboardImageBytes {
		return fmt.Errorf("ios: clipboard image is larger than %d MB", maxClipboardImageBytes>>20)
	}
	pb := d.pasteboard()
	if pb == nil {
		return fmt.Errorf("ios: pasteboard service is not available: %w", driver.ErrUnsupported)
	}
	if _, err := pb.set(ctx, imageItems(uti, data)); err != nil {
		return fmt.Errorf("ios: set clipboard image: %w", err)
	}
	return nil
}

// SetClipboardImage implements driver.ImageClipboard through WebDriverAgent's
// /wda/setPasteboard endpoint.
func (d *WDADevice) SetClipboardImage(ctx context.Context, mimeType string, data []byte) error {
	if _, ok := mimeToPasteboardUTI(mimeType); !ok {
		return fmt.Errorf("ios: clipboard image type %q is not supported: %w", mimeType, driver.ErrUnsupported)
	}
	if len(data) == 0 {
		return errors.New("ios: empty image for clipboard")
	}
	if len(data) > maxClipboardImageBytes {
		return fmt.Errorf("ios: clipboard image is larger than %d MB", maxClipboardImageBytes>>20)
	}
	return d.c.do(ctx, d.c.long, http.MethodPost, "/wda/setPasteboard", map[string]any{
		"contentType": "image",
		"content":     base64.StdEncoding.EncodeToString(data),
	}, nil)
}

// ImportMedia implements driver.MediaImporter on iOS 27 CoreDevice: it serves
// the validated image over a one-shot local HTTP server (trying the CoreDevice
// IPv6 tunnel first and falling back to the computer's LAN IPv4), opens it in
// Safari, saves it to Photos via Safari's native context menu, closes the
// temporary tab, and restores the app that was in front.
func (d *Device) ImportMedia(ctx context.Context, name string, r io.Reader, size int64) error {
	return d.gone(d.importMedia(ctx, name, r, size))
}

func (d *Device) importMedia(ctx context.Context, name string, r io.Reader, size int64) error {
	img, err := prepareImportImage(name, r, size)
	if err != nil {
		return err
	}
	if locked, lerr := d.Locked(ctx); lerr == nil && locked {
		return driver.ErrLocked
	}
	prevApp := d.frontmostBundleID(ctx)
	d.log.Info("iphone: importing photo into Photos", "device", d.info.ID, "name", name, "type", img.contentType, "bytes", len(img.data), "prev_app", prevApp)

	// Make sure HID input is ready before switching to Safari.
	d.inputMu.Lock()
	_, herr := d.waitInput(ctx)
	d.inputMu.Unlock()
	if herr != nil {
		return fmt.Errorf("ios: import media: input not ready: %w", herr)
	}

	var openedSafari bool
	defer func() {
		if !openedSafari || prevApp == "" || prevApp == safariID || prevApp == springBoard {
			return
		}
		rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := d.startApp(rctx, prevApp, ""); err != nil {
			d.log.Warn("iphone: could not restore previous app after photo import", "device", d.info.ID, "app", prevApp, "err", err)
			return
		}
		_ = sleepCtx(rctx, safariRestoreSettle)
	}()

	cleanup, err := d.loadImageInSafari(ctx, img, &openedSafari)
	if err != nil {
		return err
	}
	defer cleanup()
	return d.saveSafariImage(ctx)
}

// frontmostBundleID returns the bundle ID of the app currently in front,
// or "" if it cannot be determined.
func (d *Device) frontmostBundleID(ctx context.Context) string {
	if rax, err := d.raxReady(ctx); err == nil {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		front, ferr := rax.frontmostApp(c)
		if ferr == nil && front != nil && front.PID > 0 {
			if app, ok := d.appForPID(c, uint64(front.PID)); ok && app.ID != "" {
				cancel()
				return app.ID
			}
		}
		cancel()
	}
	d.svcMu.Lock()
	id := d.readApp.ID
	d.svcMu.Unlock()
	if id != "" {
		return id
	}
	d.mu.Lock()
	id = d.lastFront.ID
	d.mu.Unlock()
	return id
}

// mediaTransferServer tracks a one-shot HTTP delivery of one image to Safari.
type mediaTransferServer struct {
	token       string
	contentType string
	data        []byte

	hitOnce    sync.Once
	servedOnce sync.Once
	pageHit    chan struct{}
	imgServed  chan struct{}
}

func newMediaTransferServer(token, contentType string, data []byte) *mediaTransferServer {
	return &mediaTransferServer{
		token:       token,
		contentType: contentType,
		data:        data,
		pageHit:     make(chan struct{}),
		imgServed:   make(chan struct{}),
	}
}

func (m *mediaTransferServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	switch r.URL.Path {
	case "/p/" + m.token:
		m.hitOnce.Do(func() { close(m.pageHit) })
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := fmt.Sprintf(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1">
<title>Pinky Photo</title>
<style>html,body{margin:0;padding:0;width:100%%;height:100%%;background:#111;display:flex;align-items:center;justify-content:center;overflow:hidden}
img{max-width:82vw;max-height:68vh;min-width:50vw;min-height:30vh;object-fit:contain;-webkit-touch-callout:default}</style>
</head><body><img id="photo" alt="Pinky Photo" src="/i/%s"></body></html>`, m.token)
		_, _ = io.WriteString(w, html)
	case "/i/" + m.token:
		w.Header().Set("Content-Type", m.contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(m.data)))
		if r.Method == http.MethodGet {
			if _, err := w.Write(m.data); err == nil {
				m.servedOnce.Do(func() { close(m.imgServed) })
			}
		}
	default:
		http.NotFound(w, r)
	}
}

func randomHexToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// loadImageInSafari serves img over a temporary HTTP listener and opens its
// URL in Safari. It tries the CoreDevice IPv6 tunnel first (works over USB
// without Wi-Fi or Windows Firewall prompts) and falls back to the computer's
// LAN IPv4 if iOS does not route Safari traffic through the tunnel.
// On success, the caller must invoke the returned cleanup function after
// Safari finishes saving the image (WebKit may re-read the image URL on save).
func (d *Device) loadImageInSafari(ctx context.Context, img preparedImage, openedSafari *bool) (func(), error) {
	var tunnelErr error
	if !d.safariNoTunnel.Load() {
		l := d.link()
		if l != nil && l.tunnel != nil {
			if ln, err := l.tunnel.ListenTCP(0); err == nil {
				port := ln.Addr().(*net.TCPAddr).Port
				token, terr := randomHexToken(16)
				if terr == nil {
					url := fmt.Sprintf("http://[%s]:%d/p/%s", l.tunnel.HostAddr(), port, token)
					cleanup, err := d.trySafariTransport(ctx, ln, url, token, img, safariTunnelHitWait, openedSafari)
					if err == nil {
						return cleanup, nil
					}
					tunnelErr = err
					if errors.Is(err, errSafariNoHit) {
						d.safariNoTunnel.Store(true)
						d.log.Info("iphone: Safari did not reach CoreDevice tunnel; trying LAN HTTP", "device", d.info.ID)
						// Close the stalled tunnel tab before opening the LAN URL.
						d.closeSafariTab(ctx)
					} else {
						return nil, err
					}
				} else {
					ln.Close()
				}
			} else {
				tunnelErr = err
			}
		}
	}

	lanIP, err := pickLANIPv4(d.deviceLANHint())
	if err != nil {
		if tunnelErr != nil {
			return nil, fmt.Errorf("ios: Safari could not reach Pinky over the CoreDevice tunnel (%v), and no local LAN IPv4 address was found (%v); use phone_files clipboard to copy the photo over USB instead", tunnelErr, err)
		}
		return nil, fmt.Errorf("ios: no local LAN IPv4 address to send photo to Safari (%v); use phone_files clipboard to copy the photo over USB instead", err)
	}
	ln, err := net.Listen("tcp4", net.JoinHostPort(lanIP.String(), "0"))
	if err != nil {
		return nil, fmt.Errorf("ios: listen on LAN %s for photo transfer: %w", lanIP, err)
	}
	token, err := randomHexToken(16)
	if err != nil {
		ln.Close()
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://%s:%d/p/%s", lanIP, port, token)
	cleanup, err := d.trySafariTransport(ctx, ln, url, token, img, safariLANHitWait, openedSafari)
	if err != nil {
		if errors.Is(err, errSafariNoHit) {
			d.closeSafariTab(ctx)
			return nil, fmt.Errorf("ios: Safari on %s did not reach Pinky at %s (make sure the iPhone and computer share a Wi-Fi network and Windows Firewall allows Pinky, or use phone_files clipboard to copy the photo directly over USB)", d.info.Name, lanIP)
		}
		return nil, err
	}
	return cleanup, nil
}

var errSafariNoHit = errors.New("Safari did not fetch the page")

func (d *Device) trySafariTransport(
	ctx context.Context,
	ln net.Listener,
	url, token string,
	img preparedImage,
	hitWait time.Duration,
	openedSafari *bool,
) (func(), error) {
	handler := newMediaTransferServer(token, img.contentType, img.data)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	var closeOnce sync.Once
	cleanup := func() {
		closeOnce.Do(func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
			_ = ln.Close()
		})
	}

	if err := d.startApp(ctx, safariID, url); err != nil {
		cleanup()
		return nil, fmt.Errorf("ios: open photo in Safari: %w", err)
	}
	*openedSafari = true

	hitTimer := time.NewTimer(hitWait)
	defer hitTimer.Stop()
	select {
	case <-handler.imgServed:
		return cleanup, nil
	case <-handler.pageHit:
	case <-hitTimer.C:
		cleanup()
		return nil, errSafariNoHit
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	}

	imgTimer := time.NewTimer(safariImgServeWait)
	defer imgTimer.Stop()
	select {
	case <-handler.imgServed:
		return cleanup, nil
	case <-imgTimer.C:
		cleanup()
		return nil, errors.New("ios: Safari loaded the page but did not finish downloading the image")
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	}
}

// deviceLANHint returns the iPhone's Wi-Fi IPv4 address if usbmuxd lists it,
// so pickLANIPv4 prefers the same subnet.
func (d *Device) deviceLANHint() netip.Addr {
	raw, err := readMuxList()
	if err != nil {
		return netip.Addr{}
	}
	addrs, err := muxAddrs(raw, d.info.ID)
	if err != nil {
		return netip.Addr{}
	}
	for _, a := range addrs {
		if a = a.Unmap(); a.Is4() {
			return a
		}
	}
	return netip.Addr{}
}

// pickLANIPv4 selects a private non-loopback IPv4 address on this computer
// reachable from the iPhone, preferring one in the same /24 subnet as hint.
func pickLANIPv4(hint netip.Addr) (netip.Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return netip.Addr{}, err
	}
	return chooseLANIPv4(ifaces, hint)
}

func chooseLANIPv4(ifaces []net.Interface, hint netip.Addr) (netip.Addr, error) {
	var best netip.Addr
	bestScore := -1
	hint4 := hint.Is4()
	var hintB [4]byte
	if hint4 {
		hintB = hint.As4()
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		lowerName := strings.ToLower(iface.Name)
		virtual := strings.Contains(lowerName, "vethernet") ||
			strings.Contains(lowerName, "docker") ||
			strings.Contains(lowerName, "wsl") ||
			strings.Contains(lowerName, "virtualbox") ||
			strings.Contains(lowerName, "vmware") ||
			strings.Contains(lowerName, "hyper-v") ||
			strings.Contains(lowerName, "tailscale") ||
			strings.Contains(lowerName, "zerotier") ||
			strings.Contains(lowerName, "wireguard") ||
			strings.Contains(lowerName, "wintun") ||
			strings.Contains(lowerName, "clash") ||
			strings.Contains(lowerName, "mihomo") ||
			strings.Contains(lowerName, "sing-box") ||
			strings.Contains(lowerName, "vpn") ||
			strings.Contains(lowerName, "utun")
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipNet.IP)
			if !ok {
				continue
			}
			addr = addr.Unmap()
			if !addr.Is4() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
				continue
			}
			score := 1
			if addr.IsPrivate() {
				score += 10
			}
			if !virtual {
				score += 20
			}
			if hint4 {
				b := addr.As4()
				if b[0] == hintB[0] && b[1] == hintB[1] && b[2] == hintB[2] {
					score += 100
				} else if ipNet.Contains(net.IP(hintB[:])) {
					score += 80
				}
			}
			if score > bestScore {
				bestScore = score
				best = addr
			}
		}
	}
	if bestScore < 0 {
		return netip.Addr{}, errors.New("no active IPv4 network interface")
	}
	return best, nil
}

// safariSaveOps injects UI primitives for saveSafariImageWith so unit tests
// can exercise the full Safari long-press -> Save to Photos -> Cmd+W flow.
type safariSaveOps struct {
	screenInfo  func(context.Context) screenInfo
	walk        func(context.Context) ([]raxElement, error)
	longPress   func(context.Context, screen.Point, time.Duration) error
	tap         func(context.Context, screen.Point) error
	dismissMenu func(context.Context)
	closeTab    func(context.Context)
	sleep       func(context.Context, time.Duration) error
}

func (d *Device) saveSafariImage(ctx context.Context) error {
	if err := sleepCtx(ctx, safariRenderSettle); err != nil {
		return err
	}
	rax, err := d.raxReady(ctx)
	if err != nil {
		return fmt.Errorf("ios: accessibility needed to save photo in Safari: %w", err)
	}
	d.inputMu.Lock()
	defer d.inputMu.Unlock()

	lim := d.walkLimits()
	if lim.MaxElements < 550 {
		lim.MaxElements = 550
	}
	if lim.Budget < 2500*time.Millisecond {
		lim.Budget = 2500 * time.Millisecond
	}

	ops := safariSaveOps{
		screenInfo: func(c context.Context) screenInfo { return d.freshDisplay(c).upright() },
		walk: func(c context.Context) ([]raxElement, error) {
			els, _, werr := rax.walk(c, lim)
			return els, werr
		},
		longPress: d.longPressLocked,
		tap:       d.tapLocked,
		dismissMenu: func(c context.Context) {
			ec, cancel := context.WithTimeout(c, time.Second)
			defer cancel()
			_ = d.pressKeys(ec, []keyStroke{{usage: keyEscape}})
			_ = sleepCtx(ec, 150*time.Millisecond)
		},
		closeTab: func(c context.Context) { d.closeSafariTabLocked(c) },
		sleep:    sleepCtx,
	}
	return saveSafariImageWith(ctx, ops, safariMenuWait, safariMenuPoll, safariSaveSettle)
}

func (d *Device) closeSafariTab(ctx context.Context) {
	d.inputMu.Lock()
	defer d.inputMu.Unlock()
	d.closeSafariTabLocked(ctx)
}

func (d *Device) closeSafariTabLocked(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = d.pressKeys(c, []keyStroke{{usage: keyW, mod: keyLeftGUI}})
	d.releaseKeyboard()
	_ = sleepCtx(c, safariCloseSettle)
}

func saveSafariImageWith(
	ctx context.Context,
	ops safariSaveOps,
	menuWait, menuPoll, saveSettle time.Duration,
) error {
	defer ops.closeTab(ctx)

	si := ops.screenInfo(ctx)
	if si.Width <= 0 || si.Height <= 0 {
		return errors.New("ios: unknown screen size while saving photo in Safari")
	}

	var savePt screen.Point
	var found bool

	for attempt := 0; attempt < 2 && !found; attempt++ {
		var pressPt screen.Point
		if attempt == 0 {
			if els, err := ops.walk(ctx); err == nil {
				pressPt = findImportImagePoint(els, si)
			} else {
				pressPt = screen.Point{X: si.Width / 2, Y: si.Height / 2}
			}
		} else {
			pressPt = screen.Point{X: si.Width / 2, Y: si.Height * 46 / 100}
		}
		if err := ops.longPress(ctx, pressPt, safariLongPress); err != nil {
			return fmt.Errorf("ios: long-press photo in Safari: %w", err)
		}
		deadline := time.Now().Add(menuWait)
		for {
			if els, err := ops.walk(ctx); err == nil {
				if pt, ok := findSaveToPhotosMenuItem(els, si); ok {
					savePt, found = pt, true
					break
				}
			}
			if !time.Now().Before(deadline) {
				break
			}
			if err := ops.sleep(ctx, menuPoll); err != nil {
				return err
			}
		}
		if !found && attempt == 0 {
			if ops.dismissMenu != nil {
				ops.dismissMenu(ctx)
			}
			if err := ops.sleep(ctx, 400*time.Millisecond); err != nil {
				return err
			}
		}
	}
	if !found {
		if ops.dismissMenu != nil {
			ops.dismissMenu(ctx)
		}
		return errors.New("ios: Safari did not show a Save to Photos menu after long-pressing the image")
	}
	if err := ops.tap(ctx, savePt); err != nil {
		return fmt.Errorf("ios: tap Save to Photos: %w", err)
	}
	if saveSettle > 0 {
		if err := ops.sleep(ctx, saveSettle); err != nil {
			return err
		}
	}
	return nil
}

// findImportImagePoint locates the rendered <img alt="Pinky Photo"> in
// Safari's accessibility tree, falling back to the center of the screen.
func findImportImagePoint(els []raxElement, si screenInfo) screen.Point {
	fallback := screen.Point{X: si.Width / 2, Y: si.Height / 2}
	if len(els) == 0 {
		return fallback
	}
	scale := raxScale(els[0], si)
	if scale <= 0 {
		scale = si.Scale
	}
	if scale <= 0 {
		return fallback
	}
	turned := turnRaxFrames(els, si, scale)
	for _, el := range turned {
		if !el.Visible || !el.HasFrame || el.Frame.W < 40 || el.Frame.H < 40 {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(el.Label), "Pinky Photo") &&
			!strings.EqualFold(strings.TrimSpace(el.Value), "Pinky Photo") {
			continue
		}
		if pt, ok := axpRectToPoint(el.Frame, si, scale); ok {
			return pt
		}
	}
	return fallback
}

// findSaveToPhotosMenuItem searches a RemoteAX element walk for Safari's
// native "Save to Photos" / "存储到“照片”" / "“写真”に保存" context menu button.
func findSaveToPhotosMenuItem(els []raxElement, si screenInfo) (screen.Point, bool) {
	if len(els) == 0 {
		return screen.Point{}, false
	}
	scale := raxScale(els[0], si)
	if scale <= 0 {
		scale = si.Scale
	}
	if scale <= 0 {
		return screen.Point{}, false
	}
	turned := turnRaxFrames(els, si, scale)
	for _, el := range turned {
		if !el.Visible || !el.HasFrame || el.Frame.W < 20 || el.Frame.H < 12 {
			continue
		}
		if el.Type == xcKey || el.Traits&traitKeyboardKey != 0 {
			continue
		}
		if !isSaveToPhotosLabel(el.Label) && !isSaveToPhotosLabel(el.Value) && !isSaveToPhotosLabel(el.ValueText) {
			continue
		}
		if pt, ok := axpRectToPoint(el.Frame, si, scale); ok {
			return pt, true
		}
	}
	return screen.Point{}, false
}

// isSaveToPhotosLabel matches Safari's localized "Save to Photos" / "Add to
// Photos" / "Save Image" context menu actions across common iOS languages,
// while rejecting "Save to Files" / "Copy" / "Share".
func isSaveToPhotosLabel(s string) bool {
	norm := normalizeMenuLabel(s)
	if norm == "" {
		return false
	}
	switch norm {
	case "savetophotos", "addtophotos", "saveimage", "savephoto",
		"存储到照片", "添加到照片", "存储图像", "保存到照片", "保存到相册", "储存到照片",
		"儲存至照片", "加入照片", "儲存影像", "儲存到照片",
		"写真に保存", "写真に追加", "画像を保存",
		"사진에저장",
		"guardarenfotos", "añadirafotos",
		"enregistrerdansphotos", "ajouteràphotos",
		"infotossichern", "zufotoshinzufügen":
		return true
	}
	return false
}

func normalizeMenuLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsSpace(r):
			continue
		case r == '"' || r == '\'' || r == '“' || r == '”' || r == '„' || r == '‘' || r == '’' ||
			r == '«' || r == '»' || r == '「' || r == '」' || r == '『' || r == '』' || r == '《' || r == '》' || r == '·':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var (
	_ driver.MediaImporter  = (*Device)(nil)
	_ driver.ImageClipboard = (*Device)(nil)
	_ driver.ImageClipboard = (*WDADevice)(nil)
)

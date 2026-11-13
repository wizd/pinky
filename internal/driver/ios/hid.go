package ios

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	goios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/xpc"
)

// HID over CoreDevice. Wire formats were worked out by go-ios (ios/hid, MIT)
// and pymobiledevice3 (as a protocol reference only; no code is taken from
// it). The device applies input only while a media stream from this host is
// running: without one it accepts every report and drops it without an error.
// It never acknowledges a report either, so a nil error means "sent".

const (
	universalHIDService = "com.apple.coredevice.hid.universalhidservice"
	indigoHIDService    = "com.apple.coredevice.hid.indigo"

	featureUniversalHID = "com.apple.coredevice.feature.remote.universalhidservice"
	featureHIDButton    = "com.apple.coredevice.feature.remote.hid.button"

	// surfaceTouchscreen is the service id of the built-in touchscreen.
	surfaceTouchscreen = uint64(257)
	// surfaceKeyboard is the id we ask for when registering a virtual
	// keyboard. The high bit marks a host-provided, session-specific surface.
	surfaceKeyboard = uint64(0x100002001)

	touchReportLen    = 58
	keyboardReportLen = 39

	// touchIdentity is the contact identity Xcode uses for one finger.
	touchIdentity = 2
	touchContact  = 0xC0 | touchIdentity // touching and in range
	touchRelease  = touchIdentity

	buttonDown = 1
	buttonUp   = 2
)

var hidEpoch = time.Now()

// hidTimestamp stamps a report in the phone's mach time units: 24 MHz ticks
// on every iPhone that runs iOS 27. Nanoseconds would make every interval
// look 41.7 times longer: a 12 ms key press reads as half a second and opens
// the press-and-hold accent menu. Only deltas matter, so the origin is the
// first report. The field is 48 bits wide.
func hidTimestamp() uint64 {
	ns := uint64(time.Since(hidEpoch).Nanoseconds())
	return (ns / 125 * 3) & (1<<48 - 1)
}

func put48(b []byte, v uint64) {
	var full [8]byte
	binary.LittleEndian.PutUint64(full[:], v)
	copy(b, full[:6])
}

// touchReport is a 58-byte main-touchscreen report with one contact.
// Layout: report id 0x09, contact count, max contacts (5), then five 5-byte
// contact slots [state, x u16le, y u16le], 12 reserved bytes, the constant
// 02 00 00 00, a 48-bit timestamp and 8 reserved bytes. Positions are
// 0..65535 on each axis of the panel, independent of resolution.
func touchReport(state byte, x, y uint16, ts uint64) []byte {
	r := make([]byte, touchReportLen)
	r[0] = 0x09
	r[1] = 1
	r[2] = 5
	r[3] = state
	binary.LittleEndian.PutUint16(r[4:6], x)
	binary.LittleEndian.PutUint16(r[6:8], y)
	r[40] = 0x02
	put48(r[44:50], ts)
	return r
}

// keyboardReport is a 39-byte virtual keyboard report: report id 0x01, a
// 240-bit bitmap of the HID keyboard usages (page 7) held down, a 48-bit
// timestamp and two reserved bytes. Every report carries the full set of
// pressed keys.
func keyboardReport(usages []uint8, ts uint64) []byte {
	r := make([]byte, keyboardReportLen)
	r[0] = 0x01
	for _, u := range usages {
		if u < 240 {
			r[1+u/8] |= 1 << (u % 8)
		}
	}
	put48(r[31:37], ts)
	return r
}

// keyboardDescriptor declares a boot keyboard. The reports we send do not
// follow its layout; dtuhidd accepts that, as it does for macOS Universal
// Control, and the descriptor only makes the surface count as a keyboard.
var keyboardDescriptor = []byte{
	0x05, 0x01, 0x09, 0x06, 0xA1, 0x01, // Generic Desktop, Keyboard, Application
	0x05, 0x07, 0x19, 0xE0, 0x29, 0xE7, 0x15, 0x00, 0x25, 0x01, 0x95, 0x08, 0x75, 0x01, 0x81, 0x02, // modifiers
	0x95, 0x01, 0x75, 0x08, 0x81, 0x01, // reserved byte
	0x05, 0x07, 0x19, 0x00, 0x29, 0xFF, 0x15, 0x00, 0x26, 0xFF, 0x00, 0x95, 0x06, 0x75, 0x08, 0x81, 0x00, // 6 keys
	0x05, 0x08, 0x19, 0x01, 0x29, 0x05, 0x15, 0x00, 0x25, 0x01, 0x95, 0x05, 0x75, 0x01, 0x91, 0x02, // LEDs
	0x95, 0x01, 0x75, 0x03, 0x91, 0x01, 0xC0,
}

// xpcLink is one RemoteXPC connection whose replies are drained in the
// background, so that unread messages never stall the HTTP/2 stream.
type xpcLink struct {
	name    string
	mu      sync.Mutex
	conn    *xpc.Connection
	replies chan map[string]any
	done    chan struct{}
	errMu   sync.Mutex
	err     error
}

func dialXPC(entry goios.DeviceEntry, name string) (*xpcLink, error) {
	conn, err := dialXPCConn(entry, name)
	if err != nil {
		return nil, err
	}
	x := &xpcLink{name: name, conn: conn, replies: make(chan map[string]any, 16), done: make(chan struct{})}
	go x.read()
	return x, nil
}

func (x *xpcLink) read() {
	defer close(x.done)
	for {
		m, err := x.conn.ReceiveOnServerClientStream()
		if err != nil {
			x.errMu.Lock()
			x.err = err
			x.errMu.Unlock()
			return
		}
		select {
		case x.replies <- m:
		default: // nobody waits for it
		}
	}
}

func (x *xpcLink) send(m map[string]any, wantReply bool) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	select {
	case <-x.done:
		x.errMu.Lock()
		defer x.errMu.Unlock()
		return fmt.Errorf("%s: connection closed: %v", x.name, x.err)
	default:
	}
	var err error
	if wantReply {
		err = x.conn.Send(m, xpc.HeartbeatRequestFlag) // the "wants reply" flag
	} else {
		err = x.conn.Send(m)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", x.name, err)
	}
	return nil
}

// request sends m and waits for the next reply.
func (x *xpcLink) request(ctx context.Context, m map[string]any) (map[string]any, error) {
	for {
		select {
		case <-x.replies: // a stale reply from an earlier message
			continue
		default:
		}
		break
	}
	if err := x.send(m, true); err != nil {
		return nil, err
	}
	select {
	case r := <-x.replies:
		return r, nil
	case <-x.done:
		return nil, fmt.Errorf("%s: connection closed", x.name)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (x *xpcLink) Close() error { return x.conn.Close() }

// alive reports whether the connection still reads.
func (x *xpcLink) alive() bool {
	select {
	case <-x.done:
		return false
	default:
		return true
	}
}

// hidSession owns the HID connections of one device.
type hidSession struct {
	entry goios.DeviceEntry

	mu       sync.Mutex
	touch    *xpcLink
	buttons  *xpcLink
	keyboard uint64   // registered keyboard service id, 0 if none
	kb       *xpcLink // the connection that registered it; nil if none
	spare    *xpcLink // a connection dialed ahead for the next keyboard
	dialing  bool     // a spare is being dialed
	closed   bool
	down     bool
	last     [2]uint16
	held     *[2]uint64 // a button held down: usage page and usage
}

// alive reports whether the touch connection still works.
func (h *hidSession) alive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.touch != nil && h.touch.alive()
}

// liftHeld lifts a contact that is still down.
func (h *hidSession) liftHeld() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.down {
		return nil
	}
	return h.touchLocked(false, h.last[0], h.last[1])
}

func newHIDSession(entry goios.DeviceEntry) (*hidSession, error) {
	t, err := dialXPC(entry, universalHIDService)
	if err != nil {
		return nil, err
	}
	return &hidSession{entry: entry, touch: t}, nil
}

func universalReport(service uint64, report []byte) map[string]any {
	return map[string]any{
		"featureIdentifier": featureUniversalHID,
		"messageType":       "Request",
		"payload": map[string]any{
			"send": map[string]any{"_0": report, "_1": service},
		},
	}
}

// touchAt reports one contact (down) or its release (up) at panel position x, y.
func (h *hidSession) touchAt(down bool, x, y uint16) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.touchLocked(down, x, y)
}

func (h *hidSession) touchLocked(down bool, x, y uint16) error {
	state := byte(touchContact)
	if !down {
		if !h.down {
			return nil
		}
		state = touchRelease
	}
	if err := h.touch.send(universalReport(surfaceTouchscreen, touchReport(state, x, y, hidTimestamp())), false); err != nil {
		return err
	}
	h.down = down
	h.last = [2]uint16{x, y}
	return nil
}

// button presses a hardware button by HID usage and holds it for hold.
func (h *hidSession) button(ctx context.Context, page, usage uint64, hold time.Duration) error {
	_, err := h.buttonListen(ctx, page, usage, hold, 0)
	return err
}

// buttonAnswer is what the phone answered to a button press that Pinky
// listened for (buttonListen).
type buttonAnswer struct {
	Replied bool
	Reply   string // the reply, shortened for the log
	Err     error  // the phone said the press failed (hidReplyError)
}

// buttonListen is button that, when listen is above zero, asks the phone
// to answer the press and waits up to listen from the press for that
// answer. dtuhidd hands an Indigo button to "the main screen button
// service" and, when it finds none (its log: "No main screen button
// service found for button event"), drops the press; whether it says so
// in a reply is not known, so no answer is not a failure.
func (h *hidSession) buttonListen(ctx context.Context, page, usage uint64, hold, listen time.Duration) (buttonAnswer, error) {
	h.mu.Lock()
	if h.buttons != nil && !h.buttons.alive() {
		h.buttons.Close()
		h.buttons = nil
	}
	if h.buttons == nil {
		b, err := dialXPC(h.entry, indigoHIDService)
		if err != nil {
			h.mu.Unlock()
			return buttonAnswer{}, err
		}
		h.buttons = b
	}
	b := h.buttons
	if listen > 0 {
		drainReplies(b.replies)
	}
	start := time.Now()
	if err := b.send(buttonEvent(page, usage, buttonDown), listen > 0); err != nil {
		h.mu.Unlock()
		return buttonAnswer{}, err
	}
	h.held = &[2]uint64{page, usage}
	h.mu.Unlock()
	var replies <-chan map[string]any
	if listen > 0 {
		replies = b.replies
	}
	reply, replied := holdAndListen(ctx, replies, hold)
	if replied { // the button stays down for its whole hold
		holdAndListen(ctx, nil, hold-time.Since(start))
	}
	// The release goes out even when ctx ended: a held button must come up.
	h.mu.Lock()
	var err error
	if h.held != nil { // else Close released it
		h.held = nil
		err = b.send(buttonEvent(page, usage, buttonUp), false)
	}
	h.mu.Unlock()
	if listen > 0 && !replied {
		reply, replied = holdAndListen(ctx, replies, listen-time.Since(start))
	}
	var ans buttonAnswer
	if replied {
		ans = buttonAnswer{Replied: true, Reply: shortReply(reply), Err: hidReplyError(reply)}
	}
	return ans, err
}

// holdAndListen waits for d, or until ctx ends, and returns the first
// message from replies in that time (a nil replies listens to nothing).
func holdAndListen(ctx context.Context, replies <-chan map[string]any, d time.Duration) (map[string]any, bool) {
	if d <= 0 {
		select {
		case r := <-replies:
			return r, true
		default:
			return nil, false
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case r := <-replies:
		return r, true
	case <-t.C:
	case <-ctx.Done():
	}
	return nil, false
}

// drainReplies drops the replies to earlier messages.
func drainReplies(replies chan map[string]any) {
	for {
		select {
		case <-replies:
		default:
			return
		}
	}
}

// hidReplyError is the failure a reply of a HID service reports, nil when
// it reports none. The HID services' replies are not documented: an error
// may come as "error" (as the keyboard's registration answers) or as
// CoreDevice's "CoreDevice.error", at the top or in the payload, or as a
// message type that names an error.
func hidReplyError(m map[string]any) error {
	if m == nil {
		return nil
	}
	for _, k := range []string{"error", "CoreDevice.error"} {
		if e, ok := m[k]; ok && e != nil {
			if em, ok := e.(map[string]any); ok {
				return decodeCoreDeviceError(em)
			}
			return fmt.Errorf("the phone answered: %v", e)
		}
	}
	if p, ok := m["payload"].(map[string]any); ok {
		if err := hidReplyError(p); err != nil {
			return err
		}
	}
	if mt, _ := m["messageType"].(string); strings.Contains(strings.ToLower(mt), "error") {
		return fmt.Errorf("the phone answered %s: %s", mt, shortReply(m))
	}
	return nil
}

// shortReply is a reply as the log shows it.
func shortReply(m map[string]any) string {
	s := fmt.Sprintf("%v", m)
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// probeHIDServices asks the universal HID service, on a connection of its
// own, which HID services it knows (dtuhidd's log calls the answer
// "connectedServiceDescriptors returning serviceIDs"). The request's shape
// is a guess after the service's other requests ("send", "createService");
// the answer, or that none came, is only for the log: a phone with two
// screens may list a touchscreen for each.
func probeHIDServices(ctx context.Context, entry goios.DeviceEntry) (string, error) {
	l, err := dialXPC(entry, universalHIDService)
	if err != nil {
		return "", err
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, err := l.request(ctx, map[string]any{
		"featureIdentifier": featureUniversalHID,
		"messageType":       "Request",
		"payload":           map[string]any{"connectedServiceDescriptors": map[string]any{}},
	})
	if err != nil {
		return "", err
	}
	return shortReply(r), hidReplyError(r)
}

func buttonEvent(page, usage, state uint64) map[string]any {
	return map[string]any{
		"messageType":       "IndigoButtonEvent",
		"featureIdentifier": featureHIDButton,
		"payload": map[string]any{
			"state":     state,
			"usagePage": page,
			"usageCode": usage,
		},
	}
}

// ensureKeyboard registers a virtual keyboard with the device, unless one
// is registered already. While it is attached iOS treats the phone as
// having a hardware keyboard and hides the on-screen one, for the person
// too (GitHub issue #65), so callers drop it again once their keys are done
// (dropKeyboard). Nothing removes a registered service but closing the
// connection that registered it, so the keyboard gets a connection of its
// own, apart from touches: the touch connection when a second one cannot
// be dialed.
func (h *hidSession) ensureKeyboard(ctx context.Context) (uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.keyboard != 0 && h.kb != nil && h.kb.alive() {
		return h.keyboard, nil
	}
	h.forgetKeyboardLocked()
	l := h.spare
	h.spare = nil
	if l != nil && !l.alive() {
		l.Close()
		l = nil
	}
	if l == nil {
		var err error
		if l, err = dialXPC(h.entry, universalHIDService); err != nil {
			l = h.touch
		}
	}
	usagePage, usage := int64(1), int64(6)
	storage := map[string]any{
		"Manufacturer":     map[string]any{"string": "Pinky"},
		"Product":          map[string]any{"string": "Pinky virtual keyboard"},
		"ProductID":        map[string]any{"int": int64(0x0250)},
		"VendorID":         map[string]any{"int": int64(0x05AC)},
		"PrimaryUsage":     map[string]any{"int": usage},
		"PrimaryUsagePage": map[string]any{"int": usagePage},
		"DeviceUsagePairs": map[string]any{"array": []any{
			map[string]any{"dictionary": map[string]any{
				"DeviceUsage":     map[string]any{"int": usage},
				"DeviceUsagePage": map[string]any{"int": usagePage},
			}},
		}},
		"Transport":                      map[string]any{"string": "USB"},
		"ReportDescriptor":               map[string]any{"data": keyboardDescriptor},
		"UniversalControlVirtualService": map[string]any{"bool": true},
		"_ServiceID":                     map[string]any{"uint": surfaceKeyboard},
	}
	req := map[string]any{
		"featureIdentifier": featureUniversalHID,
		"messageType":       "Request",
		"payload": map[string]any{
			"createService": map[string]any{
				"_0": map[string]any{
					"DeviceUsagePairs":                   []any{map[string]any{"DeviceUsage": usage, "DeviceUsagePage": usagePage}},
					"PrimaryUsage":                       uint64(usage),
					"PrimaryUsagePage":                   uint64(usagePage),
					"Product":                            "Pinky virtual keyboard",
					"ProductID":                          int64(0x0250),
					"VendorID":                           int64(0x05AC),
					"_CoreDevice_codablePropertyStorage": storage,
					"_ServiceID":                         surfaceKeyboard,
				},
			},
		},
	}
	fail := func(err error) (uint64, error) {
		if l != h.touch {
			l.Close()
		}
		return 0, err
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	resp, err := l.request(rctx, req)
	cancel()
	if err != nil {
		return fail(fmt.Errorf("register keyboard: %w", err))
	}
	id := surfaceKeyboard
	if v := toInt64(resp["serviceID"]); v != 0 {
		id = uint64(v)
	}
	if e, ok := resp["error"]; ok {
		return fail(fmt.Errorf("register keyboard: %v", e))
	}
	// iOS attaches the keyboard asynchronously and nothing reports when it
	// is done; a Shift sent too early stays latched.
	time.Sleep(300 * time.Millisecond)
	h.keyboard, h.kb = id, l
	return id, nil
}

// forgetKeyboardLocked lets go of a keyboard whose connection dropped (iOS
// removed it with the connection). Callers hold mu.
func (h *hidSession) forgetKeyboardLocked() {
	if h.kb != nil && !h.kb.alive() {
		if h.kb != h.touch {
			h.kb.Close()
		}
		h.keyboard, h.kb = 0, nil
	}
}

// keys sends one keyboard report with the given usages held.
func (h *hidSession) keys(id uint64, usages []uint8) error {
	h.mu.Lock()
	l := h.kb
	h.mu.Unlock()
	if l == nil {
		return errors.New("virtual keyboard: not registered")
	}
	return l.send(universalReport(id, keyboardReport(usages, hidTimestamp())), false)
}

// dropKeyboard removes the virtual keyboard, so that iOS shows its
// on-screen keyboard again, and dials a connection for the next one in the
// background, which saves the next key the handshake. A keyboard
// registered on the touch connection goes with that connection, which the
// device reconnects at the next input (Device.live); not while a contact
// is down.
func (h *hidSession) dropKeyboard() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.keyboard == 0 || h.kb == nil {
		return nil
	}
	if h.kb == h.touch && h.down {
		return nil
	}
	err := h.kb.send(universalReport(h.keyboard, keyboardReport(nil, hidTimestamp())), false)
	err = errors.Join(err, h.kb.Close())
	h.keyboard, h.kb = 0, nil
	if !h.closed && h.spare == nil && !h.dialing && h.touch.alive() {
		h.dialing = true
		go h.dialSpare()
	}
	return err
}

// dialSpare dials the connection the next keyboard registers on.
func (h *hidSession) dialSpare() {
	l, err := dialXPC(h.entry, universalHIDService)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dialing = false
	if err != nil {
		return
	}
	if h.closed || h.spare != nil {
		l.Close()
		return
	}
	h.spare = l
}

// Close lifts a held contact and closes the connections.
func (h *hidSession) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var errs []error
	if h.down {
		errs = append(errs, h.touchLocked(false, h.last[0], h.last[1]))
	}
	h.closed = true
	if h.keyboard != 0 && h.kb != nil {
		errs = append(errs, h.kb.send(universalReport(h.keyboard, keyboardReport(nil, hidTimestamp())), false))
		if h.kb != h.touch {
			errs = append(errs, h.kb.Close())
		}
		h.keyboard, h.kb = 0, nil
	}
	if h.spare != nil {
		h.spare.Close()
		h.spare = nil
	}
	if h.held != nil && h.buttons != nil {
		errs = append(errs, h.buttons.send(buttonEvent(h.held[0], h.held[1], buttonUp), false))
		h.held = nil
	}
	errs = append(errs, h.touch.Close())
	if h.buttons != nil {
		errs = append(errs, h.buttons.Close())
	}
	return errors.Join(errs...)
}

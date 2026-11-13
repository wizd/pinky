package onphone

import (
	"context"
	"fmt"
	"sync"

	"github.com/DIYgod/pinky/internal/driver"
)

// Provider lists the one iPhone: the phone the core runs on, online while
// the connection loop holds a Device over the phone's own loopback.
type Provider struct {
	id string

	mu     sync.Mutex
	dev    driver.Device // the phone behind the guard, which the gateway drives
	raw    phone         // the same phone past the guard, for the core's own moves
	state  driver.State
	reason string
	detail string
}

// phone is what the core itself does on the phone, past the guard: leave
// Pinky's app and come back to the task's.
type phone interface {
	Key(ctx context.Context, k driver.Key) error
	LaunchID(ctx context.Context, bundleID string) error
}

func newProvider(id string) *Provider {
	return &Provider{id: id, state: driver.StateOffline, reason: ReasonConnecting, detail: "Connecting to this iPhone…"}
}

func (p *Provider) Platform() driver.Platform { return driver.IOS }

func (p *Provider) List(ctx context.Context) ([]driver.Info, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dev != nil {
		info := p.dev.Info()
		info.ID, info.State, info.Detail = p.id, driver.StateOnline, ""
		return []driver.Info{info}, nil
	}
	return []driver.Info{{ID: p.id, Platform: driver.IOS, Name: "This iPhone", Connection: connection,
		Backend: backend, State: p.state, Detail: p.detail}}, nil
}

func (p *Provider) Open(ctx context.Context, id string) (driver.Device, error) {
	if id != p.id {
		return nil, fmt.Errorf("onphone: no phone %q: %w", id, driver.ErrNotFound)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dev == nil {
		return nil, fmt.Errorf("onphone: %s: %w", p.detail, driver.ErrOffline)
	}
	return p.dev, nil
}

// set makes dev the phone the gateway opens, raw the same phone past the
// guard; or, with dev nil, takes the phone offline in state, for reason,
// with detail saying why.
func (p *Provider) set(dev driver.Device, raw phone, state driver.State, reason, detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if dev == nil {
		p.dev, p.raw, p.state, p.reason, p.detail = nil, nil, state, reason, detail
		return
	}
	p.dev, p.raw, p.state, p.reason, p.detail = dev, raw, driver.StateOnline, "", ""
}

// why is the reason the phone is not connected, or "".
func (p *Provider) why() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reason
}

// phone is the phone past the guard while it is connected, or nil.
func (p *Provider) phone() phone {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.raw
}

// What the gateway shows of the phone's connection.
const (
	connection = "loopback"
	backend    = "remotepairing"
)

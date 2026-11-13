package wadb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// Manager keeps the app's one connection to its own phone's adbd for the
// core: whether the person turned the feature on, whether the app's key
// is paired, the port Wireless debugging listens on, and the connection
// itself, opened when a task needs it and closed when the person turns
// the feature off. It never turns Wireless debugging on: when the
// connection cannot be made, State says what the person has to do.
type Manager struct {
	dir  string // the key and the settings, in the app's private storage
	name string // how the phone lists the pairing

	mu       sync.Mutex
	cfg      config
	key      *Key
	conn     *Conn
	lastErr  error
	changed  func()
	dialHook func(ctx context.Context, addr string, key *Key) (*Conn, error)
}

// config is what the manager keeps between runs, next to the key.
type config struct {
	On     bool   `json:"on"`
	Paired bool   `json:"paired"`
	GUID   string `json:"guid,omitempty"` // the phone's adb GUID from the pairing
	Port   int    `json:"port,omitempty"` // Wireless debugging's last port
}

const configFile = "wadb.json"

// Status is what the screens show of the feature.
type Status string

// Statuses, in the order the person meets them.
const (
	StatusOff       Status = "off"       // the person has not turned it on
	StatusUnpaired  Status = "unpaired"  // on, but Pinky's key is not paired yet
	StatusNoPort    Status = "no_port"   // paired; Wireless debugging seems off (no port seen)
	StatusReady     Status = "ready"     // paired, a port is known; connects when a task needs it
	StatusConnected Status = "connected" // connected: Pinky holds shell now
	StatusRefused   Status = "refused"   // the phone forgot Pinky's key: pair again
	StatusFailed    Status = "failed"    // the last connection failed; Error says why
)

// State is the feature's state for the screens.
type State struct {
	On     bool   `json:"on"`
	Status Status `json:"status"`
	Port   int    `json:"port,omitempty"`
	Error  string `json:"error,omitempty"`
}

// NewManager loads the manager's settings from dir; the key is made when
// the person first pairs. changed, when set, is called after each change
// of State.
func NewManager(dir, name string, changed func()) *Manager {
	m := &Manager{dir: dir, name: name, changed: changed, dialHook: Dial}
	if b, err := os.ReadFile(filepath.Join(dir, configFile)); err == nil {
		_ = json.Unmarshal(b, &m.cfg)
	}
	return m
}

func (m *Manager) saveLocked() error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(m.cfg)
	tmp := filepath.Join(m.dir, configFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(m.dir, configFile))
}

func (m *Manager) notify() {
	if m.changed != nil {
		m.changed()
	}
}

// State says where the feature stands.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateLocked()
}

func (m *Manager) stateLocked() State {
	s := State{On: m.cfg.On, Port: m.cfg.Port}
	switch {
	case !m.cfg.On:
		s.Status = StatusOff
	case !m.cfg.Paired:
		s.Status = StatusUnpaired
	case m.conn != nil && m.conn.Err() == nil:
		s.Status = StatusConnected
	case errors.Is(m.lastErr, ErrUnauthorized):
		s.Status = StatusRefused
	case m.cfg.Port == 0:
		s.Status = StatusNoPort
	case m.lastErr != nil:
		s.Status = StatusFailed
	default:
		s.Status = StatusReady
	}
	if m.lastErr != nil && s.Status != StatusOff && s.Status != StatusConnected {
		s.Error = m.lastErr.Error()
	}
	return s
}

// On reports whether the person turned the feature on.
func (m *Manager) On() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.On
}

// SetOn turns the feature on or off. Off drops the connection at once, so
// that Pinky holds no shell while the feature is off; the pairing stays,
// for turning it on again without a code.
func (m *Manager) SetOn(on bool) error {
	m.mu.Lock()
	m.cfg.On = on
	err := m.saveLocked()
	var c *Conn
	if !on {
		c, m.conn = m.conn, nil
		m.lastErr = nil
	}
	m.mu.Unlock()
	if c != nil {
		c.Close()
	}
	m.notify()
	return err
}

// Forget drops the connection and the key, so that the phone's list of
// paired devices holds a key nobody has any more; pairing again makes a
// new one.
func (m *Manager) Forget() error {
	m.mu.Lock()
	c := m.conn
	m.conn, m.key, m.lastErr = nil, nil, nil
	m.cfg.Paired, m.cfg.GUID = false, ""
	err := errors.Join(RemoveKey(m.dir), m.saveLocked())
	m.mu.Unlock()
	if c != nil {
		c.Close()
	}
	m.notify()
	return err
}

// SetPort records the port Wireless debugging listens on (the app finds
// it with Android's network service discovery); 0 says it is gone, which
// drops the connection too.
func (m *Manager) SetPort(port int) error {
	m.mu.Lock()
	if port == m.cfg.Port {
		m.mu.Unlock()
		return nil
	}
	m.cfg.Port = port
	var c *Conn
	if port == 0 {
		c, m.conn = m.conn, nil
	}
	m.lastErr = nil
	err := m.saveLocked()
	m.mu.Unlock()
	if c != nil {
		c.Close()
	}
	m.notify()
	return err
}

func (m *Manager) keyLocked() (*Key, error) {
	if m.key != nil {
		return m.key, nil
	}
	k, err := LoadKey(m.dir)
	if err != nil {
		return nil, err
	}
	m.key = k
	return k, nil
}

// Pair pairs Pinky's key with the code and the pairing port that
// Wireless debugging's "Pair device with pairing code" shows. It turns the
// feature on: pairing is how the person asks for it.
func (m *Manager) Pair(ctx context.Context, port int, code string) error {
	m.mu.Lock()
	k, err := m.keyLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	guid, err := Pair(ctx, localAddr(port), code, k, m.name)
	m.mu.Lock()
	if err != nil {
		m.lastErr = err
		m.mu.Unlock()
		m.notify()
		return err
	}
	m.cfg.On, m.cfg.Paired, m.cfg.GUID = true, true, guid
	m.lastErr = nil
	err = m.saveLocked()
	m.mu.Unlock()
	m.notify()
	return err
}

// localAddr is a port of the phone's own adbd: it listens on every
// address, the loopback one included.
func localAddr(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

// ErrOff is a connection asked for while the feature is off.
var ErrOff = errors.New("wadb: background displays are off")

// ErrUnpaired is a connection asked for before the person paired.
var ErrUnpaired = errors.New("wadb: pair Pinky in Wireless debugging first")

// ErrNoPort is a connection asked for while Wireless debugging is off, as
// far as the app can tell.
var ErrNoPort = errors.New("wadb: turn on Wireless debugging")

// dialWait bounds one connection attempt.
const dialWait = 10 * time.Second

// Client returns an adb client over the connection, connecting first when
// there is none.
func (m *Manager) Client(ctx context.Context) (*adb.Client, error) {
	c, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	return &adb.Client{Direct: c.Dialer()}, nil
}

func (m *Manager) connect(ctx context.Context) (*Conn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case !m.cfg.On:
		return nil, ErrOff
	case !m.cfg.Paired:
		return nil, ErrUnpaired
	case m.cfg.Port == 0:
		return nil, ErrNoPort
	}
	if m.conn != nil && m.conn.Err() == nil {
		return m.conn, nil
	}
	k, err := m.keyLocked()
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, dialWait)
	defer cancel()
	c, err := m.dialHook(dctx, localAddr(m.cfg.Port), k)
	if err != nil {
		m.lastErr = err
		go m.notify()
		if errors.Is(err, ErrUnauthorized) {
			return nil, fmt.Errorf("%w (%w)", err, ErrUnpaired)
		}
		return nil, err
	}
	m.conn, m.lastErr = c, nil
	go m.watch(c)
	go m.notify()
	return c, nil
}

// watch notes when the connection ends: Wireless debugging went off, the
// Wi-Fi changed or the phone forgot the key.
func (m *Manager) watch(c *Conn) {
	<-c.Done()
	m.mu.Lock()
	if m.conn == c {
		m.conn = nil
		if err := c.Err(); !errors.Is(err, ErrClosed) {
			m.lastErr = err
		}
	}
	m.mu.Unlock()
	m.notify()
}

// Close drops the connection, keeping the settings.
func (m *Manager) Close() {
	m.mu.Lock()
	c := m.conn
	m.conn = nil
	m.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// MarkPaired records a pairing made outside the manager (wadbctl pair with
// the same key directory) and turns the feature on, for tools and tests.
func (m *Manager) MarkPaired() error {
	m.mu.Lock()
	m.cfg.On, m.cfg.Paired = true, true
	err := m.saveLocked()
	m.mu.Unlock()
	m.notify()
	return err
}

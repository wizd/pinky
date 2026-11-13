// Package wadb connects Pinky's app to the adbd of the phone it runs on,
// through Wireless debugging: it pairs with the six-digit code (pair.go),
// then opens adb's TLS transport to the phone itself, the way `adb pair`
// and `adb connect` do from a computer. With it the app gets the shell
// user's privileges, which it needs to run apps on a display of its own
// (docs/notes/android-wadb.md).
//
// It speaks adb's transport protocol directly (AOSP packages/modules/adb,
// protocol.txt): CNXN, then STLS and a TLS 1.3 handshake in which the
// app's key proves who it is, then streams opened with OPEN and carried by
// WRTE, OKAY and CLSE. Each stream is a net.Conn, so the adb package's
// shell and sync code and the scrcpy session run over it unchanged
// (adb.Client.Direct).
package wadb

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Transport commands.
const (
	cmdCNXN = 0x4e584e43
	cmdSTLS = 0x534c5453
	cmdAUTH = 0x48545541
	cmdOPEN = 0x4e45504f
	cmdOKAY = 0x59414b4f
	cmdWRTE = 0x45545257
	cmdCLSE = 0x45534c43
)

const (
	// adbVersion is the protocol version that skips checksums.
	adbVersion = 0x01000001
	// stlsVersion is STLS's version.
	stlsVersion = 0x01000000
	// maxPayload is the largest message this side takes.
	maxPayload = 1 << 20
	// hostBanner announces the features streams here may use. Not
	// delayed_ack: without it every WRTE waits for its OKAY.
	hostBanner = "host::features=shell_v2,cmd,stat_v2,ls_v2,fixed_push_mkdir,abb_exec"
)

// ErrUnauthorized is a connection adbd refused: the app's key is not
// paired (any more), so the person pairs again.
var ErrUnauthorized = errors.New("wadb: the phone does not know Pinky's key: pair again")

// ErrClosed is a stream or connection that has closed.
var ErrClosed = errors.New("wadb: connection closed")

// Conn is one adb connection to the phone's adbd.
type Conn struct {
	tc     net.Conn
	w      *bufio.Writer
	wmu    sync.Mutex
	max    int // the largest payload both sides take
	Banner string

	mu      sync.Mutex
	streams map[uint32]*stream
	nextID  uint32
	err     error
	done    chan struct{}
}

// Dial connects to the adbd at addr (Wireless debugging's IP address and
// port, or 127.0.0.1 and that port) with key, which must have been paired.
func Dial(ctx context.Context, addr string, key *Key) (*Conn, error) {
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("wadb: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			raw.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = raw.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if err := writeMsg(raw, cmdCNXN, adbVersion, maxPayload, []byte(hostBanner+"\x00")); err != nil {
		return nil, fmt.Errorf("wadb: connect: %w", err)
	}
	m, err := readMsg(raw)
	if err != nil {
		return nil, fmt.Errorf("wadb: connect: %w", err)
	}
	switch m.cmd {
	case cmdSTLS:
	case cmdAUTH:
		// adbd over Wireless debugging always asks for TLS; AUTH means a
		// plain adb port (adb tcpip), which Pinky does not use.
		return nil, errors.New("wadb: the port asks for adb's RSA authentication, not TLS: not Wireless debugging's port")
	default:
		return nil, fmt.Errorf("wadb: connect: unexpected %s", cmdName(m.cmd))
	}
	if err := writeMsg(raw, cmdSTLS, stlsVersion, 0, nil); err != nil {
		return nil, fmt.Errorf("wadb: connect: %w", err)
	}
	tc := tls.Client(raw, &tls.Config{
		MinVersion: tls.VersionTLS13,
		// adbd's certificate is self-signed and changes with its key; the
		// connection only ever goes to the phone itself.
		InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &key.cert, nil
		},
	})
	if err := tc.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("wadb: TLS: %w", err)
	}
	// With TLS 1.3 adbd checks the key after the handshake: an unknown key
	// sees the connection close instead of CNXN.
	m, err = readMsg(tc)
	if err != nil {
		if errors.Is(err, io.EOF) || isReset(err) || strings.Contains(err.Error(), "certificate") {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("wadb: connect: %w", err)
	}
	if m.cmd != cmdCNXN {
		return nil, fmt.Errorf("wadb: connect: unexpected %s", cmdName(m.cmd))
	}
	_ = raw.SetDeadline(time.Time{})
	if !stop() {
		return nil, ctx.Err()
	}
	c := &Conn{
		tc: tc, w: bufio.NewWriterSize(tc, 64<<10),
		max:     min(int(m.arg1), maxPayload),
		Banner:  strings.TrimRight(string(m.data), "\x00"),
		streams: map[uint32]*stream{},
		done:    make(chan struct{}),
	}
	if c.max <= 0 {
		c.max = 4096
	}
	ok = true
	go c.readLoop()
	return c, nil
}

// Done is closed when the connection ends; Err says why.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is why the connection ended, nil while it is up.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection and every stream on it.
func (c *Conn) Close() error {
	c.fail(ErrClosed)
	return nil
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	streams := c.streams
	c.streams = map[uint32]*stream{}
	close(c.done)
	c.mu.Unlock()
	c.tc.Close()
	for _, s := range streams {
		s.remoteClosed(err)
	}
}

// Open opens a service ("shell:…", "sync:", "localabstract:…" and so on)
// and returns its stream.
func (c *Conn) Open(ctx context.Context, service string) (net.Conn, error) {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	c.nextID++
	if c.nextID == 0 {
		c.nextID = 1
	}
	s := newStream(c, c.nextID)
	c.streams[s.local] = s
	c.mu.Unlock()
	if err := c.send(cmdOPEN, s.local, 0, append([]byte(service), 0)); err != nil {
		c.drop(s.local)
		return nil, err
	}
	select {
	case <-s.opened:
		if s.remote == 0 {
			c.drop(s.local)
			return nil, fmt.Errorf("wadb: %s: the phone refused the service", firstWord(service))
		}
		return s, nil
	case <-c.done:
		return nil, c.Err()
	case <-ctx.Done():
		// Close it should the OKAY come later.
		go func() {
			select {
			case <-s.opened:
				s.Close()
			case <-c.done:
			}
		}()
		return nil, ctx.Err()
	}
}

func firstWord(s string) string {
	if i := strings.IndexAny(s, " :"); i >= 0 {
		return s[:i+1]
	}
	return s
}

func (c *Conn) drop(id uint32) {
	c.mu.Lock()
	delete(c.streams, id)
	c.mu.Unlock()
}

func (c *Conn) stream(id uint32) *stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams[id]
}

func (c *Conn) send(cmd, arg0, arg1 uint32, data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.Err(); err != nil {
		return err
	}
	if err := writeMsg(c.w, cmd, arg0, arg1, data); err != nil {
		c.fail(err)
		return err
	}
	if err := c.w.Flush(); err != nil {
		c.fail(err)
		return err
	}
	return nil
}

func (c *Conn) readLoop() {
	r := bufio.NewReaderSize(c.tc, 64<<10)
	for {
		m, err := readMsg(r)
		if err != nil {
			c.fail(fmt.Errorf("wadb: connection lost: %w", err))
			return
		}
		switch m.cmd {
		case cmdOKAY:
			// arg0 is the phone's id, arg1 ours.
			if s := c.stream(m.arg1); s != nil {
				s.okay(m.arg0)
			}
		case cmdWRTE:
			s := c.stream(m.arg1)
			if s == nil {
				_ = c.send(cmdCLSE, 0, m.arg0, nil)
				continue
			}
			// The stream acknowledges once its reader has room: one
			// message in flight per stream keeps a slow reader from
			// filling memory.
			s.data(m.data)
		case cmdCLSE:
			if s := c.stream(m.arg1); s != nil {
				c.drop(m.arg1)
				s.remoteClosed(io.EOF)
			}
		case cmdCNXN, cmdAUTH, cmdSTLS:
			c.fail(fmt.Errorf("wadb: unexpected %s mid-connection", cmdName(m.cmd)))
			return
		}
	}
}

type message struct {
	cmd, arg0, arg1 uint32
	data            []byte
}

func writeMsg(w io.Writer, cmd, arg0, arg1 uint32, data []byte) error {
	var hdr [24]byte
	binary.LittleEndian.PutUint32(hdr[0:], cmd)
	binary.LittleEndian.PutUint32(hdr[4:], arg0)
	binary.LittleEndian.PutUint32(hdr[8:], arg1)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(data)))
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	binary.LittleEndian.PutUint32(hdr[16:], sum)
	binary.LittleEndian.PutUint32(hdr[20:], cmd^0xffffffff)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func readMsg(r io.Reader) (message, error) {
	var hdr [24]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return message{}, err
	}
	m := message{
		cmd:  binary.LittleEndian.Uint32(hdr[0:]),
		arg0: binary.LittleEndian.Uint32(hdr[4:]),
		arg1: binary.LittleEndian.Uint32(hdr[8:]),
	}
	n := binary.LittleEndian.Uint32(hdr[12:])
	if binary.LittleEndian.Uint32(hdr[20:]) != m.cmd^0xffffffff {
		return message{}, errors.New("bad message magic")
	}
	if n > maxPayload {
		return message{}, fmt.Errorf("message too large (%d bytes)", n)
	}
	if n > 0 {
		m.data = make([]byte, n)
		if _, err := io.ReadFull(r, m.data); err != nil {
			return message{}, err
		}
	}
	return m, nil
}

func cmdName(c uint32) string {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], c)
	for _, x := range b {
		if x < 'A' || x > 'Z' {
			return fmt.Sprintf("0x%08x", c)
		}
	}
	return string(b[:])
}

// Dialer returns a function for adb.Client.Direct that opens services on
// this connection, whatever the serial.
func (c *Conn) Dialer() func(ctx context.Context, serial, service string) (net.Conn, error) {
	return func(ctx context.Context, _ string, service string) (net.Conn, error) {
		return c.Open(ctx, service)
	}
}

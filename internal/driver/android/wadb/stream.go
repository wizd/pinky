package wadb

import (
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// ackThreshold is how much unread data a stream holds before it stops
// acknowledging, which makes adbd wait: it sends the next WRTE of a stream
// only after the OKAY for the last one.
const ackThreshold = 4 << 20

// stream is one service on a Conn, as a net.Conn.
type stream struct {
	c      *Conn
	local  uint32
	remote uint32 // set by the OKAY that answers OPEN; 0 when refused

	opened   chan struct{} // closed once OPEN is answered
	openOnce sync.Once

	mu         sync.Mutex
	rbuf       []byte
	readable   chan struct{} // signalled when rbuf grows or the stream ends
	pendingAck bool
	rerr       error // set once the phone closed the stream
	closed     bool  // Close was called

	canWrite chan struct{} // holds a token while a WRTE may go out
	wmu      sync.Mutex    // one Write at a time

	rdl, wdl deadline
}

func newStream(c *Conn, id uint32) *stream {
	return &stream{
		c: c, local: id,
		opened:   make(chan struct{}),
		readable: make(chan struct{}, 1),
		canWrite: make(chan struct{}, 1),
		rdl:      makeDeadline(),
		wdl:      makeDeadline(),
	}
}

// okay is an OKAY from the phone: the answer to OPEN, then to each WRTE.
func (s *stream) okay(remote uint32) {
	first := false
	s.openOnce.Do(func() {
		first = true
		s.remote = remote
		close(s.opened)
	})
	if first || remote == s.remote {
		select {
		case s.canWrite <- struct{}{}:
		default:
		}
	}
}

func (s *stream) data(b []byte) {
	s.mu.Lock()
	s.rbuf = append(s.rbuf, b...)
	ack := len(s.rbuf) < ackThreshold
	if !ack {
		s.pendingAck = true
	}
	s.mu.Unlock()
	s.signal()
	if ack {
		s.sendAck()
	}
}

func (s *stream) sendAck() {
	go func() { _ = s.c.send(cmdOKAY, s.local, s.remote, nil) }()
}

func (s *stream) signal() {
	select {
	case s.readable <- struct{}{}:
	default:
	}
}

func (s *stream) remoteClosed(err error) {
	s.openOnce.Do(func() { close(s.opened) }) // a refused OPEN: remote stays 0
	s.mu.Lock()
	if s.rerr == nil {
		s.rerr = err
	}
	s.mu.Unlock()
	s.signal()
	// Wake a writer waiting for OKAY.
	select {
	case s.canWrite <- struct{}{}:
	default:
	}
}

func (s *stream) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return 0, net.ErrClosed
		}
		if len(s.rbuf) > 0 {
			n := copy(p, s.rbuf)
			s.rbuf = s.rbuf[n:]
			if len(s.rbuf) == 0 {
				s.rbuf = nil
			}
			ack := s.pendingAck && len(s.rbuf) < ackThreshold
			if ack {
				s.pendingAck = false
			}
			s.mu.Unlock()
			if ack {
				s.sendAck()
			}
			return n, nil
		}
		if s.rerr != nil {
			err := s.rerr
			s.mu.Unlock()
			return 0, err
		}
		s.mu.Unlock()
		select {
		case <-s.readable:
		case <-s.rdl.wait():
			return 0, os.ErrDeadlineExceeded
		}
	}
}

func (s *stream) Write(p []byte) (int, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	n := 0
	for len(p) > 0 {
		select {
		case <-s.canWrite:
		case <-s.wdl.wait():
			return n, os.ErrDeadlineExceeded
		}
		s.mu.Lock()
		closed, rerr := s.closed, s.rerr
		s.mu.Unlock()
		if closed {
			return n, net.ErrClosed
		}
		if rerr != nil {
			// Leave the token for the next writer to see the same.
			s.canWrite <- struct{}{}
			return n, io.ErrClosedPipe
		}
		chunk := p
		if len(chunk) > s.c.max {
			chunk = chunk[:s.c.max]
		}
		if err := s.c.send(cmdWRTE, s.local, s.remote, chunk); err != nil {
			return n, err
		}
		n += len(chunk)
		p = p[len(chunk):]
	}
	return n, nil
}

func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	gone := s.rerr != nil
	s.mu.Unlock()
	s.signal()
	select {
	case s.canWrite <- struct{}{}:
	default:
	}
	s.c.drop(s.local)
	if !gone && s.remote != 0 {
		_ = s.c.send(cmdCLSE, s.local, s.remote, nil)
	}
	return nil
}

func (s *stream) LocalAddr() net.Addr  { return addr("wadb") }
func (s *stream) RemoteAddr() net.Addr { return addr("adbd") }

func (s *stream) SetDeadline(t time.Time) error {
	s.rdl.set(t)
	s.wdl.set(t)
	return nil
}

func (s *stream) SetReadDeadline(t time.Time) error  { s.rdl.set(t); return nil }
func (s *stream) SetWriteDeadline(t time.Time) error { s.wdl.set(t); return nil }

type addr string

func (a addr) Network() string { return "adb" }
func (a addr) String() string  { return string(a) }

// deadline is a settable deadline whose wait channel closes when it
// passes, as net.Pipe's does.
type deadline struct {
	mu     *sync.Mutex
	timer  **time.Timer
	cancel *chan struct{}
}

func makeDeadline() deadline {
	ch := make(chan struct{})
	var t *time.Timer
	return deadline{mu: new(sync.Mutex), timer: &t, cancel: &ch}
}

func (d deadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if *d.timer != nil && !(*d.timer).Stop() {
		<-*d.cancel // the timer fired: wait for its close
	}
	*d.timer = nil
	closed := isClosed(*d.cancel)
	if t.IsZero() {
		if closed {
			*d.cancel = make(chan struct{})
		}
		return
	}
	if dur := time.Until(t); dur > 0 {
		if closed {
			*d.cancel = make(chan struct{})
		}
		ch := *d.cancel
		*d.timer = time.AfterFunc(dur, func() { close(ch) })
		return
	}
	if !closed {
		close(*d.cancel)
	}
}

func (d deadline) wait() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return *d.cancel
}

func isClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

var _ net.Conn = (*stream)(nil)

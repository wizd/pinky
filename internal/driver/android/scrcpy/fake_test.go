package scrcpy

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"path"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// fakePhone plays an adb server with one device that runs scripted scrcpy
// servers: each server's shell carries its log; with video, its first
// abstract-socket connection is the video socket and the second the
// control socket, and without, the only one is the control socket. The
// phone keeps the sizes of the files pushed to it and runs the few shell
// commands Pinky uses on them; a server deletes the jar it runs from, as
// scrcpy's does, and fails when that is missing.
type fakePhone struct {
	t  *testing.T
	ln net.Listener

	mu        sync.Mutex
	shell     net.Conn // the last server's
	command   string   // the last server's
	servers   map[string]*fakeServer
	failFirst int      // abstract-socket connections to refuse before accepting
	pushed    int      // bytes pushed
	pushes    []string // paths of the completed pushes
	files     map[string]int64
	pushDelay time.Duration // per DATA chunk
	noLinks   bool          // ln and cp fail
	noJar     []string      // jars servers found missing
	control   []byte
	clipboard string
}

type fakeServer struct {
	shell   net.Conn
	noVideo bool
	sockets int
}

func newFakePhone(t *testing.T) (*fakePhone, *adb.Client) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePhone{t: t, ln: ln, clipboard: "copied", servers: map[string]*fakeServer{}, files: map[string]int64{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f, f.client()
}

func (f *fakePhone) client() *adb.Client { return &adb.Client{Addr: f.ln.Addr().String()} }

func readReq(r io.Reader) (string, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return "", err
	}
	var size int
	fmt.Sscanf(string(n[:]), "%04x", &size)
	b := make([]byte, size)
	_, err := io.ReadFull(r, b)
	return string(b), err
}

var (
	fakeMvRE     = regexp.MustCompile(`^mv (\S+) (\S+) && echo ok$`)
	fakeLinkRE   = regexp.MustCompile(`^ln -f (\S+) (\S+) 2>/dev/null \|\| cp (\S+) (\S+) && echo ok$`)
	fakeServerRE = regexp.MustCompile(`^CLASSPATH=(\S+) app_process / com\.genymobile\.scrcpy\.Server (?:.*scid=([0-9a-f]{8}))?`)
)

func (f *fakePhone) handle(c net.Conn) {
	r := bufio.NewReader(c)
	req, err := readReq(r)
	if err != nil || req != "host:transport:phone" {
		io.WriteString(c, "FAIL0010device not found")
		c.Close()
		return
	}
	io.WriteString(c, "OKAY")
	svc, err := readReq(r)
	if err != nil {
		c.Close()
		return
	}
	cmd, isShell := strings.CutPrefix(svc, "shell:")
	switch {
	case svc == "sync:":
		io.WriteString(c, "OKAY")
		f.sync(c, r)
		c.Close()
	case isShell && strings.HasPrefix(cmd, "rm -f "):
		f.mu.Lock()
		delete(f.files, strings.TrimPrefix(cmd, "rm -f "))
		f.mu.Unlock()
		io.WriteString(c, "OKAY")
		c.Close()
	case isShell && fakeMvRE.MatchString(cmd):
		m := fakeMvRE.FindStringSubmatch(cmd)
		io.WriteString(c, "OKAY")
		f.mu.Lock()
		if size, ok := f.files[m[1]]; ok {
			f.files[m[2]] = size
			delete(f.files, m[1])
			io.WriteString(c, "ok\n")
		} else {
			io.WriteString(c, "mv: "+m[1]+": No such file or directory\n")
		}
		f.mu.Unlock()
		c.Close()
	case isShell && fakeLinkRE.MatchString(cmd):
		m := fakeLinkRE.FindStringSubmatch(cmd)
		io.WriteString(c, "OKAY")
		f.mu.Lock()
		if size, ok := f.files[m[1]]; ok && !f.noLinks {
			f.files[m[2]] = size
			io.WriteString(c, "ok\n")
		} else {
			io.WriteString(c, "cp: "+m[3]+": Operation not permitted\n")
		}
		f.mu.Unlock()
		c.Close()
	case isShell && cmd == staleJarsCommand():
		f.mu.Lock()
		for name := range f.files {
			base := path.Base(name)
			if path.Dir(name) == jarDir && strings.HasPrefix(base, jarPrefix) && !strings.HasPrefix(base, jarPrefix+jarTag) {
				delete(f.files, name)
			}
		}
		f.mu.Unlock()
		io.WriteString(c, "OKAY")
		c.Close()
	case isShell:
		m := fakeServerRE.FindStringSubmatch(cmd)
		if m == nil {
			f.t.Errorf("unexpected shell command %q", cmd)
			io.WriteString(c, "FAIL0006closed")
			c.Close()
			return
		}
		// Keep the shell before answering: the client goes on as soon as it
		// reads OKAY. Answering under the lock keeps others from writing the
		// server's log to the shell before the OKAY.
		f.mu.Lock()
		_, found := f.files[m[1]]
		if found {
			delete(f.files, m[1]) // the server unlinks the jar it runs from
			f.servers[m[2]] = &fakeServer{shell: c, noVideo: strings.Contains(cmd, "video=false")}
			f.shell, f.command = c, cmd
		} else {
			f.noJar = append(f.noJar, m[1])
		}
		io.WriteString(c, "OKAY")
		f.mu.Unlock()
		if !found {
			io.WriteString(c, "Error: Could not find or load main class com.genymobile.scrcpy.Server\n")
			c.Close()
			return
		}
		io.WriteString(c, "[server] INFO: Device: [Fake] Phone (Android 17)\n")
		if strings.Contains(svc, "list_apps=true") {
			io.WriteString(c, "[server] INFO: List of apps:\n * Calculator                     com.android.calculator2\n")
			c.Close()
		}
	case strings.HasPrefix(svc, "localabstract:scrcpy_"):
		f.mu.Lock()
		srv := f.servers[strings.TrimPrefix(svc, "localabstract:scrcpy_")]
		if f.failFirst > 0 || srv == nil {
			if f.failFirst > 0 {
				f.failFirst--
			}
			f.mu.Unlock()
			io.WriteString(c, "FAIL0006closed")
			c.Close()
			return
		}
		srv.sockets++
		n := srv.sockets
		f.mu.Unlock()
		io.WriteString(c, "OKAY")
		switch {
		case srv.noVideo:
			c.Write([]byte{0}) // dummy byte
			c.Write(fakeDeviceName())
			f.controlLoop(c, r, srv)
		case n == 1:
			f.video(c)
		default:
			f.controlLoop(c, r, srv)
		}
	default:
		io.WriteString(c, "FAIL0006closed")
		c.Close()
	}
}

func (f *fakePhone) sync(c net.Conn, r *bufio.Reader) {
	var sending string
	var size int64
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		id, n := string(hdr[:4]), binary.LittleEndian.Uint32(hdr[4:])
		switch id {
		case "SEND":
			spec := make([]byte, n)
			io.ReadFull(r, spec)
			sending, _, _ = strings.Cut(string(spec), ",")
			size = 0
		case "DATA":
			io.CopyN(io.Discard, r, int64(n))
			size += int64(n)
			f.mu.Lock()
			f.pushed += int(n)
			delay := f.pushDelay
			f.mu.Unlock()
			time.Sleep(delay)
		case "DONE":
			f.mu.Lock()
			f.files[sending] = size
			f.pushes = append(f.pushes, sending)
			f.mu.Unlock()
			c.Write([]byte("OKAY\x00\x00\x00\x00"))
		case "STAT":
			name := make([]byte, n)
			io.ReadFull(r, name)
			reply := make([]byte, 16)
			copy(reply, "STAT")
			f.mu.Lock()
			if size, ok := f.files[string(name)]; ok {
				binary.LittleEndian.PutUint32(reply[4:], 0o100644)
				binary.LittleEndian.PutUint32(reply[8:], uint32(size))
				binary.LittleEndian.PutUint32(reply[12:], uint32(time.Now().Unix()))
			}
			f.mu.Unlock()
			c.Write(reply)
		case "QUIT":
			return
		}
	}
}

func fakeDeviceName() []byte {
	name := make([]byte, 64)
	copy(name, "Fake Phone")
	return name
}

func (f *fakePhone) video(c net.Conn) {
	c.Write([]byte{0}) // dummy byte
	c.Write(fakeDeviceName())
	c.Write([]byte{0x68, 0x32, 0x36, 0x34})
	sess := make([]byte, 12)
	binary.BigEndian.PutUint32(sess[0:], 0x80000000)
	binary.BigEndian.PutUint32(sess[4:], 1080)
	binary.BigEndian.PutUint32(sess[8:], 2340)
	c.Write(sess)
	c.Write(header(flagConfig, 2))
	c.Write([]byte{1, 2})
	c.Write(header(flagKeyFrame|1000, 3))
	c.Write([]byte{3, 4, 5})
}

func (f *fakePhone) controlLoop(c net.Conn, r *bufio.Reader, srv *fakeServer) {
	for {
		t, err := r.ReadByte()
		if err != nil {
			return
		}
		switch t {
		case TypeInjectKeycode:
			buf := make([]byte, 13)
			io.ReadFull(r, buf)
			f.record(append([]byte{t}, buf...))
		case TypeGetClipboard:
			r.ReadByte()
			f.mu.Lock()
			text := f.clipboard
			f.mu.Unlock()
			msg := []byte{DeviceClipboard, 0, 0, 0, 0}
			binary.BigEndian.PutUint32(msg[1:], uint32(len(text)))
			c.Write(append(msg, text...))
		case TypeSetClipboard:
			var hdr [13]byte
			io.ReadFull(r, hdr[:])
			seq := binary.BigEndian.Uint64(hdr[:8])
			text := make([]byte, binary.BigEndian.Uint32(hdr[9:]))
			io.ReadFull(r, text)
			f.mu.Lock()
			f.clipboard = string(text)
			f.mu.Unlock()
			ack := make([]byte, 9)
			ack[0] = DeviceAckClipboard
			binary.BigEndian.PutUint64(ack[1:], seq)
			c.Write(ack)
		case TypeStartApp:
			n, _ := r.ReadByte()
			name := make([]byte, n)
			io.ReadFull(r, name)
			if string(name) == "com.android.calculator2" {
				io.WriteString(srv.shell, "[server] INFO: Starting app \"Calculator\" [com.android.calculator2] on display 0...\n")
			} else {
				io.WriteString(srv.shell, "[server] WARN: No app found for package \""+string(name)+"\"\n")
			}
		case TypeSetDisplayPower:
			r.ReadByte()
		case TypeResetVideo:
		default:
			f.t.Errorf("unexpected control message %d", t)
			return
		}
	}
}

func (f *fakePhone) record(msg []byte) {
	f.mu.Lock()
	f.control = append(f.control, msg...)
	f.mu.Unlock()
}

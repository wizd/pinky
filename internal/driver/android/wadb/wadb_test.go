package wadb

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Keys take a moment to make: the tests share two.
var (
	testKeys     [2]*Key
	testKeysOnce sync.Once
)

func keys(t *testing.T) (client, server *Key) {
	t.Helper()
	testKeysOnce.Do(func() {
		for i := range testKeys {
			priv, err := rsa.GenerateKey(rand.Reader, keyBits)
			if err != nil {
				panic(err)
			}
			if testKeys[i], err = NewKey(priv); err != nil {
				panic(err)
			}
		}
	})
	return testKeys[0], testKeys[1]
}

func TestLoadKeyKeepsTheKey(t *testing.T) {
	dir := t.TempDir()
	k1, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permissions: Go reports a writable file as 0666.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v, want 0600", fi.Mode().Perm())
	}
	k2, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if k1.PublicKey("x") != k2.PublicKey("x") {
		t.Fatal("loading again gave another key")
	}
	if err := RemoveKey(dir); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey(dir); err != nil {
		t.Fatal("removing a missing key:", err)
	}
	k3, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if k3.PublicKey("x") == k1.PublicKey("x") {
		t.Fatal("a removed key came back")
	}
}

func TestAdbPublicKeyFormat(t *testing.T) {
	k, _ := keys(t)
	s := k.PublicKey("pinky@phone")
	b64, name, ok := strings.Cut(s, " ")
	if !ok || name != "pinky@phone" {
		t.Fatalf("public key %q has no name", s)
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 524 {
		t.Fatalf("encoded key is %d bytes, adb's is 524", len(b))
	}
	if w := binary.LittleEndian.Uint32(b); w != 64 {
		t.Fatalf("modulus words %d, want 64", w)
	}
	le := func(p []byte) *big.Int {
		r := make([]byte, len(p))
		for i := range p {
			r[len(p)-1-i] = p[i]
		}
		return new(big.Int).SetBytes(r)
	}
	n := le(b[8:264])
	if n.Cmp(k.priv.N) != 0 {
		t.Fatal("modulus differs")
	}
	n0inv := binary.LittleEndian.Uint32(b[4:])
	if uint32(n.Uint64())*n0inv != 0xffffffff {
		t.Fatal("n0inv is not -1/n mod 2^32")
	}
	rr := new(big.Int).Lsh(big.NewInt(1), 4096)
	rr.Mod(rr, n)
	if le(b[264:520]).Cmp(rr) != 0 {
		t.Fatal("rr is not R² mod n")
	}
	if e := binary.LittleEndian.Uint32(b[520:]); int(e) != k.priv.E {
		t.Fatalf("exponent %d, want %d", e, k.priv.E)
	}
}

func TestNewKeyRefusesOtherSizes(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewKey(priv); err == nil {
		t.Fatal("a 1024-bit key was taken")
	}
}

// serverTLS is adbd's side: its own certificate, and the client's asked
// for, as adbd does.
func serverTLS(srv *Key) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{srv.cert},
		ClientAuth:   tls.RequireAnyClientCert,
	}
}

// fakePairServer is adbd's pairing side (pairing_connection.cpp as the
// server): it takes one pairing with code and reports the public key it
// got.
func fakePairServer(t *testing.T, code string, srv *Key) (addr string, got <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan string, 1)
	go func() {
		defer close(ch)
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		tc := tls.Server(raw, serverTLS(srv))
		if err := tc.Handshake(); err != nil {
			return
		}
		cs := tc.ConnectionState()
		ekm, _ := cs.ExportKeyingMaterial(exportLabel, nil, exportedKeySize)
		sp, _ := newSpake2(spakeBob, pairServerName, pairClientName, append([]byte(code), ekm...), nil)
		typ, msg, err := readPairPacket(tc)
		if err != nil || typ != pairTypeSpake2 {
			return
		}
		if err := writePairPacket(tc, pairTypeSpake2, sp.msg[:]); err != nil {
			return
		}
		shared, err := sp.finish(msg)
		if err != nil {
			return
		}
		aead, _ := pairCipher(shared[:])
		typ, enc, err := readPairPacket(tc)
		if err != nil || typ != pairTypePeerInfo {
			return
		}
		info, err := aead.open(enc)
		if err != nil {
			return // a wrong code: adbd hangs up
		}
		if info[0] != peerInfoRSAKey {
			return
		}
		ch <- strings.TrimRight(string(info[1:]), "\x00")
		var mine [peerInfoSize]byte
		mine[0] = peerInfoGUID
		copy(mine[1:], "adb-FAKE-guid")
		_ = writePairPacket(tc, pairTypePeerInfo, aead.seal(mine[:]))
		_, _ = io.Copy(io.Discard, tc)
	}()
	return ln.Addr().String(), ch
}

func TestPair(t *testing.T) {
	cli, srv := keys(t)
	addr, got := fakePairServer(t, "246810", srv)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	guid, err := Pair(ctx, addr, " 246810\n", cli, "pinky@test")
	if err != nil {
		t.Fatal(err)
	}
	if guid != "adb-FAKE-guid" {
		t.Fatalf("guid %q", guid)
	}
	if k := <-got; k != cli.PublicKey("pinky@test") {
		t.Fatalf("server got key %q", k)
	}
}

func TestPairWrongCode(t *testing.T) {
	cli, srv := keys(t)
	addr, _ := fakePairServer(t, "246810", srv)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Pair(ctx, addr, "111111", cli, "pinky@test")
	if !errors.Is(err, ErrWrongCode) {
		t.Fatalf("err %v, want ErrWrongCode", err)
	}
}

// fakeAdbd is adbd's transport side for one connection: CNXN, STLS, TLS
// with the client's certificate checked against trusted, CNXN, then
// "echo:" streams that send back what they get, and every other service
// refused. Its largest payload is max.
func fakeAdbd(t *testing.T, srv *Key, trusted *Key, max uint32) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		if m, err := readMsg(raw); err != nil || m.cmd != cmdCNXN {
			return
		}
		_ = writeMsg(raw, cmdSTLS, stlsVersion, 0, nil)
		if m, err := readMsg(raw); err != nil || m.cmd != cmdSTLS {
			return
		}
		tc := tls.Server(raw, serverTLS(srv))
		if err := tc.Handshake(); err != nil {
			return
		}
		certs := tc.ConnectionState().PeerCertificates
		if len(certs) == 0 {
			return
		}
		pub, ok := certs[0].PublicKey.(*rsa.PublicKey)
		if !ok || !pub.Equal(&trusted.priv.PublicKey) {
			return // an unknown key: adbd closes the connection
		}
		var wmu sync.Mutex
		send := func(cmd, a0, a1 uint32, data []byte) {
			wmu.Lock()
			defer wmu.Unlock()
			_ = writeMsg(tc, cmd, a0, a1, data)
		}
		send(cmdCNXN, adbVersion, max, []byte("device::features=shell_v2\x00"))
		next := uint32(100)
		open := map[uint32]uint32{} // our id -> client id
		for {
			m, err := readMsg(tc)
			if err != nil {
				return
			}
			switch m.cmd {
			case cmdOPEN:
				if string(m.data) != "echo:\x00" {
					send(cmdCLSE, 0, m.arg0, nil)
					continue
				}
				next++
				open[next] = m.arg0
				send(cmdOKAY, next, m.arg0, nil)
			case cmdWRTE:
				if uint32(len(m.data)) > max {
					send(cmdCLSE, m.arg1, m.arg0, nil)
					continue
				}
				send(cmdOKAY, m.arg1, m.arg0, nil)
				send(cmdWRTE, m.arg1, m.arg0, m.data)
			case cmdCLSE:
				if _, ok := open[m.arg1]; ok {
					delete(open, m.arg1)
					send(cmdCLSE, m.arg1, m.arg0, nil)
				}
			}
		}
	}()
	return ln.Addr().String()
}

func TestDialAndStreams(t *testing.T) {
	cli, srv := keys(t)
	addr := fakeAdbd(t, srv, cli, 4096)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, addr, cli)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Banner != "device::features=shell_v2" {
		t.Fatalf("banner %q", c.Banner)
	}
	if c.max != 4096 {
		t.Fatalf("max %d, want the phone's 4096", c.max)
	}

	if _, err := c.Open(ctx, "shell:id"); err == nil {
		t.Fatal("a refused service opened")
	}

	// Two streams at once, each with more than one message's worth.
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := c.Open(ctx, "echo:")
			if err != nil {
				t.Error(err)
				return
			}
			defer s.Close()
			want := bytes.Repeat([]byte{byte('a' + i)}, 10000)
			go func() { _, _ = s.Write(want) }()
			got := make([]byte, len(want))
			_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(s, got); err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(got, want) {
				t.Error("echo differs")
			}
		}()
	}
	wg.Wait()
}

func TestStreamEOFAndDeadline(t *testing.T) {
	cli, srv := keys(t)
	addr := fakeAdbd(t, srv, cli, 4096)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, addr, cli)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := c.Open(ctx, "echo:")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := s.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read past the deadline: %v", err)
	}
	_ = s.SetReadDeadline(time.Time{})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(make([]byte, 1)); err == nil {
		t.Fatal("read after Close")
	}

	// Closing the connection ends its streams.
	s2, err := c.Open(ctx, "echo:")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	<-c.Done()
	if _, err := s2.Read(make([]byte, 1)); err == nil {
		t.Fatal("a stream outlived its connection")
	}
	if _, err := c.Open(ctx, "echo:"); err == nil {
		t.Fatal("opened on a closed connection")
	}
}

func TestDialUnknownKey(t *testing.T) {
	cli, srv := keys(t)
	// adbd trusts only its own key here, not the client's.
	addr := fakeAdbd(t, srv, srv, 4096)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Dial(ctx, addr, cli)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err %v, want ErrUnauthorized", err)
	}
}

// The client's certificate is what adbd checks: it must carry the key.
func TestCertificateCarriesKey(t *testing.T) {
	cli, _ := keys(t)
	cert, err := x509.ParseCertificate(cli.cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if !cert.PublicKey.(*rsa.PublicKey).Equal(&cli.priv.PublicKey) {
		t.Fatal("certificate holds another key")
	}
}

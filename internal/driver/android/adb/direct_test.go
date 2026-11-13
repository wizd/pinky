package adb

import (
	"context"
	"errors"
	"net"
	"testing"
)

// Direct opens services itself, with no adb server, after the guard.
func TestDirect(t *testing.T) {
	var got []string
	c := &Client{
		Addr: "127.0.0.1:1", // no server here: Direct must be used
		Direct: func(_ context.Context, serial, service string) (net.Conn, error) {
			got = append(got, serial+" "+service)
			a, b := net.Pipe()
			go func() {
				_, _ = b.Write([]byte("uid=2000(shell)\n"))
				b.Close()
			}()
			return a, nil
		},
	}
	out, err := c.Exec(context.Background(), "self", "id")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "uid=2000(shell)\n" {
		t.Fatalf("output %q", out)
	}
	if len(got) != 1 || got[0] != "self exec:id" {
		t.Fatalf("Direct got %q", got)
	}

	refused := errors.New("not this phone")
	c.Guard = func(context.Context, string) error { return refused }
	if _, err := c.Exec(context.Background(), "self", "id"); !errors.Is(err, refused) {
		t.Fatalf("err %v, want the guard's", err)
	}
	if len(got) != 1 {
		t.Fatal("Direct ran past the guard")
	}
}

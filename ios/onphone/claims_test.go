package onphone

import (
	"context"
	"sync"
	"testing"
	"time"
)

// clock is a time the test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestComputerClaims(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	c := newComputerClaims()
	c.now = clk.now
	ctx := context.Background()

	if _, _, held := c.Computer(); held {
		t.Fatal("held before the app said a word")
	}
	if busy, err := c.Claim(ctx, "task"); busy || err != nil {
		t.Fatalf("Claim = %v, %v; want free", busy, err)
	}
	c.Release(ctx)

	c.heard()
	if _, stoppable, held := c.Computer(); !held || stoppable {
		t.Fatalf("Computer = held %v stoppable %v; want held, not stoppable", held, stoppable)
	}
	if busy, _ := c.Claim(ctx, "task"); !busy {
		t.Fatal("Claim succeeded while the computer holds the phone")
	}
	clk.add(computerHold - time.Second)
	if !c.held() {
		t.Fatal("let go before computerHold")
	}
	clk.add(2 * time.Second)
	if c.held() {
		t.Fatal("still held after computerHold without a word")
	}
	if busy, _ := c.Claim(ctx, "task"); busy {
		t.Fatal("Claim refused after the computer let go")
	}
}

// The computer taking the phone stops the core's task, and tells the core.
func TestComputerClaimsTakesTheTask(t *testing.T) {
	c := newComputerClaims()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed, lost, notified := make(chan struct{}, 4), make(chan struct{}, 4), make(chan struct{}, 4)
	c.setNotify(func() { notified <- struct{}{} })
	go c.Watch(ctx, func() { changed <- struct{}{} }, func() { lost <- struct{}{} })

	if busy, _ := c.Claim(ctx, "task"); busy {
		t.Fatal("Claim refused")
	}
	c.heard()
	for _, ch := range []chan struct{}{changed, notified, lost} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("the computer's claim went unnoticed")
		}
	}
	// A further word changes nothing and stops nothing.
	c.heard()
	select {
	case <-lost:
		t.Fatal("lost twice")
	case <-time.After(1500 * time.Millisecond):
	}
}

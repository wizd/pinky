package vcorplink

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"time"
)

// Feature is what a gateway that keeps the link itself says in its hello
// (api.HelloReply.Features), so `pinky vcorp run` does not open a second
// connection for the same pairing: the platform keeps only the newest, and
// two would push each other off.
const Feature = "vcorp"

var superviseEvery = 5 * time.Second // a var for tests

// Supervise keeps a link to the platform while this computer is paired:
// it reads the pairing at file every few seconds, connects when there is
// one, connects again when it changes (paired anew with `pinky vcorp
// pair`), and disconnects when it goes away (`pinky vcorp unpair`). When
// the platform removes this Pinky the pairing is forgotten. mk makes the
// link for a pairing. It returns when ctx ends.
func Supervise(ctx context.Context, file string, mk func(*Config) *Link, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	type running struct {
		cfg    Config
		cancel context.CancelFunc
		done   chan error
	}
	var cur *running
	stop := func() {
		if cur == nil {
			return
		}
		cur.cancel()
		<-cur.done
		cur = nil
	}
	defer stop()
	t := time.NewTicker(superviseEvery)
	defer t.Stop()
	for {
		c, err := Load(file)
		switch {
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			log.Warn("vcorp: pairing unreadable", "file", file, "err", err)
		case err != nil:
			if cur != nil {
				log.Info("vcorp: unpaired; disconnecting")
				stop()
			}
		case cur == nil || cur.cfg != *c:
			stop()
			lctx, cancel := context.WithCancel(ctx)
			r := &running{cfg: *c, cancel: cancel, done: make(chan error, 1)}
			l := mk(c)
			go func() { r.done <- l.Run(lctx) }()
			cur = r
			log.Info("vcorp: linking to the platform", "origin", c.Origin, "account", c.Account)
		}
		if cur != nil {
			select {
			case err := <-cur.done:
				// The link only ends by itself when the platform removed the
				// pairing. Forget it, unless it was paired anew meanwhile.
				if errors.Is(err, ErrRevoked) {
					if now, lerr := Load(file); lerr == nil && now.DeviceID == cur.cfg.DeviceID {
						Remove(file)
					}
					log.Warn("vcorp: the platform removed this Pinky; pair again to reconnect")
				}
				cur.cancel()
				cur = nil
				// Do not connect again with the same pairing until it changes.
				if lerr := waitChange(ctx, file, c, t); lerr != nil {
					return
				}
				continue
			default:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// waitChange waits for the pairing at file to differ from c.
func waitChange(ctx context.Context, file string, c *Config, t *time.Ticker) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		now, err := Load(file)
		if err != nil || c == nil || *now != *c {
			return nil
		}
	}
}

package adb

import (
	"context"
	"fmt"
)

// Reverse has connections to remote on the phone reach local on the
// computer ("tcp:9277"), as `adb reverse` does.
func (c *Client) Reverse(ctx context.Context, serial, remote, local string) error {
	return c.reverse(ctx, serial, "reverse:forward:"+remote+";"+local)
}

// RemoveReverse removes a reverse port Reverse made.
func (c *Client) RemoveReverse(ctx context.Context, serial, remote string) error {
	return c.reverse(ctx, serial, "reverse:killforward:"+remote)
}

func (c *Client) reverse(ctx context.Context, serial, service string) error {
	conn, err := c.Dial(ctx, serial, service)
	if err != nil {
		return err
	}
	defer conn.Close()
	// The phone's adb answers a second time with the result.
	if err := readStatus(conn); err != nil {
		return ctxErr(ctx, fmt.Errorf("adb: %s: %w", firstWord(service), err))
	}
	return nil
}

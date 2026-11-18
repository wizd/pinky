package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/mcp"
)

// runQuitShim is `pinky mcp` started after the person quit Pinky, which it
// must not start again (quitmark.go). It behaves as a shim whose Pinky quit
// while it ran: the agent's session starts and stays, every tool call gets
// at once the answer that Pinky is not running, and once the person opens
// Pinky the session goes on with it.
//
// A shim that ran when Pinky quit knows the agent's session from Pinky;
// this one has never met a Pinky. So it starts the session with an MCP
// server of its own in this process: the same server, version and tools as
// Pinky's, without a gateway behind it, whose tool calls all get that
// answer. The agent lists Pinky's real tools and reads Pinky's
// instructions. Once a gateway answers, the shim closes that server, and
// the shim session moves the agent's session to Pinky as after any quit
// (shimSession.run, awaitReopen): it replays the agent's initialize, and
// tells the agent when the tools differ.
func runQuitShim(dirs api.Dirs, stdin io.Reader, stdout io.Writer) error {
	tok, err := api.EnsureToken(dirs.TokenFile())
	if err != nil {
		return err
	}
	return runWithoutPinky(newShimSession(dirs, tok, stdout), stdin)
}

// runWithoutPinky runs the shim session on a server of its own until a
// gateway answers, then on the gateway.
func runWithoutPinky(s *shimSession, stdin io.Reader) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := quitServer(ctx)
	if err != nil {
		return err
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.poll):
			}
			if c, err := api.Dial(s.dirs); err == nil {
				c.Close()
				conn.CloseWrite() // the session ends here; run reconnects, to Pinky
				return
			}
		}
	}()
	return s.run(conn, bufio.NewReader(conn), stdin)
}

// quitServer starts Pinky's MCP server without a gateway, in this process,
// and returns the connection to it. Every tool call gets the answer that
// Pinky is not running (notRunning); nothing reaches the tools.
func quitServer(ctx context.Context) (quitConn, error) {
	m := mcp.New(nil, version)
	m.Use(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method == "tools/call" {
				return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: notRunning}}}, nil
			}
			return next(ctx, method, req)
		}
	})
	shim, server := net.Pipe()
	if _, err := m.SDK().Connect(ctx, &sdk.IOTransport{Reader: server, Writer: server}, nil); err != nil {
		shim.Close()
		server.Close()
		return quitConn{}, err
	}
	return quitConn{shim, server}, nil
}

// quitConn is the shim's end of the connection to its own server. It ends
// as a gateway's does: the shim reads to the end of what the server sent.
type quitConn struct {
	net.Conn
	server net.Conn
}

// CloseWrite ends the server's session, as a gateway ends it once the agent
// is done (halfClose).
func (c quitConn) CloseWrite() error { return c.server.Close() }

func (c quitConn) Close() error {
	c.server.Close()
	return c.Conn.Close()
}

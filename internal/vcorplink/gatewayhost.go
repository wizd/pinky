package vcorplink

import (
	"context"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// GatewayHost is a Pinky in this process: the link talks to the gateway
// and its MCP server directly, over in-memory transports.
type GatewayHost struct {
	G   *gateway.Gateway
	MCP *mcp.Server
}

// Phones lists the gateway's phones.
func (h *GatewayHost) Phones(context.Context) ([]Phone, error) {
	devs := h.G.Devices()
	out := make([]Phone, 0, len(devs))
	for _, d := range devs {
		out = append(out, Phone{ID: d.Info.ID, Name: d.Info.Name, Platform: string(d.Info.Platform), Model: d.Info.Model,
			Online: d.Info.State == driver.StateOnline && !d.Paused})
	}
	return out, nil
}

// Connect starts an MCP session on the server and returns the client's end.
// The session lasts until the client closes it.
func (h *GatewayHost) Connect(context.Context) (sdk.Transport, error) {
	st, ct := sdk.NewInMemoryTransports()
	go h.MCP.Serve(context.Background(), st)
	return ct, nil
}

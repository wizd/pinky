package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// An app that draws its own search box shows only its placeholder (Xianyu,
// GitHub issue #76): typing by its ref taps it and types, read-only mode
// included, while other text still is no field.
func TestTypeIntoSearchPlaceholder(t *testing.T) {
	g, dev := newTestGateway(t)
	ctx := context.Background()
	s, _ := connect(t, g, "Search")
	dev.Show("settings")
	dev.SetOverlay([]screen.Element{
		{Role: screen.RoleText, Label: "搜索宝贝/帖子/用户", Source: screen.SourceTree, Bounds: screen.Rect{X: 120, Y: 160, W: 700, H: 80}},
	})
	if err := g.SetReadOnly("pixel", true); err != nil {
		t.Fatal(err)
	}
	obs := s.Observe(ctx, ObserveRequest{})
	before := len(dev.Inputs())
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "type", Ref: ref(t, obs, "搜索宝贝/帖子/用户"), Text: "奥特曼"}), StatusSettled)
	ins := dev.Inputs()[before:]
	if len(ins) != 2 || ins[0].Kind != "tap" || ins[1].Kind != "type" || ins[1].Text != "奥特曼" {
		t.Fatalf("inputs %+v", ins)
	}
	res := s.Act(ctx, Call{}, Action{Tool: "type", Ref: ref(t, s.Observe(ctx, ObserveRequest{}), "Wi-Fi"), Text: "x"})
	if res.Error != ErrInvalidArgument {
		t.Fatalf("typing into a switch: %+v", res)
	}
}

// An agent the person gave read-only access types into a search box that
// only its place names, as Pinduoduo's (GitHub issue #156), and nowhere
// else; the refusal and the task card say the read-only is the agent's
// access, which Settings › Agents changes.
func TestAgentReadOnlySearchBox(t *testing.T) {
	g, dev := newTestGateway(t)
	ctx := context.Background()
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeReadOnly})
	s, _ := connect(t, g, "Find a thermos")
	if ts := g.Tasks(); len(ts) != 1 || !ts[0].AgentReadOnly || ts[0].ReadOnly {
		t.Fatalf("tasks %+v", ts)
	}
	cart := toCart(t, s)
	res := s.Act(ctx, Call{}, Action{Tool: "type", Ref: ref(t, cart, "Note for the restaurant"), Text: "hi"})
	mustStatus(t, res, StatusBlocked)
	if res.Rule != policy.RuleReadOnly || !strings.Contains(res.Message, "read-only access") ||
		!strings.Contains(res.Message, "Settings › Agents") || strings.Contains(res.Message, "Settings › Safety") {
		t.Fatalf("refusal %+v", res)
	}

	dev.Show("settings")
	dev.SetOverlay([]screen.Element{
		{Role: screen.RoleInput, Label: "羽绒服女冬季", ID: "com.xunmeng.pinduoduo:id/pdd", Source: screen.SourceTree,
			Bounds: screen.Rect{X: 216, Y: 120, W: 564, H: 84}, States: []string{screen.StateEditable}},
		{Role: screen.RoleButton, Label: "搜索", Source: screen.SourceTree, Bounds: screen.Rect{X: 900, Y: 120, W: 168, H: 84}},
	})
	obs := s.Observe(ctx, ObserveRequest{})
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "type", Ref: ref(t, obs, "羽绒服女冬季"), Text: "保温杯"}), StatusSettled)

	g.SetAgentScope(agent.Name, Scope{Mode: ScopeEverything})
	if ts := g.Tasks(); len(ts) != 1 || ts[0].AgentReadOnly {
		t.Fatalf("tasks after the change %+v", ts)
	}
}

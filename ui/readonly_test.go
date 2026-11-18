package ui

import (
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/gateway"
)

// A read-only task's card says whose read-only it is and where it changes:
// the agent's access in Settings › Agents, the agent's own request, or
// Read-only mode in Settings › Safety (GitHub issue #156).
func TestReadOnlyNotes(t *testing.T) {
	access := gateway.TaskView{Agent: "Claude Code", AgentReadOnly: true, ReadOnly: true}
	asked := gateway.TaskView{Agent: "Claude Code", ReadOnly: true}
	for _, tc := range []struct {
		name string
		t    gateway.TaskView
		mode bool
		want []string
	}{
		{"access", access, false, []string{"Settings › Agents"}},
		{"asked", asked, false, []string{"asked to run this task read-only"}},
		{"mode", gateway.TaskView{Agent: "Codex"}, true, []string{"Settings › Safety"}},
		{"access and mode", access, true, []string{"Settings › Agents", "Settings › Safety"}},
		{"none", gateway.TaskView{Agent: "Codex"}, false, nil},
	} {
		got := readOnlyNotes(tc.t, tc.mode)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: %q", tc.name, got)
		}
		for i, w := range tc.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("%s: %q lacks %q", tc.name, got[i], w)
			}
		}
	}

	inLanguage(t, "zh-Hans")
	if got := readOnlyNotes(access, true); len(got) != 2 || !strings.Contains(got[0], "“设置 › 智能体”") || !strings.Contains(got[1], "“设置 › 安全”") {
		t.Errorf("in Chinese: %q", got)
	}
	inLanguage(t, "ja")
	if got := readOnlyNotes(access, true); len(got) != 2 || !strings.Contains(got[0], "「設定 › エージェント」") || !strings.Contains(got[1], "「設定 › 安全」") {
		t.Errorf("in Japanese: %q", got)
	}
}

// The diagnostics name the read-only that Read-only mode does not show:
// the agents with read-only access and the running tasks that are
// read-only, with why.
func TestReadOnlyDiagnostics(t *testing.T) {
	a := &app{}
	a.agents.loaded = true
	a.agents.list = []gateway.AgentView{
		{Name: "Claude Code", Approved: true, Scope: gateway.Scope{Mode: gateway.ScopeReadOnly}},
		{Name: "Codex", Approved: true, Scope: gateway.Scope{Mode: gateway.ScopeEverything}},
	}
	a.tasks = []gateway.TaskView{
		{DeviceID: "phone-a", Agent: "Claude Code", State: gateway.TaskWorking, AgentReadOnly: true},
		{DeviceID: "phone-b", Agent: "Codex", State: gateway.TaskWorking, ReadOnly: true},
		{DeviceID: "phone-c", Agent: "Codex", State: gateway.TaskWorking},
	}
	got := strings.Join(a.readOnlyDiagnostics(), "\n")
	want := "Agents with read-only access (Settings › Agents): Claude Code\n" +
		"Task on phone-a: read-only, the agent's access\n" +
		"Task on phone-b: read-only, as the agent asked"
	if got != want {
		t.Errorf("diagnostics\n%s\nwant\n%s", got, want)
	}
}

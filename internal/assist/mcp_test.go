package assist

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"hal9001/internal/mcpclient"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

type fakeMCP struct {
	servers map[string]mcpclient.Detail
	calls   []string
}

func (f *fakeMCP) List() []mcpclient.View {
	var out []mcpclient.View
	for _, d := range f.servers {
		out = append(out, d.View)
	}
	return out
}

func (f *fakeMCP) Detail(name string) (mcpclient.Detail, error) {
	d, ok := f.servers[name]
	if !ok {
		return mcpclient.Detail{}, mcpclient.ErrUnknownServer
	}
	return d, nil
}

func (f *fakeMCP) RefreshTools(context.Context, string) (mcpclient.Detail, mcpclient.ToolChanges, error) {
	return mcpclient.Detail{}, mcpclient.ToolChanges{}, errors.New("not used")
}

func (f *fakeMCP) Call(_ context.Context, name, tool string, _ map[string]any) (mcpclient.CallResult, error) {
	f.calls = append(f.calls, name+"."+tool)
	return mcpclient.CallResult{Text: "ok"}, nil
}

func (f *fakeMCP) GrantScript(string, string, []string, time.Duration, string, string, string) (*mcpclient.ScriptAccess, error) {
	return nil, nil
}

func mcpSession(t *testing.T, f *fakeMCP) *session {
	t.Helper()
	m := &Manager{home: t.TempDir(), opt: Options{MCP: f}}
	return newSession(m, store.AssistSession{ID: "01JC7Z3K9Q8V5T2M4N6P8R0S1T", TargetKind: store.AssistTargetGeneral, Status: store.AssistIdle})
}

func mcpTool(t *testing.T, s *session, name string) tools.Tool {
	t.Helper()
	for _, tool := range s.mcpTools() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool %s", name)
	return tools.Tool{}
}

func TestMCPToolsExistOnlyWithAClientAndOnlyMCPCallAsks(t *testing.T) {
	s := &session{m: &Manager{}}
	if len(s.mcpTools()) != 0 {
		t.Error("MCP tools without a client")
	}
	f := &fakeMCP{servers: map[string]mcpclient.Detail{"notion": {View: mcpclient.View{Name: "notion", Status: mcpclient.StatusConnected}}}}
	s = mcpSession(t, f)
	for _, name := range []string{"mcp_servers_list", "mcp_server_inspect"} {
		if tl := mcpTool(t, s, name); tl.Approval != nil || tl.Policy != nil {
			t.Errorf("%s needs no approval", name)
		}
	}
	if tl := mcpTool(t, s, "mcp_call"); tl.Policy == nil {
		t.Error("mcp_call has no approval policy")
	}
}

func TestMCPCallAlwaysAsksButRefusesWhatCannotWorkBeforeAskingAnybody(t *testing.T) {
	f := &fakeMCP{servers: map[string]mcpclient.Detail{
		"notion": {View: mcpclient.View{Name: "notion", Status: mcpclient.StatusConnected}, Tools: []mcpclient.Tool{{Name: "search", Description: "Search pages."}}},
		"off":    {View: mcpclient.View{Name: "off", Status: mcpclient.StatusDisconnected}},
		"stale":  {View: mcpclient.View{Name: "stale", Status: mcpclient.StatusNeedsReconnect}},
	}}
	tl := mcpTool(t, mcpSession(t, f), "mcp_call")
	policy := func(args string) tools.Verdict { v, _ := tl.Policy(json.RawMessage(args)); return v }
	if v := policy(`{"server":"notion","tool":"search","args":{"query":"x"}}`); v != tools.VerdictApprove {
		t.Errorf("a call to a connected server: verdict %v, want approval", v)
	}
	for name, args := range map[string]string{
		"unknown server":  `{"server":"ghost","tool":"search"}`,
		"not connected":   `{"server":"off","tool":"search"}`,
		"needs reconnect": `{"server":"stale","tool":"search"}`,
		"no tool":         `{"server":"notion"}`,
		"not JSON":        `[`,
	} {
		if v := policy(args); v != tools.VerdictDeny {
			t.Errorf("%s: verdict %v, want deny", name, v)
		}
	}
	at := tl.Describe(json.RawMessage(`{"server":"notion","tool":"search","args":{"query":"roadmap"}}`), "")
	fields := map[string]string{}
	for _, fl := range at.Fields {
		fields[fl.Key] = fl.Value
	}
	if at.Title != "Call search on notion" || at.Command != "notion.search" || fields["server"] != "notion" || fields["tool"] != "search" ||
		fields["tool description"] != "Search pages." || !strings.Contains(fields["arguments"], `"query": "roadmap"`) || !strings.Contains(at.Description, `"search"`) {
		t.Errorf("approval text = %+v", at)
	}
}

func TestDraftTestApprovalNamesTheMCPServersAndToolsAScriptCalls(t *testing.T) {
	script := "set -e\nhal9001 mcp call notion notion-search --args '{}' | head\nhal9001 mcp call 'linear' list-issues\nhal9001 mcp call notion notion-search\necho '[]'\n"
	if got := strings.Join(mcpCallsIn(script), ","); got != "linear.list-issues,notion.notion-search" {
		t.Errorf("calls = %q", got)
	}
	sensor := &spec.Sensor{Lang: "bash", Script: script, MCPServers: []string{"notion", "linear"}}
	id := scriptOf(sensor, nil)
	fact := mcpFact(id)
	if !strings.Contains(fact, "servers: linear, notion") || !strings.Contains(fact, "calls: linear.list-issues, notion.notion-search") {
		t.Errorf("fact = %q", fact)
	}
	if !strings.Contains(id.text(), "MCP servers the script may call: linear, notion") {
		t.Errorf("the approved text does not name the servers:\n%s", id.text())
	}
	// Granting the script another server is a different script to approve.
	more := scriptOf(&spec.Sensor{Lang: "bash", Script: script, MCPServers: []string{"notion", "linear", "gmail"}}, nil)
	if more.hash() == id.hash() {
		t.Error("a sensor that gained a server kept its approval")
	}
	if none := scriptOf(&spec.Sensor{Lang: "bash", Script: "echo"}, nil); mcpFact(none) != "" {
		t.Errorf("fact of a script without MCP = %q", mcpFact(none))
	}
}

package e2e

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"hal9001/internal/mcpclient/mcptest"
)

// The MCP client end to end: a real daemon connects to a fake remote MCP server behind a fake OAuth authorization server
// (never a real service). Covers connecting through the API and the callback route, a sensor script that calls tools with
// `hal9001 mcp call` and its run token, and the chat assistant's tools, whose call always waits for the user.

// connectMCP runs the whole authorization of server name against the fake with a fake browser.
func (d *Daemon) connectMCP(name string) {
	d.t.Helper()
	var res struct {
		URL string `json:"authorize_url"`
	}
	d.Post("/api/mcp-servers/"+name+"/connect", nil).Want(200).JSON(&res)
	cb, err := mcptest.Approve(context.Background(), res.URL)
	if err != nil {
		d.t.Fatalf("authorize: %v", err)
	}
	u, err := url.Parse(cb)
	if err != nil || u.Path != "/oauth/callback" || u.Host != d.Addr() {
		d.t.Fatalf("the authorization server redirected to %q, want this daemon's /oauth/callback (%s)", cb, d.Addr())
	}
	r := d.Get("/oauth/callback?" + u.RawQuery).Want(302)
	if loc := r.Header.Get("Location"); loc != "/settings/context?mcp_server="+name {
		d.t.Errorf("callback redirect = %q", loc)
	}
}

type sensorRunJSON struct {
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code"`
	Stdout   struct {
		Text string `json:"text"`
	} `json:"stdout"`
	Stderr struct {
		Text string `json:"text"`
	} `json:"stderr"`
	NewCount int `json:"new_count"`
	MCPCalls []struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
	} `json:"mcp_calls"`
}

func (d *Daemon) pollRun(sensor string) sensorRunJSON {
	d.t.Helper()
	sr := d.Poll(sensor)
	var run sensorRunJSON
	d.Get("/api/sensor-runs/" + sr.ID).Want(200).JSON(&run)
	return run
}

func TestMCPServersSensorsCallToolsWithARunTokenAndNeverSeeCredentials(t *testing.T) {
	fake := mcptest.New(mcptest.Options{})
	defer fake.Close()
	other := mcptest.New(mcptest.Options{Mode: mcptest.Header, Header: "X-Api-Key", HeaderValue: "k-1"})
	defer other.Close()
	d := newDaemon(t)
	d.InstallCLI()
	d.Start()

	d.Post("/api/mcp-servers", map[string]any{"name": "notion", "url": fake.URL}).Want(201)
	d.Post("/api/mcp-servers", map[string]any{"name": "keyed", "url": other.URL, "auth": "header", "header": "X-Api-Key", "api_key": "k-1"}).Want(201)
	d.connectMCP("notion")
	var srv struct {
		Status    string `json:"status"`
		ToolCount int    `json:"tool_count"`
	}
	d.Get("/api/mcp-servers/notion").Want(200).JSON(&srv)
	if srv.Status != "connected" || srv.ToolCount != 4 {
		t.Fatalf("notion after connecting: %+v", srv)
	}
	if strings.Contains(d.Read("config.yaml"), "access-") || strings.Contains(d.Read("config.yaml"), "k-1") {
		t.Errorf("a secret is in config.yaml:\n%s", d.Read("config.yaml"))
	}

	// The sensor lists the server; its script calls a tool and prints what it returned as the item.
	d.writeSensor("reader", "interval: 1h\nlang: bash\nmcp_servers: [notion]\nkey: \"{{.echo.message}}\"\nscript: |\n"+
		"  env >&2\n  out=$(hal9001 mcp call notion echo --args '{\"message\":\"hi\"}') || exit 1\n  echo \"[$out]\"\n")
	run := d.pollRun("reader")
	if run.Status != "succeeded" || run.NewCount != 1 {
		t.Fatalf("reader: %+v", run)
	}
	if len(run.MCPCalls) != 1 || run.MCPCalls[0].Server != "notion" || run.MCPCalls[0].Tool != "echo" || !run.MCPCalls[0].OK {
		t.Errorf("mcp_calls = %+v", run.MCPCalls)
	}
	if !strings.Contains(run.Stderr.Text, "HAL9001_RUN_TOKEN=hal_run_") {
		t.Errorf("the script has no run token:\n%s", run.Stderr.Text)
	}
	for _, secret := range []string{"access-", "refresh-", "dyn-client", "k-1"} {
		if strings.Contains(run.Stderr.Text, secret) || strings.Contains(run.Stdout.Text, secret) {
			t.Errorf("the script saw %q", secret)
		}
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0].Tool != "echo" || !strings.HasPrefix(calls[0].Auth, "Bearer access-") {
		t.Errorf("the fake server saw %+v", calls)
	}
	// The token is revoked when the poll ends.
	token := run.Stderr.Text[strings.Index(run.Stderr.Text, "HAL9001_RUN_TOKEN=")+len("HAL9001_RUN_TOKEN="):]
	token, _, _ = strings.Cut(token, "\n")
	d.Post("/api/mcp-servers/notion/call", map[string]any{"tool": "echo"}, withBearer(token)).Want(401)

	// A server the sensor did not declare is refused, and the poll fails.
	d.writeSensor("snoop", "interval: 1h\nlang: bash\nmcp_servers: [keyed]\nkey: \"{{.id}}\"\nscript: |\n  hal9001 mcp call notion echo || exit 1\n  echo '[]'\n")
	snoop := d.pollRun("snoop")
	if snoop.Status != "failed" || snoop.ExitCode == nil || *snoop.ExitCode == 0 || !strings.Contains(snoop.Stderr.Text, "not among this run's mcp_servers") {
		t.Errorf("snoop: %+v", snoop)
	}
	if len(snoop.MCPCalls) != 1 || snoop.MCPCalls[0].OK {
		t.Errorf("snoop calls: %+v", snoop.MCPCalls)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("the undeclared server was called (%d calls)", n)
	}

	// A tool that reports an error fails the script with the message on stderr.
	d.writeSensor("broken", "interval: 1h\nlang: bash\nmcp_servers: [notion]\nkey: \"{{.id}}\"\nscript: |\n  hal9001 mcp call notion fail || exit 1\n  echo '[]'\n")
	if br := d.pollRun("broken"); br.Status != "failed" || !strings.Contains(br.Stderr.Text, "it broke") {
		t.Errorf("broken: %+v", br)
	}

	// The CLI as an admin: structured content, or {"text": ...}; an error exits 1.
	out, _, code := d.CLI("", "mcp", "call", "notion", "plain")
	if code != 0 || strings.TrimSpace(out) != `{"text":"line one\nline two"}` {
		t.Errorf("plain: %d %q", code, out)
	}
	if out, _, code := d.CLI("", "mcp", "call", "notion", "echo", "--args", `{"message":"x"}`); code != 0 || strings.TrimSpace(out) != `{"echo":{"message":"x"}}` {
		t.Errorf("echo: %d %q", code, out)
	}
	if _, errOut, code := d.CLI("", "mcp", "call", "notion", "fail"); code != 1 || !strings.Contains(errOut, "it broke") {
		t.Errorf("fail: %d %q", code, errOut)
	}
	if _, _, code := d.CLI("", "mcp", "call", "notion", "echo", "--args", "[1]"); code != 2 {
		t.Errorf("--args that is not an object: exit %d, want 2", code)
	}

	// A sensor cannot name a server that does not exist, and a server a sensor uses cannot be removed.
	r := d.Post("/api/sensors", map[string]any{"yaml": "lang: bash\nscript: echo '[]'\nkey: k\nmcp_servers: [ghost]\n", "id": "ghosty"}).Want(422)
	if !strings.Contains(string(r.Body), "unknown_mcp_server") {
		t.Errorf("unknown server: %s", r.Body)
	}
	d.Delete("/api/mcp-servers/notion").Want(409)
}

func TestMCPServerTokenRefreshFailureNeedsReconnectAndLeavesAnInboxNote(t *testing.T) {
	fake := mcptest.New(mcptest.Options{})
	defer fake.Close()
	d := newDaemon(t)
	d.Start()
	d.Post("/api/mcp-servers", map[string]any{"name": "notion", "url": fake.URL}).Want(201)
	d.connectMCP("notion")

	fake.RevokeRefreshTokens()
	var res struct {
		IsError bool `json:"is_error"`
	}
	d.Post("/api/mcp-servers/notion/call", map[string]any{"tool": "echo"}).Want(409)
	_ = res
	var srv struct {
		Status string `json:"status"`
	}
	d.Get("/api/mcp-servers/notion").Want(200).JSON(&srv)
	if srv.Status != "needs_reconnect" {
		t.Fatalf("status = %q", srv.Status)
	}
	var note InboxItem
	for _, it := range d.Inbox() {
		if strings.Contains(it.Title, "needs to be reconnected") {
			note = it
		}
	}
	if note.ID == "" {
		t.Fatalf("no reconnect note in the inbox: %+v", d.Inbox())
	}
	var full struct {
		Links []struct {
			Label string `json:"label"`
			URL   string `json:"url"`
		} `json:"links"`
	}
	d.Get("/api/inbox/" + note.ID).Want(200).JSON(&full)
	if len(full.Links) != 1 || full.Links[0].Label != "Reconnect" || !strings.HasSuffix(full.Links[0].URL, "/settings/context?mcp_server=notion") {
		t.Errorf("note links: %+v", full.Links)
	}
	d.connectMCP("notion")
	d.Get("/api/mcp-servers/notion").Want(200).JSON(&srv)
	if srv.Status != "connected" {
		t.Errorf("after reconnecting: %q", srv.Status)
	}
}

func TestAssistantInspectsMCPServersAndEveryCallWaitsForTheUser(t *testing.T) {
	fake := mcptest.New(mcptest.Options{Mode: mcptest.Open})
	defer fake.Close()
	d := newAssistDaemon(t, func(d *Daemon) {
		d.Config("mcp_servers:\n  notion:\n    url: " + fake.URL + "\n    auth: none\n")
		d.assistRuntime(false, turn{When: when("use notion$"), Steps: []step{
			mcpStep("l", "mcp_servers_list", map[string]any{}, "list.json"),
			mcpStep("i", "mcp_server_inspect", map[string]any{"server": "notion"}, "inspect.json"),
			mcpStep("bad", "mcp_call", map[string]any{"server": "ghost", "tool": "echo"}, "ghost.json"),
			mcpStep("c1", "mcp_call", map[string]any{"server": "notion", "tool": "echo", "args": map[string]any{"message": "one"}}, "call1.json"),
			mcpStep("c2", "mcp_call", map[string]any{"server": "notion", "tool": "echo", "args": map[string]any{"message": "two"}}, "call2.json"),
			say("Done."),
		}})
	})
	s := d.assistCreateGeneral()
	d.assistSend(s.ID, "use notion", nil)

	ap := d.waitPendingApproval()
	if ap["type"] != "tool_call" || ap["title"] != "Call echo on notion" || ap["assist_session_id"] != s.ID {
		t.Fatalf("approval = %v", ap)
	}
	details, _ := ap["details"].(map[string]any)
	fields := map[string]string{}
	for _, f := range details["fields"].([]any) {
		kv := f.(map[string]any)
		fields[kv["key"].(string)] = kv["value"].(string)
	}
	if details["command"] != "notion.echo" || fields["server"] != "notion" || fields["tool"] != "echo" ||
		fields["tool description"] != "Return the arguments as structured content." || !strings.Contains(fields["arguments"], `"message": "one"`) {
		t.Errorf("approval details = %v", details)
	}
	// Nothing has run yet: the call is waiting for the user.
	if n := len(fake.Calls()); n != 0 {
		t.Fatalf("the server was called %d times before the approval", n)
	}
	d.assistWait(s.ID, "awaiting_approval")
	d.Post("/api/approvals/"+ap["id"].(string)+"/approve", map[string]any{}).Want(200)

	ap2 := d.waitPendingApproval()
	if ap2["id"] == ap["id"] {
		t.Fatalf("the second call raised no approval: %v", ap2)
	}
	d.assistWait(s.ID, "awaiting_approval")
	d.Post("/api/approvals/"+ap2["id"].(string)+"/deny", map[string]any{"comment": "not that one"}).Want(200)

	got := d.assistWait(s.ID, "idle", "failed")
	if got.Status != "idle" {
		t.Fatalf("status %q error %v", got.Status, got.Error)
	}
	var list struct {
		Servers []struct {
			Name      string `json:"name"`
			Status    string `json:"status"`
			ToolCount int    `json:"tool_count"`
		} `json:"servers"`
	}
	if err := json.Unmarshal([]byte(d.assistFile(s.ID, "list.json")), &list); err != nil || len(list.Servers) != 1 || list.Servers[0].Name != "notion" || list.Servers[0].ToolCount != 0 && list.Servers[0].ToolCount != 4 {
		t.Errorf("mcp_servers_list = %s (%v)", d.assistFile(s.ID, "list.json"), err)
	}
	inspect := d.assistFile(s.ID, "inspect.json")
	if !strings.Contains(inspect, `"name":"search"`) || !strings.Contains(inspect, `"Return the arguments as structured content."`) || !strings.Contains(inspect, `"filters"`) {
		t.Errorf("mcp_server_inspect = %s", inspect)
	}
	if !strings.Contains(d.assistFile(s.ID, "call1.json"), `"message":"one"`) {
		t.Errorf("approved call result = %s", d.assistFile(s.ID, "call1.json"))
	}
	run := d.assistRuns(s.ID)[0]
	if tr := d.Transcript(run.ID); !strings.Contains(tr, "mcp[bad] is_error=true") || !strings.Contains(tr, "mcp[c2] is_error=true") || !strings.Contains(tr, `Denied by the user: \"not that one\"`) || !strings.Contains(tr, "mcp[c1] is_error=false") {
		t.Errorf("transcript:\n%s", tr)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Args["message"] != "one" {
		t.Errorf("the server saw %+v, want only the approved call", calls)
	}
	if got := d.halPerms(run.ID, "mcp_call"); len(got) != 3 {
		t.Errorf("mcp_call records = %d", len(got))
	}
	if got := d.halPerms(run.ID, "mcp_servers_list"); len(got) != 1 || decisionOf(got[0]) != "allow" {
		t.Errorf("mcp_servers_list records = %+v", got)
	}
}

// withBearer authenticates a request with a bearer token instead of the session.
func withBearer(token string) ReqOpt {
	return func(rs *reqSpec) {
		rs.header.Set("Authorization", "Bearer "+token)
		rs.noCookie = true
	}
}

// writeSensor writes a sensor file and waits until the daemon's watcher has loaded it.
func (d *Daemon) writeSensor(id, yaml string) {
	d.t.Helper()
	d.Sensor(id, yaml)
	d.Eventually("sensor "+id+" loaded", func() bool {
		var l struct {
			Items []struct {
				ID     string `json:"id"`
				Sensor any    `json:"sensor"`
			} `json:"items"`
		}
		d.Get("/api/sensors").Want(200).JSON(&l)
		for _, it := range l.Items {
			if it.ID == id && it.Sensor != nil {
				return true
			}
		}
		return false
	})
}

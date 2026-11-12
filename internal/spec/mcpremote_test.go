package spec

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func hasDiag(ds []Diagnostic, code, path string) bool {
	for _, d := range ds {
		if d.Code == code && d.Path == path && d.Severity == SeverityError {
			return true
		}
	}
	return false
}

func TestRemoteMCPValidation(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		code string
		path string
	}{
		{"http to a remote host", "mcp_servers:\n  notion:\n    url: http://mcp.example.com/mcp\n", CodeInvalidValue, "mcp_servers.notion.url"},
		{"no url", "mcp_servers:\n  notion:\n    auth: none\n", CodeRequired, "mcp_servers.notion.url"},
		{"unknown auth mode", "mcp_servers:\n  notion:\n    url: https://mcp.example.com/mcp\n    auth: basic\n", CodeInvalidEnum, "mcp_servers.notion.auth"},
		{"pasted client without an id", "mcp_servers:\n  notion:\n    url: https://mcp.example.com/mcp\n    auth: oauth_client\n", CodeRequired, "mcp_servers.notion.client_id"},
		{"client id on a key server", "mcp_servers:\n  notion:\n    url: https://mcp.example.com/mcp\n    auth: header\n    header: X-Api-Key\n    client_id: abc\n", CodeFieldNotAllowed, "mcp_servers.notion.client_id"},
		{"key server without a header name", "mcp_servers:\n  notion:\n    url: https://mcp.example.com/mcp\n    auth: header\n", CodeRequired, "mcp_servers.notion.header"},
		{"header the transport owns", "mcp_servers:\n  notion:\n    url: https://mcp.example.com/mcp\n    auth: header\n    header: Content-Type\n", CodeInvalidValue, "mcp_servers.notion.header"},
		{"uppercase name", "mcp_servers:\n  Notion:\n    url: https://mcp.example.com/mcp\n", CodeInvalidID, "mcp_servers.Notion"},
		{"scopes on a key server", "mcp_servers:\n  notion:\n    url: https://mcp.example.com/mcp\n    auth: none\n    scopes: [read]\n", CodeFieldNotAllowed, "mcp_servers.notion.scopes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ds := CheckConfig([]byte(tt.yaml))
			if !hasDiag(ds, tt.code, tt.path) {
				t.Errorf("want %s at %s, got %v", tt.code, tt.path, ds)
			}
		})
	}
}

func TestRemoteMCPAcceptsEveryAuthMode(t *testing.T) {
	const cfg = `mcp_servers:
  notion:
    url: https://mcp.notion.com/mcp
  linear:
    url: https://mcp.linear.app/mcp
    auth: oauth_client
    client_id: abc
    scopes: [read, write]
  internal:
    url: https://tools.example.com/mcp
    auth: header
    header: X-Api-Key
  local:
    url: http://127.0.0.1:3000/mcp
    auth: none
`
	c, ds := CheckConfig([]byte(cfg))
	if len(ds) != 0 || c == nil {
		t.Fatalf("diagnostics: %v", ds)
	}
	if got := c.MCPServers["notion"].AuthMode(); got != MCPAuthOAuth {
		t.Errorf("default auth mode = %q, want oauth", got)
	}
	if !c.MCPServers["linear"].UsesOAuth() || c.MCPServers["internal"].UsesOAuth() {
		t.Errorf("UsesOAuth: linear %v internal %v", c.MCPServers["linear"].UsesOAuth(), c.MCPServers["internal"].UsesOAuth())
	}
}

func TestSensorMCPServersMustExist(t *testing.T) {
	const body = "lang: bash\nscript: echo\nkey: k\nmcp_servers: [notion, ghost]\n"
	refs := &Refs{MCPServers: map[string]bool{"notion": true}}
	_, ds := CheckSensor("s", []byte(body), refs)
	if !hasDiag(ds, CodeUnknownMCPServer, "mcp_servers[1]") || hasDiag(ds, CodeUnknownMCPServer, "mcp_servers[0]") {
		t.Errorf("want unknown_mcp_server only for ghost, got %v", ds)
	}
	if _, ds := CheckSensor("s", []byte(body), nil); len(ds) != 0 {
		t.Errorf("without refs the names are not looked up: %v", ds)
	}
	_, ds = CheckSensor("s", []byte("lang: bash\nscript: echo\nkey: k\nmcp_servers: [notion, notion]\n"), refs)
	if !hasDiag(ds, CodeDuplicateID, "mcp_servers[1]") {
		t.Errorf("want a duplicate finding, got %v", ds)
	}
}

func TestSetRemoteMCPKeepsComments(t *testing.T) {
	const raw = `# my config
max_agent_runs: 3   # keep
mcp_servers:
  # the notion server
  notion:
    url: https://old.example.com/mcp   # was here
`
	out, changed, err := EditConfigYAML([]byte(raw), func(root *yaml.Node) (bool, error) {
		a, err := SetRemoteMCP(root, "notion", RemoteMCP{URL: "https://mcp.notion.com/mcp", Scopes: []string{"read"}})
		if err != nil {
			return false, err
		}
		b, err := SetRemoteMCP(root, "tools", RemoteMCP{URL: "https://tools.example.com/mcp", Auth: MCPAuthHeader, Header: "X-Api-Key"})
		return a || b, err
	})
	if err != nil || !changed {
		t.Fatalf("edit: changed %v err %v", changed, err)
	}
	s := string(out)
	for _, want := range []string{"# my config", "max_agent_runs: 3 # keep", "# the notion server", "https://mcp.notion.com/mcp", "scopes: [read]", "header: X-Api-Key"} {
		if !strings.Contains(s, want) {
			t.Errorf("result lost %q:\n%s", want, s)
		}
	}
	c, ds := CheckConfig(out)
	if len(ds) != 0 || c.MCPServers["tools"].Header != "X-Api-Key" || c.MCPServers["notion"].URL != "https://mcp.notion.com/mcp" {
		t.Fatalf("round trip: %v %+v", ds, c.MCPServers)
	}
	// Removing the last server takes the section with it.
	out, changed, err = EditConfigYAML(out, func(root *yaml.Node) (bool, error) {
		a, b := RemoveRemoteMCP(root, "notion"), RemoveRemoteMCP(root, "tools")
		return a && b, nil
	})
	if err != nil || !changed {
		t.Fatalf("remove: %v", err)
	}
	if strings.Contains(string(out), "mcp_servers") {
		t.Errorf("empty mcp_servers section left behind:\n%s", out)
	}
}

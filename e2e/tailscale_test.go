package e2e

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"hal9001/internal/spec"
	"hal9001/internal/tailscale/tailscaletest"
)

// tailscaleDaemon is a daemon whose only tailscale CLI is the stub (see Daemon.Start).
func tailscaleDaemon(t *testing.T, config string) (*Daemon, *tailscaletest.Stub) {
	t.Helper()
	stub := tailscaletest.New(t)
	d := newDaemon(t)
	d.Script("tailscale", tailscaletest.Script(stub.Dir))
	d.Config(config)
	return d, stub
}

// Sharing on the tailnet end to end: the CLI asks the daemon, the daemon runs the stub `tailscale serve`, writes
// allowed_hosts, and the Host check then lets the tailnet name through to the auth stack.
func TestTailscaleServeThroughTheCLIOpensTheHostCheck(t *testing.T) {
	d, stub := tailscaleDaemon(t, "# my config\nallowed_hosts: [other.example.com]\n")
	d.Start()

	before := d.Get("/api/status", withHost(tailscaletest.DefaultDNS))
	if before.Status != 403 || before.Problem() != "host-not-allowed" {
		t.Fatalf("before sharing: %s", before)
	}

	out, errOut, code := d.CLI("", "tailscale", "serve")
	if code != 0 {
		t.Fatalf("tailscale serve: exit %d\n%s%s", code, out, errOut)
	}
	if got := stub.Mutations(); !slices.Equal(got, []string{"serve --bg --yes --https=443 http://127.0.0.1:" + strconv.Itoa(d.Port())}) {
		t.Errorf("serve invocations = %q", got)
	}
	cfg := d.Read("config.yaml")
	if !strings.Contains(cfg, "# my config") || !strings.Contains(cfg, tailscaletest.DefaultDNS) || !strings.Contains(cfg, "other.example.com") {
		t.Errorf("config.yaml:\n%s", cfg)
	}

	// The daemon reloads the config: the tailnet name now reaches authentication (401: the stack wants an identity).
	after := d.Get("/api/status", withHost(tailscaletest.DefaultDNS))
	if after.Status != 401 {
		t.Errorf("after sharing: %s", after)
	}
	d.sameAsAPI("tailscale status", d.cliJSON("", "tailscale", "status"), "/api/tailscale")

	if _, errOut, code := d.CLI("", "tailscale", "unserve"); code != 0 {
		t.Fatalf("tailscale unserve: exit %d %s", code, errOut)
	}
	if stub.Served(443) != "" {
		t.Errorf("mapping after unserve = %q", stub.Served(443))
	}
	cfg = d.Read("config.yaml")
	if strings.Contains(cfg, tailscaletest.DefaultDNS) || !strings.Contains(cfg, "other.example.com") {
		t.Errorf("config.yaml after unserve:\n%s", cfg)
	}
}

// With tailscale.serve on, a start puts the mapping and the allowed_hosts entry back.
func TestTailscaleServeIsReappliedAtStart(t *testing.T) {
	d, stub := tailscaleDaemon(t, "tailscale: {serve: true}\n")
	d.Start()
	d.Eventually("the mapping is back", func() bool { return stub.Served(443) == "http://127.0.0.1:"+strconv.Itoa(d.Port()) })
	d.Eventually("allowed_hosts has the name", func() bool {
		cfg, _ := spec.ParseConfig([]byte(d.Read("config.yaml")))
		return cfg != nil && slices.Contains(cfg.AllowedHosts, tailscaletest.DefaultDNS)
	})
	if got := stub.Mutations(); len(got) != 1 {
		t.Errorf("serve invocations = %q", got)
	}
}

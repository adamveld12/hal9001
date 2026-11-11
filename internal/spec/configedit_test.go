package spec

import (
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func edit(t *testing.T, raw string, fn func(root *yaml.Node) (bool, error)) (string, bool) {
	t.Helper()
	out, changed, err := EditConfigYAML([]byte(raw), fn)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), changed
}

func TestAddAllowedHostKeepsCommentsAndEntries(t *testing.T) {
	raw := "# top\nallowed_hosts:\n  - a.example.com # mine\n  # second\n  - b.example.com\nmax_agent_runs: 3 # keep\n"
	out, changed := edit(t, raw, func(r *yaml.Node) (bool, error) { return AddAllowedHost(r, "mac.ts.net") })
	if !changed {
		t.Fatal("not changed")
	}
	for _, want := range []string{"# top", "# mine", "# second", "max_agent_runs: 3 # keep", "- mac.ts.net"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lost %q:\n%s", want, out)
		}
	}
	cfg, ds := ParseConfig([]byte(out))
	if cfg == nil || HasErrors(ds) || !slices.Equal(cfg.AllowedHosts, []string{"a.example.com", "b.example.com", "mac.ts.net"}) {
		t.Errorf("allowed_hosts = %v, %v", cfg.AllowedHosts, ds)
	}
}

func TestAddAllowedHostForms(t *testing.T) {
	for name, raw := range map[string]string{
		"flow list":   "allowed_hosts: []\n",
		"null":        "allowed_hosts:\n",
		"missing":     "max_agent_runs: 3\n",
		"empty file":  "",
		"flow filled": "allowed_hosts: [x.example.com]\n",
	} {
		t.Run(name, func(t *testing.T) {
			out, changed := edit(t, raw, func(r *yaml.Node) (bool, error) { return AddAllowedHost(r, "mac.ts.net") })
			cfg, ds := ParseConfig([]byte(out))
			if !changed || cfg == nil || HasErrors(ds) || !slices.Contains(cfg.AllowedHosts, "mac.ts.net") {
				t.Errorf("changed %v, hosts %v, %v\n%s", changed, cfg.AllowedHosts, ds, out)
			}
		})
	}
}

func TestAddAllowedHostIsIdempotentAndCaseInsensitive(t *testing.T) {
	raw := "allowed_hosts: [Mac.TS.net]\n"
	out, changed := edit(t, raw, func(r *yaml.Node) (bool, error) { return AddAllowedHost(r, "mac.ts.net") })
	if changed || out != raw {
		t.Errorf("a present host rewrote the file: changed %v\n%s", changed, out)
	}
}

func TestRemoveAllowedHostRemovesOnlyThatHost(t *testing.T) {
	raw := "allowed_hosts: [a.example.com, mac.ts.net, MAC.ts.net]\n"
	out, changed := edit(t, raw, func(r *yaml.Node) (bool, error) { return RemoveAllowedHost(r, "mac.ts.net"), nil })
	cfg, _ := ParseConfig([]byte(out))
	if !changed || !slices.Equal(cfg.AllowedHosts, []string{"a.example.com"}) {
		t.Errorf("changed %v, hosts %v", changed, cfg.AllowedHosts)
	}
	if _, changed := edit(t, raw, func(r *yaml.Node) (bool, error) { return RemoveAllowedHost(r, "none.ts.net"), nil }); changed {
		t.Error("removing an absent host reported a change")
	}
}

func TestSetTailscaleServe(t *testing.T) {
	out, changed := edit(t, "max_agent_runs: 3\n", func(r *yaml.Node) (bool, error) { return SetTailscaleServe(r, true, 8443) })
	cfg, ds := ParseConfig([]byte(out))
	if !changed || cfg == nil || HasErrors(ds) || !cfg.Tailscale.Serve || cfg.Tailscale.HTTPSPort != 8443 {
		t.Fatalf("changed %v, %+v, %v\n%s", changed, cfg, ds, out)
	}
	// Existing keys and their comments stay; same values change nothing.
	raw := "tailscale:\n  serve: true # on\n  https_port: 8443\n"
	if out, changed := edit(t, raw, func(r *yaml.Node) (bool, error) { return SetTailscaleServe(r, true, 8443) }); changed || out != raw {
		t.Errorf("identical values rewrote the file:\n%s", out)
	}
	out, changed = edit(t, raw, func(r *yaml.Node) (bool, error) { return SetTailscaleServe(r, false, 8443) })
	if !changed || !strings.Contains(out, "# on") || !strings.Contains(out, "serve: false") {
		t.Errorf("turning off:\n%s", out)
	}
}

func TestTailscaleConfigDefaultsAndValidation(t *testing.T) {
	cfg, ds := CheckConfig(nil)
	if cfg == nil || HasErrors(ds) || cfg.Tailscale.Serve || cfg.Tailscale.HTTPSPort != DefaultTailscaleHTTPSPort {
		t.Fatalf("defaults: %+v %v", cfg, ds)
	}
	if m := cfg.Minimal(); m.Tailscale.HTTPSPort != 0 {
		t.Errorf("the default port is written out: %+v", m.Tailscale)
	}
	_, ds = CheckConfig([]byte("tailscale: {serve: true, https_port: 70000}\n"))
	if !HasErrors(ds) || !strings.Contains(ds[0].Path, "tailscale.https_port") {
		t.Errorf("out-of-range port accepted: %v", ds)
	}
}

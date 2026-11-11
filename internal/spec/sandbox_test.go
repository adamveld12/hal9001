package spec

import (
	"strings"
	"testing"
	"time"
)

func codes(ds []Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Path+":"+d.Code)
	}
	return strings.Join(out, " ")
}

func TestCheckSandboxValidation(t *testing.T) {
	for _, tc := range []struct {
		name, backend, yaml, want string
	}{
		{"empty file is the disabled default", SandboxDocker, "", ""},
		{"disabled backend may be half filled", SandboxSSH, "host: h\n", ""},
		{"ssh needs host and user when enabled", SandboxSSH, "enabled: true\n", "host:required user:required"},
		{"ssh fingerprint format", SandboxSSH, "enabled: true\nhost: h\nuser: u\nhost_key: abc\n", "host_key:invalid_value"},
		{"docker needs an image when enabled", SandboxDocker, "enabled: true\nimages: []\n", "images:required"},
		{"lifecycle enum", SandboxLocal, "lifecycle: forever\n", "lifecycle:invalid_enum"},
		{"approval enum", SandboxLocal, "approval: sometimes\n", "approval:invalid_enum"},
		{"bad regex and action", SandboxLocal, "approval_rules:\n  shell:\n    - {match: '(', action: maybe}\n", "approval_rules.shell[0].match:invalid_regex approval_rules.shell[0].action:invalid_enum"},
		{"unknown field", SandboxLocal, "bogus: 1\n", "bogus:unknown_field"},
		{"depot vcpus", SandboxDepot, "enabled: true\nresources: {cpus: 1}\n", "resources.cpus:invalid_value"},
		{"depot ok", SandboxDepot, "enabled: true\narchitecture: arm64\nresources: {cpus: 4}\n", ""},
		{"keep_alive accepts a duration", SandboxLocal, "keep_alive: 2h\n", ""},
		{"keep_alive accepts zero", SandboxLocal, "keep_alive: 0\n", ""},
		{"keep_alive rejects a negative duration", SandboxLocal, "keep_alive: -1m\n", "keep_alive:invalid_value"},
		{"keep_alive rejects text", SandboxLocal, "keep_alive: soon\n", "keep_alive:yaml_type"},
		{"keep_alive needs a unit", SandboxLocal, "keep_alive: 30\n", "keep_alive:yaml_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ds := CheckSandbox(tc.backend, []byte(tc.yaml))
			if got := codes(ds); got != tc.want {
				t.Fatalf("diagnostics = %q, want %q", got, tc.want)
			}
		})
	}
	if _, ds := CheckSandbox("nope", nil); !HasErrors(ds) {
		t.Fatal("unknown backend accepted")
	}
}

func TestSandboxKeepAlive(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		want       time.Duration
	}{
		{"absent means the default", "approval: auto\n", time.Minute},
		{"seconds", "keep_alive: 30s\n", 30 * time.Second},
		{"hours", "keep_alive: 2h\n", 2 * time.Hour},
		{"days", "keep_alive: 1d\n", 24 * time.Hour},
		{"zero removes at once", "keep_alive: 0\n", 0},
		{"zero with a unit", "keep_alive: 0s\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ds := CheckSandbox(SandboxLocal, []byte(tc.yaml))
			if HasErrors(ds) {
				t.Fatalf("diagnostics: %v", ds)
			}
			if got := s.KeepAlive.Std(); got != tc.want {
				t.Fatalf("keep_alive = %v, want %v", got, tc.want)
			}
			// A written file reads back the same, so 0 is not turned into the default.
			b, err := MarshalSandbox(*s)
			if err != nil {
				t.Fatal(err)
			}
			again, ds := CheckSandbox(SandboxLocal, b)
			if HasErrors(ds) || again.KeepAlive != s.KeepAlive {
				t.Fatalf("round trip: %v %v\n%s", again.KeepAlive, ds, b)
			}
		})
	}
}

func TestSandboxRoundTrip(t *testing.T) {
	s := DefaultSandbox(SandboxDocker)
	s.Enabled, s.Network, s.Images = true, false, []string{"alpine:3", "ubuntu:24.04"}
	s.Rules.Shell = []SandboxShellRule{{Match: "^ls", Action: RuleAuto}}
	b, err := MarshalSandbox(s)
	if err != nil {
		t.Fatal(err)
	}
	got, ds := CheckSandbox(SandboxDocker, b)
	if HasErrors(ds) || len(ds) > 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	if got.Network || len(got.Images) != 2 || got.Rules.Shell[0].Match != "^ls" || !got.Enabled {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestAgentNodeSandboxRefs(t *testing.T) {
	refs := &Refs{Sandboxes: map[string]Sandbox{
		SandboxDocker: {Enabled: true, Images: []string{"alpine:3"}},
		SandboxLocal:  {Enabled: true},
	}}
	for _, tc := range []struct{ name, node, want string }{
		{"enabled backend and listed image", "sandbox: docker\n    image: alpine:3", ""},
		{"local without image", "sandbox: local\n    repo: https://x/y\n    ref: main", ""},
		{"disabled backend", "sandbox: ssh", "nodes[0].sandbox:unknown_sandbox"},
		{"unknown backend", "sandbox: nope", "nodes[0].sandbox:invalid_enum"},
		{"image not listed", "sandbox: docker\n    image: ubuntu", "nodes[0].image:invalid_enum"},
		{"image on local", "sandbox: local\n    image: alpine:3", "nodes[0].image:field_not_allowed"},
		{"image without sandbox", "image: alpine:3", "nodes[0].image:field_not_allowed"},
		{"ref without repo", "sandbox: local\n    ref: main", "nodes[0].repo:required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y := "id: f\nenabled: true\nsubscribe: [s]\nnodes:\n  - id: a\n    type: agent\n    runtime: r\n    mode: auto\n    instructions: hi\n    " + tc.node + "\n"
			refs.Sensors, refs.Runtimes = map[string]bool{"s": true}, map[string]Runtime{"r": {}}
			_, ds := CheckFlow("f", []byte(y), refs)
			var got []string
			for _, d := range ds {
				if d.Severity == SeverityError {
					got = append(got, d.Path+":"+d.Code)
				}
			}
			if strings.Join(got, " ") != tc.want {
				t.Fatalf("diagnostics = %v, want %q", ds, tc.want)
			}
		})
	}
}

func TestLocalSandboxIsAlwaysOn(t *testing.T) {
	home := t.TempDir()
	put(t, home, "sensors/s.yaml", "id: s\ninterval: 1m\nlang: bash\nscript: echo\nkey: k\n")
	put(t, home, "prompts/p.md", "---\nname: p\n---\nb\n")
	put(t, home, "config.yaml", "runtimes:\n  claude: {command: claude}\n")
	put(t, home, "flows/f.yaml", "id: f\nenabled: true\nsubscribe: [s]\nnodes:\n  - {id: work, type: agent, runtime: claude, mode: auto, prompt: p, sandbox: local}\n")
	if got := Load(home).Flows[0].Diagnostics; HasErrors(got) {
		t.Fatalf("sandbox: local without a sandbox file is rejected: %v", got)
	}
	s, ds := CheckSandbox(SandboxLocal, nil)
	if len(ds) != 0 || !s.Enabled || len(s.Rules.FSWrite) != 1 || s.Rules.FSWrite[0].Action != RuleAuto {
		t.Fatalf("local default = %+v %v", s, ds)
	}
	s, ds = CheckSandbox(SandboxLocal, []byte("enabled: false\n"))
	if HasErrors(ds) || len(ds) != 1 || ds[0].Severity != SeverityWarning || !s.Enabled {
		t.Fatalf("enabled: false = %+v %v", s, ds)
	}
}

func TestDepotRequiresToken(t *testing.T) {
	home := t.TempDir()
	cfg := []byte("enabled: true\norg_id: org-1\n")
	if _, ds := CheckSandboxIn(home, SandboxDepot, cfg); !HasErrors(ds) || !strings.Contains(ds[0].Message, "Depot token is required") {
		t.Fatalf("enabled depot without a token: %v", ds)
	}
	if _, ds := CheckSandboxIn(home, SandboxDepot, []byte("org_id: org-1\n")); len(ds) != 0 {
		t.Fatalf("disabled depot needs no token: %v", ds)
	}
	put(t, home, "env.d/sandboxes/depot.env", "DEPOT_TOKEN=tok-123456\n")
	if _, ds := CheckSandboxIn(home, SandboxDepot, cfg); len(ds) != 0 {
		t.Fatalf("enabled depot with a token: %v", ds)
	}
}

// A disabled or unknown sandbox, an image outside the list, and an unknown tool each list the valid options.
func TestAgentNodeConfigDiagnosticsListOptions(t *testing.T) {
	refs := &Refs{
		Runtimes:  map[string]Runtime{"claude": {}},
		Tools:     map[string]bool{"lint": true},
		Sandboxes: map[string]Sandbox{SandboxLocal: {Enabled: true}, SandboxDocker: {Enabled: true, Images: []string{"alpine:3"}}},
	}
	for _, tc := range []struct{ node, want string }{
		{"sandbox: kubernetes", "enable it in Configure \u2192 Sandboxes \u2192 Kubernetes"},
		{"sandbox: kubernetes", "(available: docker, local)"},
		{"sandbox: docker\n    image: nope", "(available: alpine:3)"},
		{"tools: [{id: nope}]", "(available: lint)"},
	} {
		_, ds := CheckFlow("f", []byte("subscribe: []\nnodes:\n  - id: a\n    type: agent\n    runtime: claude\n    instructions: hi\n    "+tc.node+"\nedges: []\n"), refs)
		var msgs []string
		for _, d := range ds {
			msgs = append(msgs, d.Message)
		}
		if !strings.Contains(strings.Join(msgs, "\n"), tc.want) {
			t.Errorf("%q: diagnostics %q lack %q", tc.node, msgs, tc.want)
		}
	}
}

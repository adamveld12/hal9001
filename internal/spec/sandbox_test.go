package spec

import (
	"strings"
	"testing"
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

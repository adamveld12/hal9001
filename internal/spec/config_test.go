package spec

import (
	"strings"
	"testing"
	"time"
)

const requirementsConfig = `
max_agent_runs: 2
max_command_runs: 8
retention_days: 30
dedup_retention_days: 180
notify_min_priority: high
approval_timeout: 1h
allowed_hosts: [my-mac.my-tailnet.ts.net]
allowed_logins: []
runtimes:
  claude: {command: [claude-agent-acp], modes: {ask: default, full: bypassPermissions}}
  codex:  {command: [codex-acp],        modes: {ask: read-only, full: full-access}}
  omp:    {command: [omp, acp],         modes: {ask: TBD, full: TBD}}
auth:
  stacks:
    default:
      - {module: localhost-trust, control: sufficient}
      - {module: tailscale-identity, control: sufficient}
      - {module: webauthn, control: sufficient}
      - {module: password, control: sufficient}
  services: {ui: default, api: default, approve: default, admin: default}
`

func TestRequirementsConfigLoadsClean(t *testing.T) {
	cfg, ds := CheckConfig([]byte(requirementsConfig))
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	rt := cfg.Runtimes["omp"]
	if strings.Join(rt.Command, " ") != "omp acp" || rt.Modes.Ask != "TBD" {
		t.Errorf("omp runtime = %+v", rt)
	}
	if cfg.NotifyMinPriority != PriorityHigh || cfg.AllowedHosts[0] != "my-mac.my-tailnet.ts.net" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestEmptyConfigGetsDefaults(t *testing.T) {
	for _, in := range []string{"", "# only a comment\n"} {
		cfg, ds := ParseConfig([]byte(in))
		if len(ds) != 0 || cfg == nil {
			t.Fatalf("%q: %v %v", in, cfg, ds)
		}
		if cfg.MaxAgentRuns != 2 || cfg.MaxCommandRuns != 8 || cfg.RetentionDays != 30 || cfg.DedupRetentionDays != 180 ||
			cfg.NotifyMinPriority != PriorityNormal || cfg.SessionTTL.Std() != 7*24*time.Hour || cfg.ApprovalTimeout.Std() != time.Hour ||
			cfg.UpdateChannel != UpdateChannelStable || cfg.UpdateAutoInstall {
			t.Errorf("defaults wrong: %+v", cfg)
		}
	}
}

func TestDefaultAuthStack(t *testing.T) {
	cfg, _ := ParseConfig(nil)
	stack := cfg.Auth.Stacks[DefaultStack]
	var names []string
	for _, m := range stack {
		if m.Control != ControlSufficient {
			t.Errorf("%s control = %s, want sufficient", m.Module, m.Control)
		}
		names = append(names, m.Module)
	}
	if got := strings.Join(names, ","); got != "localhost-trust,tailscale-identity,webauthn,password" {
		t.Errorf("default stack = %s", got)
	}
	for _, svc := range Services {
		if cfg.Auth.Services[svc] != DefaultStack {
			t.Errorf("service %s -> %q, want default", svc, cfg.Auth.Services[svc])
		}
	}
}

func TestPartialAuthServicesFallBackToDefaultStack(t *testing.T) {
	cfg, ds := CheckConfig([]byte(`
auth:
  stacks:
    default: [{module: localhost-trust, control: sufficient}]
    strict:  [{module: webauthn, control: required}]
  services: {admin: strict}
`))
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	if cfg.Auth.Services[ServiceAdmin] != "strict" || cfg.Auth.Services[ServiceUI] != DefaultStack {
		t.Errorf("services = %v", cfg.Auth.Services)
	}
}

func TestConfigValidationErrors(t *testing.T) {
	tests := []struct {
		name, yaml, code, path, msg string
	}{
		{"negative concurrency", "max_agent_runs: -1\n", CodeInvalidValue, "max_agent_runs", "must be positive"},
		{"priority enum", "notify_min_priority: loud\n", CodeInvalidEnum, "notify_min_priority", `"loud" is not valid (one of low | normal | high | urgent)`},
		{"negative session ttl", "session_ttl: -1h\n", CodeInvalidValue, "session_ttl", "must not be negative"},
		{"host with scheme", "allowed_hosts: ['https://x.ts.net']\n", CodeInvalidValue, "allowed_hosts[0]", "bare hostname"},
		{"runtime without command", "runtimes: {x: {modes: {ask: a}}}\n", CodeRequired, "runtimes.x.command", "needs a command"},
		{"update channel enum", "update_channel: nightly\n", CodeInvalidEnum, "update_channel", `"nightly" is not valid (one of stable | beta)`},
		{"unknown module", "auth: {stacks: {default: [{module: pam, control: sufficient}]}}\n", CodeUnknownModule, "auth.stacks.default[0].module", `unknown auth module "pam"`},
		{"control enum", "auth: {stacks: {default: [{module: password, control: maybe}]}}\n", CodeInvalidEnum, "auth.stacks.default[0].control", `"maybe" is not valid`},
		{"service to missing stack", "auth: {services: {admin: nope}}\n", CodeUnknownStack, "auth.services.admin", `stack "nope", which is not defined`},
		{"unknown service", "auth: {services: {root: default}}\n", CodeInvalidEnum, "auth.services.root", `unknown service "root"`},
		{"unknown key", "port: 9002\n", CodeUnknownField, "port", `unknown field "port"`},
		{"steers too high", "max_steers_per_run: 101\n", CodeInvalidValue, "max_steers_per_run", "between 1 and 100"},
		{"steers negative", "max_steers_per_run: -1\n", CodeInvalidValue, "max_steers_per_run", "between 1 and 100"},
		{"bad duration", "approval_timeout: forever\n", CodeYAMLType, "approval_timeout", `invalid duration "forever"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ds := CheckConfig([]byte(tt.yaml))
			for _, d := range ds {
				if d.Code == tt.code && d.Path == tt.path && strings.Contains(d.Message, tt.msg) && d.File == "config.yaml" {
					return
				}
			}
			t.Fatalf("want %s at %s containing %q in %v", tt.code, tt.path, tt.msg, ds)
		})
	}
}

func TestConfigMarshalOmitsDefaults(t *testing.T) {
	cfg, _ := ParseConfig([]byte("notify_min_priority: urgent\nallowed_hosts: [a.ts.net]\n"))
	out, err := MarshalConfig(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "auth") || strings.Contains(string(out), "retention") {
		t.Errorf("defaults written:\n%s", out)
	}
	back, ds := ParseConfig(out)
	if len(ds) != 0 || mustJSON(t, back) != mustJSON(t, cfg) {
		t.Errorf("round trip: %v\n%s", ds, out)
	}
}

const gateRuntimes = `
runtimes:
  claude:
    command: [claude-agent-acp]
    modes: {ask: default, full: bypassPermissions}
    session_meta:
      claudeCode: {options: {settingSources: [project]}}
  omp:
    command: [omp, acp]
    modes: {ask: "", full: ""}
    ask: {args: [--approval-mode, always-ask]}
    full: {args: [--approval-mode, yolo], env: {EXTRA: "1"}}
    env:
      PI_CODING_AGENT_DIR: "{{data}}/runtimes/omp-home"
    elicitation: true
`

func TestGateRuntimeBlockParsesCleanAndRoundTrips(t *testing.T) {
	cfg, ds := CheckConfig([]byte(gateRuntimes))
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	omp := cfg.Runtimes["omp"]
	if strings.Join(omp.Ask.Args, " ") != "--approval-mode always-ask" || omp.Full.Env["EXTRA"] != "1" ||
		!omp.Elicitation || omp.Env["PI_CODING_AGENT_DIR"] != DataPlaceholder+"/runtimes/omp-home" {
		t.Errorf("omp = %+v", omp)
	}
	meta, _ := cfg.Runtimes["claude"].SessionMeta["claudeCode"].(map[string]any)
	if meta == nil {
		t.Fatalf("claude session_meta = %+v", cfg.Runtimes["claude"].SessionMeta)
	}
	out, err := MarshalConfig(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	again, ds := CheckConfig(out)
	if len(ds) != 0 || strings.Join(again.Runtimes["omp"].Full.Args, " ") != "--approval-mode yolo" || !again.Runtimes["omp"].Elicitation {
		t.Fatalf("round trip lost runtime fields: %v\n%s", ds, out)
	}
}

func TestRuntimeSpawnValidation(t *testing.T) {
	tests := []struct {
		name, yaml, path, msg string
	}{
		{"env name", "runtimes: {x: {command: [a], env: {'BAD-NAME': v}}}\n", "runtimes.x.env.BAD-NAME", "invalid environment variable name"},
		{"ask env name", "runtimes: {x: {command: [a], ask: {env: {'1X': v}}}}\n", "runtimes.x.ask.env.1X", "invalid environment variable name"},
		{"full empty arg", "runtimes: {x: {command: [a], full: {args: ['']}}}\n", "runtimes.x.full.args[0]", "must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ds := CheckConfig([]byte(tt.yaml))
			for _, d := range ds {
				if d.Code == CodeInvalidValue && d.Path == tt.path && strings.Contains(d.Message, tt.msg) {
					return
				}
			}
			t.Fatalf("want %s containing %q in %v", tt.path, tt.msg, ds)
		})
	}
}

func TestMaxSteersPerRun(t *testing.T) {
	cfg, ds := ParseConfig([]byte("{}\n"))
	if len(ds) != 0 || cfg.MaxSteers() != DefaultMaxSteersPerRun {
		t.Fatalf("default: %v %d", ds, cfg.MaxSteers())
	}
	cfg, _ = ParseConfig([]byte("max_steers_per_run: 5\n"))
	if cfg.MaxSteers() != 5 {
		t.Errorf("MaxSteers = %d", cfg.MaxSteers())
	}
	if _, ds := CheckConfig([]byte("max_steers_per_run: 100\n")); len(ds) != 0 {
		t.Errorf("100 is in range: %v", ds)
	}
	def := Config{MaxSteersPerRun: DefaultMaxSteersPerRun}
	out, err := MarshalConfig(def)
	if err != nil || strings.Contains(string(out), "max_steers_per_run") {
		t.Errorf("default written: %v\n%s", err, out)
	}
}

package spec

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Sandbox backend names. Each backend has one config file,
// sandboxes/<backend>.yaml, and an optional env file env.d/sandboxes/<backend>.env.
const (
	SandboxLocal      = "local"
	SandboxDocker     = "docker"
	SandboxDepot      = "depot"
	SandboxKubernetes = "kubernetes"
	SandboxSSH        = "ssh"
)

// SandboxBackends lists the backends in display order.
var SandboxBackends = []string{SandboxLocal, SandboxDocker, SandboxDepot, SandboxKubernetes, SandboxSSH}

// ValidSandboxBackend reports whether name is a backend.
func ValidSandboxBackend(name string) bool { return slices.Contains(SandboxBackends, name) }

// Sandbox lifecycles.
const (
	LifecyclePerRun     = "per_run"
	LifecyclePersistent = "persistent"
)

// Sandbox approval modes (the `approval` field).
const (
	ApprovalAuto   = "auto"
	ApprovalAlways = "always"
	ApprovalRules  = "rules"
)

// Actions of an approval rule.
const (
	RuleAuto    = "auto"
	RuleApprove = "approve"
	RuleDeny    = "deny"
)

// SandboxResources are compute limits. Zero means the backend default.
type SandboxResources struct {
	CPUs     float64 `yaml:"cpus,omitempty" json:"cpus"`
	MemoryMB int     `yaml:"memory_mb,omitempty" json:"memory_mb"`
	DiskGB   int     `yaml:"disk_gb,omitempty" json:"disk_gb"`
}

// SandboxShellRule decides a shell command: Match is a regular expression
// searched in the whole command text.
type SandboxShellRule struct {
	Match  string `yaml:"match" json:"match"`
	Action string `yaml:"action" json:"action"`
}

// SandboxPathRule decides an fs_write path: Path is a glob relative to the
// workdir (`*` within a path segment, `**` across segments).
type SandboxPathRule struct {
	Path   string `yaml:"path" json:"path"`
	Action string `yaml:"action" json:"action"`
}

// SandboxRules are the rules of `approval: rules`. The first matching rule
// wins; no match means approval is required.
type SandboxRules struct {
	Shell   []SandboxShellRule `yaml:"shell,omitempty" json:"shell"`
	FSWrite []SandboxPathRule  `yaml:"fs_write,omitempty" json:"fs_write"`
}

// Sandbox is the config of one sandbox backend (sandboxes/<backend>.yaml).
// The common fields apply to every backend; the others only to the backends
// named in their comment and are ignored elsewhere. Credentials (the Depot
// token, SSH password or private key) are not part of the file: they live in
// the backend's env file. The local backend is always enabled: its `enabled`
// field is ignored (a false value only draws a warning).
type Sandbox struct {
	Enabled   bool             `yaml:"enabled" json:"enabled"`
	Lifecycle string           `yaml:"lifecycle" json:"lifecycle"`
	Approval  string           `yaml:"approval" json:"approval"`
	Rules     SandboxRules     `yaml:"approval_rules,omitempty" json:"approval_rules"`
	Timeout   Duration         `yaml:"timeout" json:"timeout"`               // default command timeout
	Resources SandboxResources `yaml:"resources,omitempty" json:"resources"` // docker, depot, kubernetes

	Root string `yaml:"root,omitempty" json:"root"` // local, ssh

	Socket  string   `yaml:"socket,omitempty" json:"socket"` // docker; default DOCKER_HOST or the standard socket
	Images  []string `yaml:"images,omitempty" json:"images"` // docker, kubernetes; the first is the default
	Network bool     `yaml:"network" json:"network"`         // docker; false disables container networking

	OrgID             string            `yaml:"org_id,omitempty" json:"org_id"`                           // depot
	ImageRef          string            `yaml:"image_ref,omitempty" json:"image_ref"`                     // depot
	Architecture      string            `yaml:"architecture,omitempty" json:"architecture"`               // depot: amd64 | arm64
	TimeoutMinutes    int               `yaml:"timeout_minutes,omitempty" json:"timeout_minutes"`         // depot
	DisableTailnet    bool              `yaml:"disable_tailnet,omitempty" json:"disable_tailnet"`         // depot
	DisableDepotCache bool              `yaml:"disable_depot_cache,omitempty" json:"disable_depot_cache"` // depot
	CISecrets         map[string]string `yaml:"secrets,omitempty" json:"secrets"`                         // depot: env name -> Depot CI secret name
	Endpoint          string            `yaml:"endpoint,omitempty" json:"endpoint"`                       // depot; default https://api.depot.dev

	Kubeconfig     string `yaml:"kubeconfig,omitempty" json:"kubeconfig"`           // kubernetes
	Context        string `yaml:"context,omitempty" json:"context"`                 // kubernetes
	Namespace      string `yaml:"namespace,omitempty" json:"namespace"`             // kubernetes
	ServiceAccount string `yaml:"service_account,omitempty" json:"service_account"` // kubernetes

	Host    string `yaml:"host,omitempty" json:"host"`         // ssh
	Port    int    `yaml:"port,omitempty" json:"port"`         // ssh
	User    string `yaml:"user,omitempty" json:"user"`         // ssh
	KeyFile string `yaml:"key_file,omitempty" json:"key_file"` // ssh; alternative to SSH_PRIVATE_KEY / SSH_PASSWORD in the env file
	HostKey string `yaml:"host_key,omitempty" json:"host_key"` // ssh: pinned SHA256 fingerprint
}

// DefaultSandbox returns the config a backend has before its file exists.
func DefaultSandbox(backend string) Sandbox {
	s := Sandbox{
		Lifecycle: LifecyclePerRun, Approval: ApprovalRules, Timeout: Duration(10 * time.Minute),
		Network: true,
		Rules: SandboxRules{
			Shell:   []SandboxShellRule{},
			FSWrite: []SandboxPathRule{},
		},
	}
	switch backend {
	case SandboxLocal:
		// Always on and usable with no file: writes inside the workdir run without asking
		// (paths that leave it are denied); shell commands ask unless a rule matches.
		s.Enabled = true
		s.Rules.FSWrite = []SandboxPathRule{{Path: "**", Action: RuleAuto}}
	case SandboxDocker, SandboxKubernetes:
		s.Images = []string{"ubuntu:24.04"}
	case SandboxSSH:
		s.Port = 22
		s.Root = "hal9001-sandbox"
	}
	if backend == SandboxKubernetes {
		s.Namespace = "default"
	}
	s.normalize()
	return s
}

// normalize replaces nil collections so the JSON form always has arrays and objects.
func (s *Sandbox) normalize() {
	if s.Rules.Shell == nil {
		s.Rules.Shell = []SandboxShellRule{}
	}
	if s.Rules.FSWrite == nil {
		s.Rules.FSWrite = []SandboxPathRule{}
	}
	if s.Images == nil {
		s.Images = []string{}
	}
	if s.CISecrets == nil {
		s.CISecrets = map[string]string{}
	}
}

// ParseSandbox parses sandboxes/<backend>.yaml over DefaultSandbox. An empty
// file is the defaults.
func ParseSandbox(backend string, b []byte) (*Sandbox, []Diagnostic) {
	s := DefaultSandbox(backend)
	ds, _, fatal := decodeYAML(b, &s)
	if fatal {
		return nil, ds
	}
	if backend == SandboxLocal && !s.Enabled {
		// The local sandbox has no off switch; a file saying so is still accepted.
		s.Enabled = true
		ds = append(ds, Diagnostic{Path: "enabled", Severity: SeverityWarning, Code: CodeInvalidValue,
			Message: "enabled is ignored for the local sandbox: it is always on"})
	}
	s.normalize()
	return &s, ds
}

// CheckSandbox parses and validates the file of a backend.
func CheckSandbox(backend string, b []byte) (*Sandbox, []Diagnostic) {
	file := KindSandbox.Rel(backend)
	if !ValidSandboxBackend(backend) {
		return nil, []Diagnostic{{File: file, Severity: SeverityError, Code: CodeInvalidValue,
			Message: fmt.Sprintf("unknown sandbox backend %q (use %s)", backend, strings.Join(SandboxBackends, ", "))}}
	}
	s, ds := ParseSandbox(backend, b)
	if s != nil {
		ds = append(ds, ValidateSandbox(backend, s)...)
	}
	return s, withFile(file, ds)
}

// Credential variable of the depot backend; it lives in the sandbox env file, never in depot.yaml.
const envDepotToken = "DEPOT_TOKEN"

// ValidateSandboxCredentials checks the credentials s needs, which live in the
// home's env files (the global env overlaid by the backend's own): an enabled
// depot sandbox requires DEPOT_TOKEN.
func ValidateSandboxCredentials(home, backend string, s *Sandbox) []Diagnostic {
	if backend != SandboxDepot || !s.Enabled {
		return nil
	}
	for _, f := range []struct {
		kind Kind
		id   string
	}{{KindSandboxEnv, backend}, {KindEnv, ""}} {
		raw, _, err := ReadFile(home, f.kind, f.id)
		if err != nil {
			continue
		}
		if env, _ := ParseEnv(raw); strings.TrimSpace(env.Vars[envDepotToken]) != "" {
			return nil
		}
	}
	return []Diagnostic{{Path: envDepotToken, Severity: SeverityError, Code: CodeRequired,
		Message: "Depot token is required: set " + envDepotToken + " in the sandbox environment"}}
}

// CheckSandboxIn is CheckSandbox plus the credential checks of
// ValidateSandboxCredentials against the env files of home.
func CheckSandboxIn(home, backend string, b []byte) (*Sandbox, []Diagnostic) {
	s, ds := CheckSandbox(backend, b)
	if s != nil {
		ds = append(ds, withFile(KindSandbox.Rel(backend), ValidateSandboxCredentials(home, backend, s))...)
	}
	return s, ds
}

// MarshalSandbox renders s as YAML.
func MarshalSandbox(s Sandbox) ([]byte, error) {
	s.normalize()
	return encodeYAML(s)
}

var (
	depotArchitectures = []string{"", "amd64", "arm64"}
	fingerprintRE      = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}=?$`)
)

// ValidateSandbox checks s. Backend fields are only required when the backend
// is enabled, so a half-filled form can be saved while it is disabled.
func ValidateSandbox(backend string, s *Sandbox) []Diagnostic {
	c := &collector{}
	switch s.Lifecycle {
	case LifecyclePerRun, LifecyclePersistent:
	default:
		c.errorf(CodeInvalidEnum, "lifecycle", "lifecycle must be %s or %s", LifecyclePerRun, LifecyclePersistent)
	}
	switch s.Approval {
	case ApprovalAuto, ApprovalAlways, ApprovalRules:
	default:
		c.errorf(CodeInvalidEnum, "approval", "approval must be %s, %s or %s", ApprovalAuto, ApprovalAlways, ApprovalRules)
	}
	if s.Timeout.Std() <= 0 {
		c.errorf(CodeInvalidValue, "timeout", "timeout must be a positive duration")
	}
	for i, r := range s.Rules.Shell {
		p := fmt.Sprintf("approval_rules.shell[%d]", i)
		if _, err := regexp.Compile(r.Match); err != nil || r.Match == "" {
			c.errorf(CodeInvalidRegex, p+".match", "match must be a non-empty valid regular expression")
		}
		checkRuleAction(c, p+".action", r.Action)
	}
	for i, r := range s.Rules.FSWrite {
		p := fmt.Sprintf("approval_rules.fs_write[%d]", i)
		if strings.TrimSpace(r.Path) == "" {
			c.errorf(CodeRequired, p+".path", "path is required")
		}
		checkRuleAction(c, p+".action", r.Action)
	}
	if s.Resources.CPUs < 0 || s.Resources.MemoryMB < 0 || s.Resources.DiskGB < 0 {
		c.errorf(CodeInvalidValue, "resources", "resources must not be negative")
	}
	if !s.Enabled {
		return c.ds
	}
	switch backend {
	case SandboxDocker:
		if len(s.Images) == 0 {
			c.errorf(CodeRequired, "images", "list at least one image")
		}
	case SandboxKubernetes:
		if len(s.Images) == 0 {
			c.errorf(CodeRequired, "images", "list at least one image")
		}
		if s.Namespace == "" {
			c.errorf(CodeRequired, "namespace", "namespace is required")
		}
	case SandboxDepot:
		if !slices.Contains(depotArchitectures, s.Architecture) {
			c.errorf(CodeInvalidEnum, "architecture", "architecture must be amd64 or arm64")
		}
		if s.TimeoutMinutes < 0 || s.TimeoutMinutes > 24*60 {
			c.errorf(CodeInvalidValue, "timeout_minutes", "timeout_minutes must be between 1 and 1440")
		}
		if v := int(s.Resources.CPUs); s.Resources.CPUs != 0 && (float64(v) != s.Resources.CPUs || v < 2 || v > 64) {
			c.errorf(CodeInvalidValue, "resources.cpus", "Depot sandboxes support a whole number of 2 to 64 vCPUs")
		}
	case SandboxSSH:
		if s.Host == "" {
			c.errorf(CodeRequired, "host", "host is required")
		}
		if s.User == "" {
			c.errorf(CodeRequired, "user", "user is required")
		}
		if s.Root == "" {
			c.errorf(CodeRequired, "root", "root is required")
		}
		if s.Port < 1 || s.Port > 65535 {
			c.errorf(CodeInvalidValue, "port", "port must be between 1 and 65535")
		}
		if s.HostKey != "" && !fingerprintRE.MatchString(s.HostKey) {
			c.errorf(CodeInvalidValue, "host_key", "host_key must be a SHA256:<base64> fingerprint as printed by ssh-keygen -l")
		}
	}
	return c.ds
}

func checkRuleAction(c *collector, path, action string) {
	switch action {
	case RuleAuto, RuleApprove, RuleDeny:
	default:
		c.errorf(CodeInvalidEnum, path, "action must be %s, %s or %s", RuleAuto, RuleApprove, RuleDeny)
	}
}

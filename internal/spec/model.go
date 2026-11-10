package spec

import (
	"time"

	"hal9001/internal/transform"
)

// Enum values. The wire format is the plain string, so fields below use
// string rather than a named type; these constants are the only legal values.
const (
	NodeCommand  = "command"
	NodeAgent    = "agent"
	NodeInbox    = "inbox"
	NodeApproval = "approval"

	NodeTransform = "transform"

	LangBash   = "bash"
	LangPython = "python"
	LangNode   = "node"
	LangJQ     = "jq"
	LangExec   = "exec"

	ModeAuto     = "auto"
	ModeApprove  = "approve"
	ModeReadonly = "readonly"

	ApproveProposal = "proposal"
	ApprovePerTool  = "per_tool"

	CwdDir      = "dir"
	CwdWorktree = "worktree"

	FirstRunBaseline = "baseline"
	FirstRunFire     = "fire"

	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"

	OutputNote = "note"
	OutputNone = "none"

	MCPStdio = "stdio"
	MCPHTTP  = "http"
	MCPSSE   = "sse"

	// Ports. Every node has PortError implicitly; agent nodes in approve mode
	// also have PortDenied. PortOut is the default declared port.
	PortOut    = "out"
	PortError  = "error"
	PortDenied = "denied"
	// PortApproved is the port an approval node emits on when the request was approved.
	PortApproved = "approved"

	ServiceUI      = "ui"
	ServiceAPI     = "api"
	ServiceApprove = "approve"
	ServiceAdmin   = "admin"

	ControlRequired   = "required"
	ControlRequisite  = "requisite"
	ControlSufficient = "sufficient"
	ControlOptional   = "optional"

	// DefaultStack is the stack name used for every service when config.yaml
	// does not say otherwise.
	DefaultStack = "default"
)

// Languages lists the legal values of lang.
var Languages = []string{LangBash, LangPython, LangNode, LangJQ, LangExec}

// On-timeout outcomes of an approval node (on_timeout).
const (
	OnTimeoutDenied   = "denied"
	OnTimeoutApproved = "approved"
	OnTimeoutError    = "error"
)

// Values of Config.NotifyVia.
const (
	NotifyViaAuto     = "auto"
	NotifyViaApp      = "app"
	NotifyViaTerminal = "terminal-notifier"
	NotifyViaNone     = "none"
)

// NotifyVias lists the values of Config.NotifyVia.
var NotifyVias = []string{NotifyViaAuto, NotifyViaApp, NotifyViaTerminal, NotifyViaNone}

// Priorities lists inbox priorities from lowest to highest.
var Priorities = []string{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent}

// Services lists the auth services.
var Services = []string{ServiceUI, ServiceAPI, ServiceApprove, ServiceAdmin}

// KnownAuthModules are the compiled-in auth module names (REQUIREMENTS §10.4).
// Add a name here when the auth package registers a new module.
var KnownAuthModules = []string{"localhost-trust", "tailscale-identity", "webauthn", "password"}

// Defaults (REQUIREMENTS §4, §6, §7.2, §8, §9, §10).
const (
	DefaultMaxAgentRuns       = 2
	DefaultMaxCommandRuns     = 8
	DefaultRetentionDays      = 30
	DefaultDedupRetentionDays = 180
	DefaultNotifyMinPriority  = PriorityNormal
	DefaultNotifyVia          = NotifyViaAuto
	DefaultSessionTTL         = Duration(7 * 24 * time.Hour)
	DefaultMaxConcurrent      = 2
	DefaultCommandTimeout     = Duration(time.Minute)
	DefaultAgentTimeout       = Duration(30 * time.Minute)
	DefaultApprovalTimeout    = Duration(time.Hour)
	// MaxSystemPromptBytes caps Config.SystemPrompt.
	MaxSystemPromptBytes = 16 << 10
)

// Config is config.yaml. Zero numeric values mean "unset" and get the default.
// There is no port key: the bind address is fixed at 127.0.0.1:9001.
type Config struct {
	MaxAgentRuns       int    `yaml:"max_agent_runs,omitempty" json:"max_agent_runs"`
	MaxCommandRuns     int    `yaml:"max_command_runs,omitempty" json:"max_command_runs"`
	RetentionDays      int    `yaml:"retention_days,omitempty" json:"retention_days"`
	DedupRetentionDays int    `yaml:"dedup_retention_days,omitempty" json:"dedup_retention_days"`
	NotifyMinPriority  string `yaml:"notify_min_priority,omitempty" json:"notify_min_priority"`
	// NotifyVia routes notifications: auto (the macOS app when one is connected, else terminal-notifier), app,
	// terminal-notifier, or none (log only).
	NotifyVia string `yaml:"notify_via,omitempty" json:"notify_via"`
	// ApprovalTimeout is the default for agent nodes that do not set their own
	// approval_timeout (see Config.ApprovalTimeoutFor).
	ApprovalTimeout Duration `yaml:"approval_timeout,omitempty" json:"approval_timeout"`
	// SystemPrompt is user-written markdown (name, preferences, facts) added to every agent call; see tools.Preamble.
	SystemPrompt  string             `yaml:"system_prompt,omitempty" json:"system_prompt"`
	SessionTTL    Duration           `yaml:"session_ttl,omitempty" json:"session_ttl"`
	AllowedHosts  []string           `yaml:"allowed_hosts,omitempty" json:"allowed_hosts"`
	AllowedLogins []string           `yaml:"allowed_logins,omitempty" json:"allowed_logins"`
	Runtimes      map[string]Runtime `yaml:"runtimes,omitempty" json:"runtimes"`
	Auth          Auth               `yaml:"auth,omitempty" json:"auth"`
}

// DataPlaceholder in a runtime's command, env values and spawn args/env is
// replaced by the engine with <home>/data (the directory for runtime state such
// as the isolated agent homes). Write it quoted in YAML: "{{data}}/runtimes/x".
const DataPlaceholder = "{{data}}"

// Runtime is one configured ACP agent command (REQUIREMENTS §7.3). The fields
// beyond Command and Modes are what the ACP gate (docs/acp-gate.md) found
// necessary to run a runtime restrictively.
type Runtime struct {
	// Kind names the runtime family (claude, codex, omp) so hal can lock it down to hal's MCP tools
	// (docs/acp-lockdown.md). Empty: detected from the command; an unknown runtime runs without lockdown.
	Kind    string            `yaml:"kind,omitempty" json:"kind,omitempty"`
	Command []string          `yaml:"command" json:"command"`
	Modes   RuntimeModes      `yaml:"modes,omitempty" json:"modes"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// Ask applies to readonly and approve nodes, Full to auto nodes (and to the
	// approved phase of a proposal): extra argv and env for the agent process,
	// for runtimes that pick their approval behaviour with a flag.
	Ask  RuntimeSpawn `yaml:"ask,omitempty" json:"ask"`
	Full RuntimeSpawn `yaml:"full,omitempty" json:"full"`
	// SessionMeta is sent as _meta on session/new and session/load, for
	// runtime-specific session options such as claude's settingSources.
	SessionMeta map[string]any `yaml:"session_meta,omitempty" json:"session_meta,omitempty"`
	// Elicitation advertises form elicitation to the agent, which runtimes
	// like omp need to route edit and MCP approvals to the harness.
	Elicitation bool `yaml:"elicitation,omitempty" json:"elicitation,omitempty"`
}

// RuntimeSpawn is extra argv and environment for one policy of a runtime.
type RuntimeSpawn struct {
	Args []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env  map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// RuntimeModes names the agent's ACP session modes for the restrictive
// ("ask") and permissive ("full") policies. An empty name means the runtime is
// restricted some other way (see Runtime.Ask and Runtime.Full).
type RuntimeModes struct {
	Ask  string `yaml:"ask,omitempty" json:"ask"`
	Full string `yaml:"full,omitempty" json:"full"`
}

// Auth is the PAM-style auth configuration (REQUIREMENTS §10.4).
type Auth struct {
	// Stacks maps stack name to its ordered module entries.
	Stacks map[string][]AuthModule `yaml:"stacks,omitempty" json:"stacks"`
	// Services maps each service (ui, api, approve, admin) to a stack name.
	Services map[string]string `yaml:"services,omitempty" json:"services"`
}

// AuthModule is one entry of an auth stack.
type AuthModule struct {
	Module  string         `yaml:"module" json:"module"`
	Control string         `yaml:"control" json:"control"`
	Options map[string]any `yaml:"options,omitempty" json:"options,omitempty"`
}

// Sensor is sensors/<id>.yaml (REQUIREMENTS §5). At most one of Interval and
// Cron is set; with neither the sensor is manual-only (never scheduled).
type Sensor struct {
	// ID is the file name stem; it is never written to the file. LegacyID
	// receives an old-style id: field on read and is dropped by ParseSensor.
	ID          string   `yaml:"-" json:"id,omitempty"`
	LegacyID    string   `yaml:"id,omitempty" json:"-"`
	Description string   `yaml:"description,omitempty" json:"description"`
	Interval    Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	Cron        string   `yaml:"cron,omitempty" json:"cron,omitempty"`
	Lang        string   `yaml:"lang" json:"lang"`
	Script      string   `yaml:"script,omitempty" json:"script,omitempty"`
	Command     []string `yaml:"command,omitempty" json:"command,omitempty"`
	Cwd         string   `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	// Key is a text/template over one item producing its dedup key.
	Key      string   `yaml:"key" json:"key"`
	FirstRun string   `yaml:"first_run,omitempty" json:"first_run"`
	Timeout  Duration `yaml:"timeout,omitempty" json:"timeout"`
}

// Flow is flows/<id>.yaml (REQUIREMENTS §6).
type Flow struct {
	// ID is the file name stem; see Sensor.ID.
	ID            string   `yaml:"-" json:"id,omitempty"`
	LegacyID      string   `yaml:"id,omitempty" json:"-"`
	Description   string   `yaml:"description,omitempty" json:"description"`
	Enabled       bool     `yaml:"enabled" json:"enabled"`
	DryRun        bool     `yaml:"dry_run" json:"dry_run"`
	Subscribe     []string `yaml:"subscribe" json:"subscribe"`
	MaxConcurrent int      `yaml:"max_concurrent,omitempty" json:"max_concurrent"`
	Nodes         []Node   `yaml:"nodes" json:"nodes"`
	Edges         []Edge   `yaml:"edges" json:"edges"`
}

// Node is one flow node. Type selects which of the type-specific fields apply;
// validation rejects fields that do not belong to the node's type.
//
//	command: lang, script|command, ports, cwd (dir only), timeout, side_effect
//	agent:   runtime, mode, approve, prompt|instructions, output, extra_prompt, model, cwd, timeout,
//	         approval_timeout, allow, mcp_servers, sandbox, image, repo, ref, tools
//	inbox:   title, priority, summary_md, suggested_reply, links
//	transform: steps, timeout
//	approval: title, body, fields, priority, timeout, on_timeout
type Node struct {
	ID   string `yaml:"id" json:"id"`
	Type string `yaml:"type" json:"type"`

	// transform
	Steps []transform.Step `yaml:"steps,omitempty" json:"steps,omitempty"`

	// command
	Lang    string   `yaml:"lang,omitempty" json:"lang,omitempty"`
	Ports   []string `yaml:"ports,omitempty" json:"ports,omitempty"`
	Script  string   `yaml:"script,omitempty" json:"script,omitempty"`
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`

	// agent
	Runtime     string `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Mode        string `yaml:"mode,omitempty" json:"mode,omitempty"`
	Approve     string `yaml:"approve,omitempty" json:"approve,omitempty"`
	Prompt      string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	ExtraPrompt string `yaml:"extra_prompt,omitempty" json:"extra_prompt,omitempty"`
	// Instructions is an inline prompt template (alternative to Prompt);
	// Output (note|none, default note) applies only to inline instructions.
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	Output       string `yaml:"output,omitempty" json:"output,omitempty"`
	Model        string `yaml:"model,omitempty" json:"model,omitempty"`

	// inbox
	Title          string `yaml:"title,omitempty" json:"title,omitempty"`
	Priority       string `yaml:"priority,omitempty" json:"priority,omitempty"`
	SummaryMD      string `yaml:"summary_md,omitempty" json:"summary_md,omitempty"`
	SuggestedReply string `yaml:"suggested_reply,omitempty" json:"suggested_reply,omitempty"`
	Links          []Link `yaml:"links,omitempty" json:"links,omitempty"`

	// approval (title and priority are shared with inbox)
	Body   string   `yaml:"body,omitempty" json:"body,omitempty"`
	Fields []string `yaml:"fields,omitempty" json:"fields,omitempty"`
	// Editable lists dot-paths into the input the approver may edit before approving.
	Editable  []string `yaml:"editable,omitempty" json:"editable,omitempty"`
	OnTimeout string   `yaml:"on_timeout,omitempty" json:"on_timeout,omitempty"`

	// command and agent
	Cwd             *Cwd     `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	Timeout         Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	ApprovalTimeout Duration `yaml:"approval_timeout,omitempty" json:"approval_timeout,omitempty"`
	SideEffect      bool     `yaml:"side_effect,omitempty" json:"side_effect,omitempty"`

	Allow      *Allow      `yaml:"allow,omitempty" json:"allow,omitempty"`
	MCPServers []MCPServer `yaml:"mcp_servers,omitempty" json:"mcp_servers,omitempty"`

	// agent, hal-provided tools (docs/design/agent-access.md). Sandbox names a sandbox backend (adds fs and shell
	// tools executed there); Image, Repo and Ref are sandbox options; Tools are custom tool ids. The engine passes them
	// to internal/tools; validation against the configured sandboxes and tools happens where those are defined.
	Sandbox string    `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	Image   string    `yaml:"image,omitempty" json:"image,omitempty"`
	Repo    string    `yaml:"repo,omitempty" json:"repo,omitempty"`
	Ref     string    `yaml:"ref,omitempty" json:"ref,omitempty"`
	Tools   []ToolRef `yaml:"tools,omitempty" json:"tools,omitempty"`

	UI UI `yaml:"ui,omitempty" json:"ui"`
}

// Cwd is a working-directory selection (REQUIREMENTS §7.5). In YAML a plain
// string is shorthand for {mode: dir, path: <string>}. Path, Repo and Ref are
// text/templates.
type Cwd struct {
	Mode string `yaml:"mode" json:"mode"`
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	Repo string `yaml:"repo,omitempty" json:"repo,omitempty"`
	Ref  string `yaml:"ref,omitempty" json:"ref,omitempty"`
}

// Allow names tools beyond hal's own that an agent node may call: each entry is a
// regular expression over the tool title or name (tools of the node's mcp_servers).
// Agents have no shell or file tools, so nothing else is matched.
type Allow struct {
	Tools []string `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// Link is an inbox link; both fields are templates on inbox nodes.
type Link struct {
	Label string `yaml:"label" json:"label"`
	URL   string `yaml:"url" json:"url"`
}

// MCPServer is an MCP server passed through to the agent session.
type MCPServer struct {
	Name    string            `yaml:"name" json:"name"`
	Type    string            `yaml:"type,omitempty" json:"type"`
	Command string            `yaml:"command,omitempty" json:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	URL     string            `yaml:"url,omitempty" json:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

// UI is the canvas position of a node.
type UI struct {
	X float64 `yaml:"x" json:"x"`
	Y float64 `yaml:"y" json:"y"`
}

// Edge connects an output port of one node to another node. An empty Port
// means PortOut.
type Edge struct {
	From string `yaml:"from" json:"from"`
	Port string `yaml:"port,omitempty" json:"port,omitempty"`
	To   string `yaml:"to" json:"to"`
}

// Prompt is prompts/<name>.md: YAML frontmatter plus a template body
// (REQUIREMENTS §7.1).
type Prompt struct {
	// Name is the file name stem; a frontmatter name: is ignored and never written.
	Name        string `yaml:"name,omitempty" json:"name"`
	Description string `yaml:"description,omitempty" json:"description"`
	// Output is OutputNote (structured inbox schema, §7.4) or OutputNone.
	Output string `yaml:"output,omitempty" json:"output"`
	Body   string `yaml:"-" json:"body"`
}

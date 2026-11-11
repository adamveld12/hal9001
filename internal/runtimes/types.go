package runtimes

import "time"

// Detection statuses.
const (
	StatusReady       = "ready"
	StatusWarning     = "warning"
	StatusUnavailable = "unavailable"
)

// Login states.
const (
	AuthOK      = "ok"
	AuthMissing = "missing"
	AuthUnknown = "unknown"
)

// Warning codes.
const (
	CodeCLIMissing           = "cli_missing"
	CodeAuthMissing          = "auth_missing"
	CodeAuthUnknown          = "auth_unknown"
	CodeNodeMissing          = "node_missing"
	CodeNodeOld              = "node_old"
	CodeAdapterMissing       = "adapter_missing"
	CodeProvidersMissing     = "providers_missing"
	CodeAdapterInstallFailed = "adapter_install_failed"
)

// Detection is what hal9001 found out about one runtime; it is the `detection` object of GET /api/runtimes.
type Detection struct {
	// Status is StatusReady (a command and a login), StatusWarning (a command, but the login is missing or unknown,
	// or another warning applies) or StatusUnavailable (no command can be started).
	Status  string  `json:"status"`
	CLI     CLI     `json:"cli"`
	Auth    Auth    `json:"auth"`
	Adapter Adapter `json:"adapter"`
	Node    Node    `json:"node"`
	// Command is the argv hal9001 starts for the runtime, nil when it is unavailable.
	Command   []string  `json:"command"`
	Warnings  []Warning `json:"warnings"`
	CheckedAt time.Time `json:"checked_at"`
}

// CLI is the agent's own command line tool.
type CLI struct {
	Name    string  `json:"name"`
	Found   bool    `json:"found"`
	Path    *string `json:"path"`
	Version *string `json:"version"`
}

// Auth is the login state of the agent.
type Auth struct {
	Status string `json:"status"`
	// Command is what the user runs to sign in.
	Command *string `json:"command"`
}

// Adapter is what speaks ACP for the runtime: the CLI itself (builtin), an adapter hal9001 installed (managed), the
// user's command (custom), or nothing yet (none).
type Adapter struct {
	Source        string  `json:"source"`
	Installed     bool    `json:"installed"`
	Version       *string `json:"version"`
	PinnedVersion *string `json:"pinned_version"`
	Path          *string `json:"path"`
	// Installable: hal9001 can install an adapter for this runtime (a claude or codex runtime without a command).
	Installable bool `json:"installable"`
	Installing  bool `json:"installing"`
}

// Node is the Node.js the adapters run on.
type Node struct {
	Found   bool    `json:"found"`
	Version *string `json:"version"`
	// OK: the version is MinNodeMajor or newer.
	OK bool `json:"ok"`
}

// Warning is one thing to fix, with the command or page that fixes it.
type Warning struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Command *string `json:"command"`
	URL     *string `json:"url"`
}

// Event reports the progress of an adapter install.
type Event struct {
	// Name is the runtime the install was started for.
	Name string
	// Phase is PhaseInstalling, PhaseDone or PhaseFailed.
	Phase string
	// Line is a line of npm's output (PhaseInstalling).
	Line string
	// Error is why the install failed (PhaseFailed).
	Error string
}

// Install phases.
const (
	PhaseInstalling = "installing"
	PhaseDone       = "done"
	PhaseFailed     = "failed"
)

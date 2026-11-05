package runner

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const fallbackPATH = "/usr/bin:/bin:/usr/sbin:/sbin"

// baseEnvKeys are the daemon environment variables passed to every process. The
// daemon's full environment is deliberately not inherited.
var baseEnvKeys = []string{
	"HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TZ",
	"SSH_AUTH_SOCK", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
}

// DefaultBaseEnv returns the minimal environment every node starts from. path, when
// non-empty, becomes PATH (typically the login-shell PATH); otherwise the daemon's
// PATH is used, falling back to the system default.
func DefaultBaseEnv(path string) map[string]string {
	env := make(map[string]string, len(baseEnvKeys)+1)
	for _, k := range baseEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	switch {
	case path != "":
		env["PATH"] = path
	case os.Getenv("PATH") != "":
		env["PATH"] = os.Getenv("PATH")
	default:
		env["PATH"] = fallbackPATH
	}
	return env
}

// EnvOptions are the inputs to BuildEnv.
type EnvOptions struct {
	// Path overrides PATH in the base environment (see DefaultBaseEnv).
	Path string
	// Base replaces DefaultBaseEnv(Path) when non-nil.
	Base map[string]string
	// EnvFile holds the user's env-file values; they override Base.
	EnvFile map[string]string

	// Event is the original event JSON, written to a temp file exposed as
	// HAL_EVENT_FILE. nil means no event file (e.g. a sensor poll).
	Event []byte
	// EventDir is where the event temp file is created; empty means os.TempDir().
	EventDir string

	RunID, EventID, Flow, Node string
	// StateDir is created if missing and exposed as HAL_STATE_DIR.
	StateDir string
	DryRun   bool
	// Sensor marks a sensor poll: HAL_LAST_SUCCESS_AT is set when LastSuccessAt is non-zero.
	Sensor        bool
	LastSuccessAt time.Time
}

// BuildEnv assembles the process environment: base < env file < HAL_* variables
// (HAL_* always win, so an env file cannot spoof them). Empty ids are omitted, as are
// HAL_DRY_RUN outside dry-run and HAL_LAST_SUCCESS_AT before the first successful poll.
// The returned cleanup removes the event temp file and is always non-nil.
func BuildEnv(o EnvOptions) (env map[string]string, cleanup func(), err error) {
	cleanup = func() {}
	base := o.Base
	if base == nil {
		base = DefaultBaseEnv(o.Path)
	}
	env = make(map[string]string, len(base)+len(o.EnvFile)+8)
	for k, v := range base {
		env[k] = v
	}
	for k, v := range o.EnvFile {
		env[k] = v
	}
	// Drop inherited or env-file HAL_* so only values computed here are visible.
	for k := range env {
		if strings.HasPrefix(k, "HAL_") {
			delete(env, k)
		}
	}

	set := func(k, v string) {
		if v != "" {
			env[k] = v
		}
	}
	set("HAL_RUN_ID", o.RunID)
	set("HAL_EVENT_ID", o.EventID)
	set("HAL_FLOW", o.Flow)
	set("HAL_NODE", o.Node)
	if o.DryRun {
		env["HAL_DRY_RUN"] = "1"
	}
	if o.Sensor && !o.LastSuccessAt.IsZero() {
		env["HAL_LAST_SUCCESS_AT"] = o.LastSuccessAt.UTC().Format(time.RFC3339)
	}
	if o.StateDir != "" {
		if err := os.MkdirAll(o.StateDir, 0o700); err != nil {
			return nil, cleanup, fmt.Errorf("runner: create state dir: %w", err)
		}
		env["HAL_STATE_DIR"] = o.StateDir
	}
	if o.Event != nil {
		f, err := os.CreateTemp(o.EventDir, "hal-event-*.json")
		if err != nil {
			return nil, cleanup, fmt.Errorf("runner: create event file: %w", err)
		}
		name := f.Name()
		cleanup = func() { _ = os.Remove(name) }
		if _, err := f.Write(o.Event); err != nil {
			_ = f.Close()
			cleanup()
			return nil, func() {}, fmt.Errorf("runner: write event file: %w", err)
		}
		if err := f.Close(); err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("runner: close event file: %w", err)
		}
		env["HAL_EVENT_FILE"] = name
	}
	return env, cleanup, nil
}

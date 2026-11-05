package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envOf runs `env` under the given environment and returns what the process saw.
func envOf(t *testing.T, env map[string]string, extra Spec) map[string]string {
	t.Helper()
	skipIfMissing(t, "bash")
	extra.Lang, extra.Script = LangBash, "env"
	extra.Env, extra.BaseEnv = env, map[string]string{"PATH": os.Getenv("PATH")}
	res := mustRun(t, extra)
	got := map[string]string{}
	for line := range strings.SplitSeq(string(res.Stdout), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	return got
}

func TestBuildEnvInjectsHALVariables(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state", "node-a")
	evDir := t.TempDir()
	env, cleanup, err := BuildEnv(EnvOptions{
		Path:     "/opt/x/bin:/usr/bin",
		EnvFile:  map[string]string{"SVC_TWO": "value-two-abcd"},
		Event:    []byte(`{"id":"e1"}`),
		EventDir: evDir,
		RunID:    "run1", EventID: "ev1", Flow: "triage", Node: "classify",
		StateDir: state, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["PATH"] != "/opt/x/bin:/usr/bin" {
		t.Errorf("PATH = %q", env["PATH"])
	}
	for k, want := range map[string]string{
		"SVC_TWO": "value-two-abcd", "HAL_RUN_ID": "run1", "HAL_EVENT_ID": "ev1",
		"HAL_FLOW": "triage", "HAL_NODE": "classify", "HAL_STATE_DIR": state, "HAL_DRY_RUN": "1",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if st, err := os.Stat(state); err != nil || !st.IsDir() {
		t.Errorf("state dir not created: %v", err)
	}
	data, err := os.ReadFile(env["HAL_EVENT_FILE"])
	if err != nil || string(data) != `{"id":"e1"}` {
		t.Fatalf("event file = %q, %v", data, err)
	}
	if filepath.Dir(env["HAL_EVENT_FILE"]) != evDir {
		t.Errorf("event file %s not in %s", env["HAL_EVENT_FILE"], evDir)
	}
	if _, ok := env["HAL_LAST_SUCCESS_AT"]; ok {
		t.Error("HAL_LAST_SUCCESS_AT is sensors-only")
	}
	cleanup()
	if _, err := os.Stat(env["HAL_EVENT_FILE"]); !os.IsNotExist(err) {
		t.Errorf("cleanup left the event file behind: %v", err)
	}
}

func TestBuildEnvVisibleToProcess(t *testing.T) {
	state := filepath.Join(t.TempDir(), "st")
	env, cleanup, err := BuildEnv(EnvOptions{
		Event: []byte(`{"id":"e1"}`), RunID: "r9", EventID: "e1", Flow: "f", Node: "n", StateDir: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got := envOf(t, env, Spec{})
	for _, k := range []string{"HAL_EVENT_FILE", "HAL_RUN_ID", "HAL_EVENT_ID", "HAL_FLOW", "HAL_NODE", "HAL_STATE_DIR"} {
		if got[k] == "" {
			t.Errorf("%s missing in process env", k)
		}
	}
	if _, ok := got["HAL_DRY_RUN"]; ok {
		t.Error("HAL_DRY_RUN must be absent outside dry-run")
	}
}

func TestBuildEnvOmitsUnsetValues(t *testing.T) {
	env, cleanup, err := BuildEnv(EnvOptions{Flow: "f"})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	for _, k := range []string{"HAL_RUN_ID", "HAL_EVENT_ID", "HAL_NODE", "HAL_STATE_DIR", "HAL_EVENT_FILE", "HAL_DRY_RUN", "HAL_LAST_SUCCESS_AT"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s should be unset", k)
		}
	}
	if env["HAL_FLOW"] != "f" {
		t.Errorf("HAL_FLOW = %q", env["HAL_FLOW"])
	}
}

func TestBuildEnvLastSuccessAtOnlyForSensors(t *testing.T) {
	ts := time.Date(2026, 11, 5, 10, 30, 0, 0, time.FixedZone("x", -5*3600))
	sensor, _, err := BuildEnv(EnvOptions{Sensor: true, LastSuccessAt: ts})
	if err != nil {
		t.Fatal(err)
	}
	if sensor["HAL_LAST_SUCCESS_AT"] != "2026-11-05T15:30:00Z" {
		t.Errorf("sensor HAL_LAST_SUCCESS_AT = %q", sensor["HAL_LAST_SUCCESS_AT"])
	}
	first, _, _ := BuildEnv(EnvOptions{Sensor: true})
	if _, ok := first["HAL_LAST_SUCCESS_AT"]; ok {
		t.Error("first poll must not set HAL_LAST_SUCCESS_AT")
	}
	node, _, _ := BuildEnv(EnvOptions{LastSuccessAt: ts})
	if _, ok := node["HAL_LAST_SUCCESS_AT"]; ok {
		t.Error("command nodes must not get HAL_LAST_SUCCESS_AT")
	}
}

func TestBuildEnvPrecedenceAndIsolation(t *testing.T) {
	t.Setenv("HAL_RUN_ID", "inherited")
	t.Setenv("HAL_DRY_RUN", "1")
	t.Setenv("HOME", "/home/test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "daemon-only")
	env, cleanup, err := BuildEnv(EnvOptions{
		EnvFile: map[string]string{"HOME": "/home/override", "HAL_RUN_ID": "spoofed", "HAL_NODE": "spoofed"},
		RunID:   "real",
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if env["HOME"] != "/home/override" {
		t.Errorf("env file must override base: HOME = %q", env["HOME"])
	}
	if env["HAL_RUN_ID"] != "real" {
		t.Errorf("HAL_RUN_ID = %q; computed value must beat env file and daemon env", env["HAL_RUN_ID"])
	}
	if _, ok := env["HAL_NODE"]; ok {
		t.Error("env file must not be able to set HAL_NODE")
	}
	if _, ok := env["HAL_DRY_RUN"]; ok {
		t.Error("daemon's own HAL_DRY_RUN leaked into the node")
	}
	if _, ok := env["AWS_SECRET_ACCESS_KEY"]; ok {
		t.Error("arbitrary daemon env vars must not be inherited")
	}
}

func TestDefaultBaseEnvPath(t *testing.T) {
	if got := DefaultBaseEnv("/custom")["PATH"]; got != "/custom" {
		t.Errorf("explicit PATH = %q", got)
	}
	t.Setenv("PATH", "")
	if got := DefaultBaseEnv("")["PATH"]; got != fallbackPATH {
		t.Errorf("fallback PATH = %q", got)
	}
}

func TestRunMergesEnvOntoBaseAndSetsCwd(t *testing.T) {
	state := t.TempDir()
	got := envOf(t, map[string]string{"FOO": "bar", "HAL_STATE_DIR": state}, Spec{Cwd: state})
	if got["FOO"] != "bar" {
		t.Errorf("FOO = %q", got["FOO"])
	}
	if got["PATH"] == "" {
		t.Error("PATH from base env missing")
	}
}

func TestWorkingDirDefaultsToStateDirUnlessCwdSet(t *testing.T) {
	skipIfMissing(t, "bash")
	state, other := t.TempDir(), t.TempDir()
	realPath := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	env := map[string]string{"HAL_STATE_DIR": state}

	res, err := Run(t.Context(), Spec{Lang: LangBash, Script: "pwd -P", Env: env})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != realPath(state) {
		t.Errorf("default cwd = %q, want state dir %q", got, realPath(state))
	}
	res, err = Run(t.Context(), Spec{Lang: LangBash, Script: "pwd -P", Env: env, Cwd: other})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != realPath(other) {
		t.Errorf("explicit cwd = %q, want %q", got, realPath(other))
	}
}

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hal9001/internal/spec"
)

// Env is the environment of a sandbox: hal's global env file overlaid by the sandbox's own env file.
type Env struct {
	// Vars are all variables. They include the backend's credentials (DEPOT_TOKEN, SSH_PASSWORD, ...), which New
	// keeps out of the environment commands see.
	Vars map[string]string
	// Secrets are the values of the variables not marked plain; they are redacted from everything a session returns.
	Secrets []string
}

// LoadEnv reads env and env.d/sandboxes/<backend>.env under home. A missing file is empty.
func LoadEnv(home, backend string) (Env, error) {
	out := Env{Vars: map[string]string{}}
	for _, f := range []struct {
		kind spec.Kind
		id   string
	}{{spec.KindEnv, ""}, {spec.KindSandboxEnv, backend}} {
		raw, _, err := spec.ReadFile(home, f.kind, f.id)
		if errors.Is(err, spec.ErrNotFound) {
			continue
		}
		if err != nil {
			return Env{}, err
		}
		env, _ := spec.ParseEnv(raw)
		for k, v := range env.Vars {
			out.Vars[k] = v
		}
		for _, v := range env.SecretVars() {
			out.Secrets = append(out.Secrets, v)
		}
	}
	return out, nil
}

// credentialVars are the variables a backend uses to authenticate itself; commands in the sandbox never see them.
var credentialVars = map[string][]string{
	spec.SandboxDepot: {envDepotToken, envDepotOrg},
	spec.SandboxSSH:   {envSSHPassword, envSSHPrivateKey, envSSHPassphrase},
}

func (e Env) commandVars(backend string) map[string]string {
	out := mergeEnv(e.Vars)
	for _, k := range credentialVars[backend] {
		delete(out, k)
	}
	return out
}

// CheckResult is the outcome of a connectivity check.
type CheckResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	// Fingerprint is the SHA256 host key fingerprint the ssh server presented, set when the host key is not
	// pinned or does not match. Pinning it (host_key) after the user confirmed it makes the check pass.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// New builds the backend for cfg. home is the hal home dir (the default local root lives under it). The
// returned Backend redacts the env's secrets from everything its sessions return. Backends that hold a
// connection (ssh) implement io.Closer.
func New(backend string, cfg spec.Sandbox, env Env, home string) (Backend, error) {
	if ds := spec.ValidateSandbox(backend, &cfg); spec.HasErrors(ds) {
		return nil, fmt.Errorf("sandbox %s: %s", backend, ds[0].Message)
	}
	cmdEnv := env.commandVars(backend)
	var b Backend
	switch backend {
	case spec.SandboxLocal:
		root := cfg.Root
		if root == "" {
			root = filepath.Join(home, "data", "sandboxes", "local")
		}
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("sandbox local: root %q must be an absolute path", root)
		}
		b = &localBackend{root: root, lifecycle: cfg.Lifecycle, timeout: cfg.Timeout.Std(), env: cmdEnv}
	case spec.SandboxDocker:
		b = newDockerBackend(cfg, cmdEnv)
	case spec.SandboxDepot:
		b = newDepotBackend(cfg, env.Vars, cmdEnv)
	case spec.SandboxKubernetes:
		b = newKubeBackend(cfg, cmdEnv)
	case spec.SandboxSSH:
		b = newSSHBackend(cfg, env.Vars, cmdEnv)
	default:
		return nil, fmt.Errorf("unknown sandbox backend %q", backend)
	}
	return &wrapped{redactBackend{b, env.Secrets}, b}, nil
}

// wrapped adds the optional interfaces of the inner backend to the redacting wrapper.
type wrapped struct {
	redactBackend
	inner Backend
}

// Close closes the inner backend's connection, if it has one.
func (w *wrapped) Close() error {
	if c, ok := w.inner.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

// Check tests that the backend works with cfg: Docker answers ping, kubectl reaches the cluster, ssh connects
// (and reports the host key fingerprint), Depot lists sandboxes, the local root is writable. Nothing is left
// behind.
func Check(ctx context.Context, backend string, cfg spec.Sandbox, env Env, home string) CheckResult {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	switch backend {
	case spec.SandboxLocal:
		return checkLocal(cfg, home)
	case spec.SandboxDocker:
		return newDockerBackend(cfg, env.commandVars(backend)).check(ctx)
	case spec.SandboxDepot:
		return newDepotBackend(cfg, env.Vars, env.commandVars(backend)).check(ctx)
	case spec.SandboxKubernetes:
		return newKubeBackend(cfg, env.commandVars(backend)).check(ctx)
	case spec.SandboxSSH:
		b := newSSHBackend(cfg, env.Vars, env.commandVars(backend))
		defer func() { _ = b.Close() }()
		return b.check(ctx)
	}
	return CheckResult{Message: fmt.Sprintf("unknown sandbox backend %q", backend)}
}

func checkLocal(cfg spec.Sandbox, home string) CheckResult {
	root := cfg.Root
	if root == "" {
		root = filepath.Join(home, "data", "sandboxes", "local")
	}
	if !filepath.IsAbs(root) {
		return CheckResult{Message: fmt.Sprintf("root %q must be an absolute path", root)}
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return CheckResult{Message: "cannot create the root directory: " + err.Error()}
	}
	f, err := os.CreateTemp(root, ".hal-check-*")
	if err != nil {
		return CheckResult{Message: "root directory is not writable: " + err.Error()}
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	return CheckResult{OK: true, Message: "root directory " + root + " is writable"}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/coder/acp-go-sdk"
)

// ProbeResult is what Probe learned about a runtime.
type ProbeResult struct {
	AgentInfo    *AgentInfo
	Capabilities json.RawMessage
	LoadSession  bool
	// Modes are the session mode ids offered by session/new.
	Modes []string
	// ConfigOptionIDs are the ids of the session config options (model, effort, ...).
	ConfigOptionIDs []string
	// Argv is the command that was run.
	Argv []string
	// Stderr is the tail of the agent's stderr.
	Stderr   string
	Duration time.Duration
}

// Probe spawns the runtime's command (with the restrictive "ask" spawn
// configuration), runs initialize and a throwaway session/new in dir, and stops
// the process. It reports what the agent advertised; a runtime that cannot
// start or answer is an error (the partial result still carries Argv, Stderr
// and Duration). env is the process environment; nil means os.Environ().
func Probe(ctx context.Context, cfg RuntimeConfig, dir string, env []string, grace time.Duration) (res ProbeResult, err error) {
	start := time.Now()
	spawn := cfg.Ask
	res.Argv = append(append([]string(nil), cfg.Command...), spawn.Args...)
	if env == nil {
		env = osEnviron()
	}
	proc, err := startProcess(res.Argv, dir, buildEnv(env, cfg.Env, spawn.Env))
	if err != nil {
		res.Duration = time.Since(start)
		return res, fmt.Errorf("spawn %s: %w", res.Argv[0], err)
	}
	defer func() {
		proc.stop(time.Second, orDefault(grace, DefaultKillGrace))
		res.Stderr = proc.stderr.String()
		res.Duration = time.Since(start)
	}()

	conn := acp.NewClientSideConnection(probeClient{}, proc.stdin, proc.stdout)
	var caps acp.ClientCapabilities
	if cfg.Elicitation {
		caps.Elicitation = &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}
	}
	init, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientCapabilities: caps,
		ClientInfo:         &acp.Implementation{Name: "hal9001", Version: "probe"},
	})
	if err != nil {
		return res, probeErr(ctx, proc, "initialize", err)
	}
	res.Capabilities, _ = json.Marshal(init.AgentCapabilities)
	res.LoadSession = init.AgentCapabilities.LoadSession
	if init.AgentInfo != nil {
		ai := &AgentInfo{Name: init.AgentInfo.Name, Version: init.AgentInfo.Version}
		if init.AgentInfo.Title != nil {
			ai.Title = *init.AgentInfo.Title
		}
		res.AgentInfo = ai
	}
	sess, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: dir, McpServers: []acp.McpServer{}, Meta: cfg.SessionMeta})
	if err != nil {
		return res, probeErr(ctx, proc, "session/new", err)
	}
	if sess.Modes != nil {
		for _, m := range sess.Modes.AvailableModes {
			res.Modes = append(res.Modes, string(m.Id))
		}
	}
	for _, o := range sess.ConfigOptions {
		switch {
		case o.Select != nil:
			res.ConfigOptionIDs = append(res.ConfigOptionIDs, string(o.Select.Id))
		case o.Boolean != nil:
			res.ConfigOptionIDs = append(res.ConfigOptionIDs, string(o.Boolean.Id))
		}
	}
	return res, nil
}

func probeErr(ctx context.Context, p *process, what string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", what, context.Cause(ctx))
	}
	if p.waitExit(500 * time.Millisecond) {
		return fmt.Errorf("%s: %w (%s)", what, err, describeExit(p))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// probeClient is the acp.Client of a probe: no capabilities, every request refused.
type probeClient struct{}

var _ acp.Client = probeClient{}

func (probeClient) SessionUpdate(context.Context, acp.SessionNotification) error { return nil }

func (probeClient) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
}

func (probeClient) UnstableCreateElicitation(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	return acp.UnstableCreateElicitationResponse{Decline: &acp.UnstableCreateElicitationDecline{Action: "decline"}}, nil
}

func (probeClient) UnstableCompleteElicitation(context.Context, acp.UnstableCompleteElicitationNotification) error {
	return nil
}

var errProbeClient = errors.New("probe client offers no capabilities")

func (probeClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errProbeClient
}

func (probeClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errProbeClient
}

func (probeClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errProbeClient
}

func (probeClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errProbeClient
}

func (probeClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errProbeClient
}

func (probeClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errProbeClient
}

func (probeClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errProbeClient
}

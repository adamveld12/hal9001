package engine

import (
	"context"
	"encoding/json"
	"sync"

	"hal9001/internal/sandbox"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// Limits of one sandbox session's log. Each command's output is capped by the sandbox package (sandbox.LogOutputLimit
// per stream); these bound the whole run.
const (
	maxSandboxLogEntries = 5000
	maxSandboxLogBytes   = 8 << 20
)

// SandboxRunLog opens the log of the sandbox session that the agent run rs.RunID is about to start (tools.SandboxOptions.Log).
// It records a store.SandboxSession for the node run and returns the sink the sandbox package writes to; it returns nil
// (no log) when the session cannot be recorded. Everything written is redacted with the current env secrets.
func (e *Engine) SandboxRunLog(_ context.Context, rs tools.RunSpec, info tools.RunInfo) sandbox.RunLog {
	ss := &store.SandboxSession{
		NodeRunID: info.NodeRunID, AgentRunID: rs.RunID, Backend: rs.Sandbox, Status: store.SandboxProvisioning,
		Image: rs.Image, Repo: sandbox.CleanRepo(rs.Repo), Ref: rs.Ref, EnvNames: []string{},
	}
	if ss.NodeRunID == "" {
		return nil
	}
	if err := e.st.CreateSandboxSession(e.dbCtx, ss); err != nil {
		e.log.Warn("engine: record sandbox session", "agent_run", rs.RunID, "err", err)
		return nil
	}
	return &sandboxRunLog{e: e, ss: *ss}
}

type sandboxRunLog struct {
	e *Engine

	mu      sync.Mutex
	ss      store.SandboxSession
	entries int
	bytes   int
	full    bool // the entry cap was reached; only lifecycle entries are still kept
	capped  bool // the byte cap was reached; entries lose their output
}

var _ sandbox.RunLog = (*sandboxRunLog)(nil)

func (l *sandboxRunLog) Add(en sandbox.LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	red := l.e.snapshot().red
	if en.Level == "" {
		en.Level = sandbox.LevelInfo
	}
	data := store.SandboxLogData{
		Tool: red.String(en.Tool), Approval: red.String(en.Approval), Command: red.String(en.Command), Cwd: en.Cwd, Path: red.String(en.Path),
		ExitCode: en.ExitCode, Bytes: en.Bytes, Stdout: red.String(en.Stdout), Stderr: red.String(en.Stderr),
		TimedOut: en.TimedOut, Truncated: en.Truncated, Error: red.String(en.Error),
	}
	if en.Duration > 0 {
		data.DurationMS = new(int64)
		*data.DurationMS = en.Duration.Milliseconds()
	}
	if len(en.Fields) > 0 {
		data.Fields = make(map[string]string, len(en.Fields))
		for k, v := range en.Fields {
			data.Fields[k] = red.String(v)
		}
	}
	title := red.String(en.Title)

	if l.entries >= maxSandboxLogEntries && en.Kind != sandbox.KindLifecycle {
		if !l.full {
			l.full = true
			l.markTruncated()
			l.write(store.SandboxKindLifecycle, store.SandboxWarn, "Log is full: further commands and file operations are not recorded", store.SandboxLogData{})
		}
		return
	}
	size := len(title) + len(data.Command) + len(data.Stdout) + len(data.Stderr) + len(data.Error)
	if l.bytes+size > maxSandboxLogBytes && (data.Stdout != "" || data.Stderr != "") {
		if !l.capped {
			l.capped = true
			l.markTruncated()
		}
		const dropped = "[output not recorded: the run's sandbox log reached its size limit]"
		data.Stdout, data.Stderr, data.Truncated = "", dropped, true
		size = len(title) + len(data.Command) + len(dropped) + len(data.Error)
	}
	l.bytes += size
	l.write(en.Kind, en.Level, title, data)
}

// write stores one entry. The caller holds mu.
func (l *sandboxRunLog) write(kind, level, title string, data store.SandboxLogData) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	entry := &store.SandboxLogEntry{SessionID: l.ss.ID, Kind: kind, Level: level, Title: title, Data: raw}
	if err := l.e.st.AppendSandboxLogEntry(l.e.dbCtx, entry); err != nil {
		l.e.log.Warn("engine: record sandbox log entry", "sandbox_session", l.ss.ID, "err", err)
		return
	}
	l.entries++
}

// markTruncated flags the session. The caller holds mu.
func (l *sandboxRunLog) markTruncated() {
	l.ss.Truncated = true
	l.save()
}

func (l *sandboxRunLog) Update(u sandbox.SessionUpdate) {
	l.mu.Lock()
	defer l.mu.Unlock()
	red := l.e.snapshot().red
	ss := &l.ss
	now := l.e.st.Now()
	if u.Status != "" {
		ss.Status = u.Status
		switch u.Status {
		case sandbox.StatusReady:
			ss.ReadyAt = &now
		case sandbox.StatusFailed, sandbox.StatusClosed:
			ss.EndedAt = &now
		}
	}
	set := func(dst *string, v string) {
		if v != "" {
			*dst = red.String(v)
		}
	}
	set(&ss.SandboxID, u.Info.ID)
	set(&ss.SandboxName, u.Info.Name)
	set(&ss.Image, u.Info.Image)
	set(&ss.Repo, u.Info.Repo)
	set(&ss.Ref, u.Info.Ref)
	set(&ss.Lifecycle, u.Info.Lifecycle)
	set(&ss.Workdir, u.Info.Workdir)
	set(&ss.Error, u.Error)
	if len(u.Info.EnvNames) > 0 {
		ss.EnvNames = u.Info.EnvNames
	}
	l.save()
}

// save persists the header. The caller holds mu.
func (l *sandboxRunLog) save() {
	ss := l.ss
	if err := l.e.st.UpdateSandboxSession(l.e.dbCtx, &ss); err != nil {
		l.e.log.Warn("engine: update sandbox session", "sandbox_session", ss.ID, "err", err)
	}
}

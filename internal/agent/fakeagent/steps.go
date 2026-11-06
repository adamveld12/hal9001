package fakeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// turnState carries what steps need while a turn runs. s is nil for startup
// steps (only the process-level ops are allowed there).
type turnState struct {
	a       *Agent
	s       *session
	ctx     context.Context
	prompt  string
	emitted map[string]bool // tool call ids already announced
	// lastOption is the permission option the client chose most recently.
	lastOption string
}

func (t *turnState) cwd() string {
	if t.s != nil {
		return t.s.cwd
	}
	wd, _ := os.Getwd()
	return wd
}

func (t *turnState) expand(in string) string {
	if t.s == nil {
		return in
	}
	t.a.mu.Lock()
	mode, turns := t.s.mode, t.s.turns
	t.a.mu.Unlock()
	return strings.NewReplacer(
		"{{prompt}}", t.prompt,
		"{{cwd}}", t.s.cwd,
		"{{session_id}}", t.s.id,
		"{{mode}}", mode,
		"{{turn}}", strconv.Itoa(turns),
	).Replace(in)
}

func (t *turnState) abs(p string) string {
	p = t.expand(p)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(t.cwd(), p)
}

// emit sends a session/update and persists it for later session/load replay.
func (t *turnState) emit(u acp.SessionUpdate) error {
	if t.s == nil {
		return errUnsupportedStartupOp
	}
	if raw, err := marshalUpdate(u); err == nil {
		t.a.persist(t.s.id, record{T: "update", Update: raw})
	}
	return t.a.conn.SessionUpdate(t.ctx, acp.SessionNotification{SessionId: acp.SessionId(t.s.id), Update: u})
}

func (t *turnState) sleep(d time.Duration, ignoreCancel bool) bool {
	if ignoreCancel {
		time.Sleep(d)
		return false
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-t.ctx.Done():
		return true
	case <-tm.C:
		return false
	}
}

func (t *turnState) toolContent(in []Content) []acp.ToolCallContent {
	out := make([]acp.ToolCallContent, 0, len(in))
	for _, c := range in {
		switch c.Type {
		case "diff":
			var old []string
			if c.OldText != nil {
				old = append(old, t.expand(*c.OldText))
			}
			out = append(out, acp.ToolDiffContent(t.expand(c.Path), t.expand(c.NewText), old...))
		default:
			out = append(out, acp.ToolContent(acp.TextBlock(t.expand(c.Text))))
		}
	}
	return out
}

func (t *turnState) locations(paths []string) []acp.ToolCallLocation {
	var out []acp.ToolCallLocation
	for _, p := range paths {
		out = append(out, acp.ToolCallLocation{Path: t.expand(p)})
	}
	return out
}

func (t *turnState) announce(st Step) error {
	t.emitted[st.ID] = true
	opts := []acp.ToolCallStartOpt{
		acp.WithStartStatus(acp.ToolCallStatus(firstNonEmpty(st.Status, string(acp.ToolCallStatusPending)))),
		acp.WithStartKind(acp.ToolKind(firstNonEmpty(st.Kind, string(acp.ToolKindOther)))),
	}
	if st.RawInput != nil {
		opts = append(opts, acp.WithStartRawInput(st.RawInput))
	}
	if len(st.Content) > 0 {
		opts = append(opts, acp.WithStartContent(t.toolContent(st.Content)))
	}
	if len(st.Locations) > 0 {
		opts = append(opts, acp.WithStartLocations(t.locations(st.Locations)))
	}
	return t.emit(acp.StartToolCall(acp.ToolCallId(st.ID), t.expand(st.Title), opts...))
}

func (t *turnState) update(id, status string, st Step) error {
	opts := []acp.ToolCallUpdateOpt{acp.WithUpdateStatus(acp.ToolCallStatus(firstNonEmpty(status, string(acp.ToolCallStatusCompleted))))}
	if st.Title != "" {
		opts = append(opts, acp.WithUpdateTitle(t.expand(st.Title)))
	}
	if st.RawOutput != nil {
		opts = append(opts, acp.WithUpdateRawOutput(st.RawOutput))
	}
	if len(st.Content) > 0 {
		opts = append(opts, acp.WithUpdateContent(t.toolContent(st.Content)))
	}
	return t.emit(acp.UpdateToolCall(acp.ToolCallId(id), opts...))
}

// run executes steps in order. cancelled reports that the client cancelled
// the turn (the caller then answers stopReason=cancelled).
func (t *turnState) run(steps []Step) (cancelled bool, err error) {
	for _, st := range steps {
		if !st.IgnoreCancel && t.ctx.Err() != nil {
			return true, nil
		}
		cancelled, err = t.exec(st)
		if err != nil || cancelled {
			return cancelled, err
		}
	}
	return false, nil
}

func (t *turnState) exec(st Step) (bool, error) {
	switch st.Op {
	case OpMessage, OpThought:
		chunks := st.Chunks
		if len(chunks) == 0 {
			chunks = []string{st.Text}
		}
		for i, c := range chunks {
			if i > 0 && st.DelayMS > 0 && t.sleep(time.Duration(st.DelayMS)*time.Millisecond, false) {
				return true, nil
			}
			text := t.expand(c)
			u := acp.UpdateAgentMessageText(text)
			if st.Op == OpThought {
				u = acp.UpdateAgentThoughtText(text)
			}
			if err := t.emit(u); err != nil {
				return false, err
			}
		}
	case OpPlan:
		entries := make([]acp.PlanEntry, 0, len(st.Entries))
		for _, e := range st.Entries {
			entries = append(entries, acp.PlanEntry{
				Content:  t.expand(e.Content),
				Status:   acp.PlanEntryStatus(firstNonEmpty(e.Status, string(acp.PlanEntryStatusPending))),
				Priority: acp.PlanEntryPriority(firstNonEmpty(e.Priority, string(acp.PlanEntryPriorityMedium))),
			})
		}
		return false, t.emit(acp.UpdatePlan(entries...))
	case OpToolCall:
		return false, t.announce(st)
	case OpToolUpdate:
		return false, t.update(st.ID, st.Status, st)
	case OpPermission:
		return t.permission(st)
	case OpSleep:
		return t.sleep(time.Duration(st.MS)*time.Millisecond, st.IgnoreCancel), nil
	case OpExit:
		os.Exit(st.Code)
	case OpHang:
		if st.IgnoreCancel {
			stuck.Store(true)
			select {}
		}
		<-t.ctx.Done()
		return true, nil
	case OpMCPCall:
		return t.permission(Step{
			Op: OpPermission, ID: st.ID, Kind: "other", Title: "mcp__" + firstNonEmpty(st.Name, "hal") + "__" + st.Tool, RawInput: st.Args,
			OnAllow:  []Step{{Op: OpMCPInvoke, ID: st.ID, Name: st.Name, Tool: st.Tool, Args: st.Args, Path: st.Path}},
			OnReject: []Step{{Op: OpMessage, Text: "mcp[" + st.ID + "] rejected by the client"}},
		})
	case OpMCPInvoke:
		return false, t.mcpInvoke(st)
	case OpIgnoreSIGTRM:
		signal.Ignore(syscall.SIGTERM)
	case OpStderr:
		fmt.Fprintln(os.Stderr, t.expand(st.Text))
	case OpError:
		return false, fmt.Errorf("%s", t.expand(st.Text))
	case OpWriteFile:
		p := t.abs(st.Path)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return false, err
		}
		return false, os.WriteFile(p, []byte(t.expand(st.Data)), 0o600)
	case OpDeleteFile:
		return false, os.Remove(t.abs(st.Path))
	case OpExec:
		cmd := exec.CommandContext(t.ctx, t.expand(st.Cmd[0]), t.expandAll(st.Cmd[1:])...) //nolint:gosec // scripted test command
		cmd.Dir = t.cwd()
		if out, err := cmd.CombinedOutput(); err != nil {
			return false, fmt.Errorf("exec %v: %w: %s", st.Cmd, err, out)
		}
	case OpSpawnChild:
		cmd := exec.CommandContext(context.Background(), t.expand(st.Cmd[0]), t.expandAll(st.Cmd[1:])...) //nolint:gosec // scripted test command
		cmd.Dir = t.cwd()
		if err := cmd.Start(); err != nil {
			return false, err
		}
		go func() { _ = cmd.Wait() }()
		if st.PidFile != "" {
			if err := os.WriteFile(t.abs(st.PidFile), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
				return false, err
			}
		}
	default:
		return false, fmt.Errorf("unknown op %q", st.Op)
	}
	return false, nil
}

func (t *turnState) expandAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = t.expand(s)
	}
	return out
}

func (t *turnState) permission(st Step) (bool, error) {
	if !t.emitted[st.ID] && !st.NoToolCall {
		if err := t.announce(st); err != nil {
			return false, err
		}
	}
	opts := make([]acp.PermissionOption, 0, 2)
	src := st.Options
	if len(src) == 0 {
		src = []Option{
			{ID: "allow", Name: "Allow", Kind: string(acp.PermissionOptionKindAllowOnce)},
			{ID: "reject", Name: "Reject", Kind: string(acp.PermissionOptionKindRejectOnce)},
		}
	}
	for _, o := range src {
		opts = append(opts, acp.PermissionOption{OptionId: acp.PermissionOptionId(o.ID), Name: firstNonEmpty(o.Name, o.ID), Kind: acp.PermissionOptionKind(o.Kind)})
	}
	tc := acp.ToolCallUpdate{
		ToolCallId: acp.ToolCallId(st.ID),
		Title:      acp.Ptr(t.expand(st.Title)),
		Kind:       acp.Ptr(acp.ToolKind(firstNonEmpty(st.Kind, string(acp.ToolKindOther)))),
		Status:     acp.Ptr(acp.ToolCallStatusPending),
		RawInput:   st.RawInput,
		Locations:  t.locations(st.Locations),
		Content:    t.toolContent(st.Content),
	}
	outcome, err := t.askClient(st, tc, opts, src)
	if err != nil {
		if t.ctx.Err() != nil {
			_, _ = t.run(st.OnCancel)
			return true, nil
		}
		return false, err
	}
	if outcome == "cancelled" {
		t.a.logCall(map[string]any{"method": "permission_outcome", "tool_call_id": st.ID, "outcome": "cancelled"})
		_, err := t.run(st.OnCancel)
		return true, err
	}
	allow := outcome == "allow"
	t.a.logCall(map[string]any{"method": "permission_outcome", "tool_call_id": st.ID, "outcome": outcome, "allowed": allow, "option_id": t.lastOption})
	branch, status := st.OnReject, string(acp.ToolCallStatusFailed)
	if allow {
		branch, status = st.OnAllow, string(acp.ToolCallStatusCompleted)
	}
	cancelled, err := t.run(branch)
	if err != nil || cancelled {
		return cancelled, err
	}
	if !allow && st.CancelOnReject {
		return true, nil // codex-style: a rejected call ends the whole turn
	}
	if !st.NoAutoDone {
		return false, t.update(st.ID, status, Step{})
	}
	return false, nil
}

// askClient sends the permission prompt (session/request_permission, or an
// elicitation form when st.Via is "elicitation") and returns allow, reject or
// cancelled.
func (t *turnState) askClient(st Step, tc acp.ToolCallUpdate, opts []acp.PermissionOption, src []Option) (string, error) {
	if st.Via == "elicitation" {
		return t.askElicitation(st)
	}
	resp, err := t.a.conn.RequestPermission(t.ctx, acp.RequestPermissionRequest{
		SessionId: acp.SessionId(t.s.id),
		ToolCall:  tc,
		Options:   opts,
	})
	if err != nil {
		return "", err
	}
	if resp.Outcome.Selected == nil {
		return "cancelled", nil
	}
	chosen := string(resp.Outcome.Selected.OptionId)
	t.lastOption = chosen
	for _, o := range src {
		if o.ID == chosen && strings.HasPrefix(o.Kind, "allow") {
			return "allow", nil
		}
	}
	return "reject", nil
}

// askElicitation mimics runtimes that approve non-execute tools through an
// elicitation form. Like them, it refuses the tool outright when the client
// did not advertise form elicitation.
func (t *turnState) askElicitation(st Step) (string, error) {
	if !t.a.clientElicitation() {
		return "reject", nil
	}
	name := t.expand(firstNonEmpty(st.Name, st.Title))
	enum := []any{"Approve", "Approve " + name + " for this session", "Deny"}
	resp, err := t.a.conn.UnstableCreateElicitation(t.ctx, acp.UnstableCreateElicitationRequest{Form: &acp.UnstableCreateElicitationForm{
		Mode:    "form",
		Message: "Allow tool: " + name + "\nDetails: " + t.expand(st.Text),
		RequestedSchema: acp.UnstableElicitationSchema{
			Type:       "object",
			Properties: map[string]any{"value": map[string]any{"type": "string", "enum": enum}},
			Required:   []string{"value"},
		},
	}})
	if err != nil {
		return "", err
	}
	switch {
	case resp.Accept != nil:
		if v, _ := resp.Accept.Content["value"].(string); strings.HasPrefix(v, "Approve") {
			return "allow", nil
		}
		return "reject", nil
	case resp.Decline != nil:
		return "reject", nil
	}
	return "cancelled", nil
}

// mcpInvoke calls a tool on one of the MCP servers the client injected (stdio only) and reports the outcome as an
// agent message "mcp[<id>] is_error=<bool> <text>", so tests can read it from the run's final message.
func (t *turnState) mcpInvoke(st Step) error {
	name := firstNonEmpty(st.Name, "hal")
	var srv *acp.McpServerStdio
	for _, m := range t.s.mcp {
		if m.Stdio != nil && m.Stdio.Name == name {
			srv = m.Stdio
		}
	}
	if srv == nil {
		return fmt.Errorf("mcp_call: the client injected no stdio MCP server %q", name)
	}
	cmd := exec.CommandContext(t.ctx, srv.Command, srv.Args...) //nolint:gosec // the command is what the client under test injected
	cmd.Env = os.Environ()
	for _, e := range srv.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "fake-acp-agent", Version: "0"}, nil)
	cs, err := client.Connect(t.ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return t.emit(acp.UpdateAgentMessageText(fmt.Sprintf("mcp[%s] is_error=true connect: %v", st.ID, err)))
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(t.ctx, &mcp.CallToolParams{Name: st.Tool, Arguments: st.Args})
	if err != nil {
		return t.emit(acp.UpdateAgentMessageText(fmt.Sprintf("mcp[%s] is_error=true call: %v", st.ID, err)))
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if st.Path != "" && !res.IsError {
		p := t.abs(st.Path)
		if err := os.WriteFile(p, []byte(text.String()), 0o600); err != nil {
			return err
		}
	}
	return t.emit(acp.UpdateAgentMessageText(fmt.Sprintf("mcp[%s] is_error=%t %s", st.ID, res.IsError, text.String())))
}

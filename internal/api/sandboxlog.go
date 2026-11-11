package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"hal9001/internal/store"
)

const (
	defaultSandboxLogLimit = 200
	maxSandboxLogLimit     = 1000
	// maxSandboxRawBytes caps the text of a downloaded sandbox log.
	maxSandboxRawBytes = 8 << 20
)

// sandboxEntryView is a log entry with its kind-specific data at the top level.
type sandboxEntryView struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id"`
	TS        time.Time `json:"ts"`
	Kind      string    `json:"kind"`
	Level     string    `json:"level"`
	Title     string    `json:"title"`
	store.SandboxLogData
}

func newSandboxEntryView(e store.SandboxLogEntry) sandboxEntryView {
	return sandboxEntryView{ID: e.ID, SessionID: e.SessionID, TS: e.TS, Kind: e.Kind, Level: e.Level, Title: e.Title, SandboxLogData: e.Decode()}
}

// sandboxLogView is the response of GET .../sandbox-log.
type sandboxLogView struct {
	Sessions []store.SandboxSession `json:"sessions"`
	Entries  []sandboxEntryView     `json:"entries"`
	HasMore  bool                   `json:"has_more"`
}

// sandboxLogAppendedView is the data of `sandbox_log.appended`.
type sandboxLogAppendedView struct {
	sandboxEntryView
	AgentRunID      string `json:"agent_run_id"`
	NodeRunID       string `json:"node_run_id"`
	AssistSessionID string `json:"assist_session_id"`
}

func (s *Server) getAgentRunSandboxLog(w http.ResponseWriter, r *http.Request) {
	s.sandboxLog(w, r, store.SandboxFilter{AgentRunID: r.PathValue("id")}, false)
}

func (s *Server) getNodeRunSandboxLog(w http.ResponseWriter, r *http.Request) {
	s.sandboxLog(w, r, store.SandboxFilter{NodeRunID: r.PathValue("id")}, false)
}

// conversationSandboxFilter selects the sessions of a conversation: a chat's own (assist session id), or the agent
// run's for a conversation that is a flow node's agent run.
func (s *Server) conversationSandboxFilter(w http.ResponseWriter, r *http.Request) (store.SandboxFilter, bool) {
	id := r.PathValue("id")
	if _, err := s.opts.Store.GetAssistSession(r.Context(), id); err == nil {
		return store.SandboxFilter{AssistSessionID: id}, true
	} else if !errors.Is(err, store.ErrNotFound) {
		s.writeError(w, r, err)
		return store.SandboxFilter{}, false
	}
	return store.SandboxFilter{AgentRunID: id}, true
}

func (s *Server) getConversationSandboxLog(w http.ResponseWriter, r *http.Request) {
	if f, ok := s.conversationSandboxFilter(w, r); ok {
		s.sandboxLog(w, r, f, false)
	}
}

func (s *Server) downloadConversationSandboxLog(w http.ResponseWriter, r *http.Request) {
	if f, ok := s.conversationSandboxFilter(w, r); ok {
		s.sandboxLog(w, r, f, true)
	}
}

func (s *Server) downloadAgentRunSandboxLog(w http.ResponseWriter, r *http.Request) {
	s.sandboxLog(w, r, store.SandboxFilter{AgentRunID: r.PathValue("id")}, true)
}

func (s *Server) downloadNodeRunSandboxLog(w http.ResponseWriter, r *http.Request) {
	s.sandboxLog(w, r, store.SandboxFilter{NodeRunID: r.PathValue("id")}, true)
}

// sandboxSessions loads the sessions of f's run. It answers 404 (and returns false) when the run does not exist:
// a node run needs its row; an agent run may have only sessions (provisioning failed before the row was created).
func (s *Server) sandboxSessions(w http.ResponseWriter, r *http.Request, f store.SandboxFilter) ([]store.SandboxSession, bool) {
	st := s.opts.Store
	if f.NodeRunID != "" {
		if _, err := st.GetNodeRun(r.Context(), f.NodeRunID); err != nil {
			s.writeError(w, r, err)
			return nil, false
		}
	}
	sessions, err := st.ListSandboxSessions(r.Context(), f)
	if err != nil {
		s.writeError(w, r, err)
		return nil, false
	}
	if f.AgentRunID != "" && len(sessions) == 0 {
		if _, err := st.GetAgentRun(r.Context(), f.AgentRunID); err != nil {
			s.writeError(w, r, err)
			return nil, false
		}
	}
	return sessions, true
}

func (s *Server) sandboxLog(w http.ResponseWriter, r *http.Request, f store.SandboxFilter, raw bool) {
	ef := store.SandboxEntryFilter{SandboxFilter: f, Limit: defaultSandboxLogLimit}
	if !raw {
		q := newQuery(r)
		if v := q.str("after_id"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				q.fail("after_id must be a non-negative integer")
			} else {
				ef.AfterID = n
			}
		}
		if v := q.str("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > maxSandboxLogLimit {
				q.fail("limit must be an integer from 1 to %d", maxSandboxLogLimit)
			} else {
				ef.Limit = n
			}
		}
		if t := q.boolp("tail"); t != nil {
			ef.Tail = *t
		}
		if !q.ok(w) {
			return
		}
	}
	sessions, ok := s.sandboxSessions(w, r, f)
	if !ok {
		return
	}
	if raw {
		s.writeSandboxRaw(w, r, f, sessions)
		return
	}
	entries, more, err := s.opts.Store.ListSandboxLogEntries(r.Context(), ef)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := sandboxLogView{Sessions: sessions, Entries: make([]sandboxEntryView, 0, len(entries)), HasMore: more}
	for _, e := range entries {
		out.Entries = append(out.Entries, newSandboxEntryView(e))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) writeSandboxRaw(w http.ResponseWriter, r *http.Request, f store.SandboxFilter, sessions []store.SandboxSession) {
	id := f.AgentRunID
	if id == "" {
		id = f.NodeRunID
	}
	if id == "" {
		id = f.AssistSessionID
	}
	var b strings.Builder
	b.WriteString(renderSandboxHeader(sessions))
	ef := store.SandboxEntryFilter{SandboxFilter: f, Limit: maxSandboxLogLimit}
	for b.Len() < maxSandboxRawBytes {
		entries, more, err := s.opts.Store.ListSandboxLogEntries(r.Context(), ef)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		for _, e := range entries {
			b.WriteString(renderSandboxEntry(e))
			ef.AfterID = e.ID
		}
		if !more || len(entries) == 0 {
			break
		}
	}
	text := b.String()
	if len(text) > maxSandboxRawBytes {
		text = text[:maxSandboxRawBytes] + "\n[log cut at 8 MiB]\n"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "sandbox-log-"+id+".txt"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(text))
}

// renderSandboxHeader renders one block per session.
func renderSandboxHeader(sessions []store.SandboxSession) string {
	var b strings.Builder
	for _, ss := range sessions {
		fmt.Fprintf(&b, "sandbox %s (%s)\n", ss.ID, ss.Backend)
		kv := func(k, v string) {
			if v != "" {
				fmt.Fprintf(&b, "  %s: %s\n", k, v)
			}
		}
		kv("status", ss.Status)
		kv("sandbox id", ss.SandboxID)
		kv("sandbox name", ss.SandboxName)
		kv("image", ss.Image)
		if ss.Repo != "" {
			kv("repo", ss.Repo+"@"+ss.Ref)
		}
		kv("workdir", ss.Workdir)
		kv("env", strings.Join(ss.EnvNames, ", "))
		kv("started", ss.StartedAt.UTC().Format(time.RFC3339))
		kv("error", ss.Error)
		if ss.Truncated {
			kv("note", "log truncated")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderSandboxEntry renders one entry as a block: a title line, then indented details.
func renderSandboxEntry(e store.SandboxLogEntry) string {
	d := e.Decode()
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s\n", e.TS.UTC().Format(time.RFC3339), strings.ToUpper(e.Level), e.Title)
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "  %s: %s\n", k, v)
		}
	}
	line("tool", d.Tool)
	line("approval", d.Approval)
	line("cwd", d.Cwd)
	line("path", d.Path)
	if d.ExitCode != nil {
		line("exit code", strconv.Itoa(*d.ExitCode))
	}
	if d.DurationMS != nil {
		line("duration", (time.Duration(*d.DurationMS) * time.Millisecond).String())
	}
	if d.Bytes != nil {
		line("bytes", strconv.FormatInt(*d.Bytes, 10))
	}
	if d.TimedOut {
		line("timed out", "yes")
	}
	if d.Truncated {
		line("truncated", "yes")
	}
	line("command", d.Command)
	block := func(name, text string) {
		if text == "" {
			return
		}
		fmt.Fprintf(&b, "  %s:\n", name)
		for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			fmt.Fprintf(&b, "    %s\n", l)
		}
	}
	block("stdout", d.Stdout)
	block("stderr", d.Stderr)
	line("error", d.Error)
	keys := make([]string, 0, len(d.Fields))
	for k := range d.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s=%s\n", k, d.Fields[k])
	}
	return b.String()
}

// sandboxMeta resolves the stream filters of a session.
func (rv *resolver) sandboxMeta(ss store.SandboxSession) map[string]string {
	meta := map[string]string{"agent_run_id": ss.AgentRunID, "assist_session_id": ss.AssistSessionID}
	if nr, err := rv.nodeRun(ss.NodeRunID); err == nil && nr != nil {
		meta["flow_run_id"] = nr.FlowRunID
		if fr, err := rv.flowRun(nr.FlowRunID); err == nil && fr != nil {
			meta["flow_id"] = fr.FlowID
		}
	}
	return optionalMeta(meta)
}

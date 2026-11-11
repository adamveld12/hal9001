package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Limits of the read tools an assist session gets: one result is at most MaxReadResult bytes, and one string inside it
// (a rendered prompt, a script, a command's output) at most MaxReadString.
const (
	MaxReadResult = 64 << 10
	MaxReadString = 12 << 10
)

// WithAPI returns ctx carrying the REST API the read tools call. The daemon sets it, per call, for the sessions that
// get ReadTools; the API it supplies serves reads only.
func WithAPI(ctx context.Context, api APIFunc) context.Context {
	return withEnv(ctx, OperatorEnv{API: api})
}

// ReadTools is the read-only tool set of an assist session: everything the hal9001 CLI can list, get and inspect
// (flows, runs, node runs, agent runs and their transcripts, sensors and their polls, events, inbox, approvals,
// sandbox logs, custom tools, prompts, runtimes, sandboxes, settings, status, queue). It shares its definitions with
// the operator tools MCP clients get (operator.go) and adds the ones only a debugging session needs.
//
// No tool here changes anything: each is one GET of the REST API, and the API a session gets (WithAPI) refuses every
// other method. Calls need no approval. Results are capped: see MaxReadResult.
func ReadTools() []Tool {
	var all []operatorTool
	all = append(all, operatorReadTools(false)...)
	all = append(all, debugReadTools()...)
	out := make([]Tool, 0, len(all))
	for _, t := range all {
		if t.NeedsApproval || t.Service != "" {
			panic("tools: read tool " + t.Name + " is not read-only") // a programming error caught by any test
		}
		out = append(out, capped(t.Tool))
	}
	return out
}

// capped limits the size of a tool's result (see ShrinkJSON).
func capped(t Tool) Tool {
	run := t.Run
	t.Run = func(ctx context.Context, c Call) (Result, error) {
		res, err := run(ctx, c)
		res.Text = ShrinkJSON(res.Text, MaxReadResult, MaxReadString)
		return res, err
	}
	return t
}

// ShrinkJSON keeps a JSON result within limit bytes. A result that fits is returned as is. A larger one has every
// string longer than maxString cut (head and tail kept, a marker between), which keeps its structure; when it is still
// over limit it is cut with a note. Text that is not JSON is only cut.
func ShrinkJSON(text string, limit, maxString int) string {
	if len(text) <= limit {
		return text
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return cutText(text, limit)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(clampStrings(v, maxString)); err != nil {
		return cutText(text, limit)
	}
	return cutText(strings.TrimRight(buf.String(), "\n"), limit)
}

func clampStrings(v any, limit int) any {
	switch x := v.(type) {
	case string:
		if len(x) <= limit {
			return x
		}
		head := limit * 2 / 3
		tail := limit - head
		return cutUTF8Head(x, head) + fmt.Sprintf("\n… [%d bytes omitted] …\n", len(x)-head-tail) + cutUTF8Tail(x, tail)
	case []any:
		for i := range x {
			x[i] = clampStrings(x[i], limit)
		}
	case map[string]any:
		for k := range x {
			x[k] = clampStrings(x[k], limit)
		}
	}
	return v
}

func cutUTF8Head(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func cutUTF8Tail(s string, n int) string {
	if n >= len(s) {
		return s
	}
	i := len(s) - n
	for i < len(s) && s[i]&0xC0 == 0x80 {
		i++
	}
	return s[i:]
}

func cutText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := cutUTF8Head(s, limit)
	return cut + fmt.Sprintf("\n[truncated: %d of %d bytes shown; narrow the query, lower limit or page with cursor]", len(cut), len(s))
}

// debugReadTools are the read tools beyond the operator set: each is one GET.
func debugReadTools() []operatorTool {
	get, getByID, list := apiGetPath, getByIDPath, listPath
	// idPath is a GET of /api/<segment>/<id>/<suffix> with the given query arguments.
	idPath := func(prefix, suffix string, q func(map[string]any) (url.Values, error)) func(map[string]any) (apiReq, error) {
		return func(a map[string]any) (apiReq, error) {
			id, err := idArg(a)
			if err != nil {
				return apiReq{}, err
			}
			var v url.Values
			if q != nil {
				if v, err = q(a); err != nil {
					return apiReq{}, err
				}
			}
			return apiReq{method: http.MethodGet, path: prefix + url.PathEscape(id) + suffix, query: v}, nil
		}
	}
	// query reads optional integer, string and boolean arguments into query parameters.
	query := func(ints []intSpec, strs, bools []string) func(map[string]any) (url.Values, error) {
		return func(a map[string]any) (url.Values, error) {
			v := url.Values{}
			for _, s := range ints {
				n, set, err := intArg(a, s.key, s.lo, s.hi)
				if err == nil && !set && s.def != 0 {
					n, set = s.def, true
				}
				if err != nil {
					return nil, err
				}
				if set {
					v.Set(s.key, strconv.FormatInt(n, 10))
				}
			}
			for _, k := range strs {
				s, err := strArg(a, k)
				if err != nil {
					return nil, err
				}
				if s != "" {
					v.Set(k, s)
				}
			}
			for _, k := range bools {
				b, err := boolArg(a, k)
				if err != nil {
					return nil, err
				}
				if b != nil {
					v.Set(k, strconv.FormatBool(*b))
				}
			}
			return v, nil
		}
	}
	logProps := func(what string) map[string]any {
		return map[string]any{
			"id":       idProp(what),
			"after_id": map[string]any{"type": "integer", "minimum": 0, "description": "Only entries with a greater id."},
			"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Entries to return (default 100)."},
			"tail":     prop("boolean", "Return the newest entries instead of the oldest."),
		}
	}
	logQuery := query([]intSpec{{key: "after_id", lo: 0, hi: 1 << 62}, {key: "limit", lo: 1, hi: 200, def: 100}}, nil, []string{"tail"})
	return []operatorTool{
		restTool("approval_get", "Read one approval: what is being asked, its status and the decision. Only the user decides approvals.",
			obj(map[string]any{"id": idProp("approval")}, "id"), nil, getByID("/api/approvals/")),

		restTool("event_get", "Read one event: its payload, the sensor run that produced it and the flow runs it fanned out to.",
			obj(map[string]any{"id": idProp("event")}, "id"), nil, getByID("/api/events/")),

		restTool("node_run_get", "Read one node run: its decision, error, stdin, stdout, stderr, outputs and the agent runs it started.",
			obj(map[string]any{"id": idProp("node run")}, "id"), nil, getByID("/api/node-runs/")),
		restTool("node_run_sandbox_log", "Read the sandbox log of a node run: provisioning, every command and file operation with its result, and denied calls. Secrets are redacted.",
			obj(logProps("node run"), "id"), nil, idPath("/api/node-runs/", "/sandbox-log", logQuery)),

		restTool("agent_runs_list", "List agent runs (flow agent nodes and assist turns), newest first.",
			obj(withListProps(map[string]any{
				"status":            map[string]any{"type": "string", "enum": []string{"queued", "running", "awaiting_approval", "succeeded", "failed", "denied", "killed", "interrupted", "timeout"}, "description": "Run status."},
				"runtime":           prop("string", "Runtime name."),
				"flow_run_id":       idProp("flow run"),
				"node_run_id":       idProp("node run"),
				"assist_session_id": idProp("assist session"),
			})), nil, list("/api/agent-runs", []string{"status", "runtime", "flow_run_id", "node_run_id", "assist_session_id"}, nil)),
		restTool("agent_run_get", "Read one agent run: runtime, mode, status, error, the rendered prompt, the result and the approvals it raised. The transcript is agent_run_events.",
			obj(map[string]any{"id": idProp("agent run")}, "id"), nil, getByID("/api/agent-runs/")),
		restTool("agent_run_events", "Read an agent run's transcript: messages, thoughts and tool calls with their results, in order. Page with after_seq; kind keeps only some kinds (comma-separated, for example tool_call,tool_call_update).",
			obj(map[string]any{
				"id":        idProp("agent run"),
				"after_seq": map[string]any{"type": "integer", "minimum": 0, "description": "Only events with a greater seq."},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Events to return (default 50)."},
				"kind":      prop("string", "Comma-separated event kinds to include."),
			}, "id"), nil, idPath("/api/agent-runs/", "/events", query([]intSpec{{key: "after_seq", lo: 0, hi: 1 << 62}, {key: "limit", lo: 1, hi: 200, def: 50}}, []string{"kind"}, nil))),
		restTool("agent_run_permission_requests", "Read the permission requests of an agent run: each tool call the agent made, how it was decided (policy rule or a user) and why.",
			obj(map[string]any{"id": idProp("agent run")}, "id"), nil, idPath("/api/agent-runs/", "/permission-requests", nil)),
		restTool("agent_run_sandbox_log", "Read the sandbox log of an agent run: provisioning, every shell command and file operation with its result, and denied calls. Secrets are redacted.",
			obj(logProps("agent run"), "id"), nil, idPath("/api/agent-runs/", "/sandbox-log", logQuery)),

		restTool("conversations_list", "List conversations, newest activity first: assistant chats and the agent runs of flow nodes in one list, with forks of runs. status is running, awaiting_approval, idle, succeeded, failed, killed, denied, interrupted, timeout or closed; q searches titles, ids, messages, prompts and results.",
			obj(withListProps(map[string]any{
				"source":      map[string]any{"type": "string", "enum": []string{"flow", "chat"}, "description": "flow: agent runs of flow nodes; chat: assistant chats."},
				"kind":        map[string]any{"type": "string", "enum": []string{"general", "sensor", "flow", "tool", "agent"}, "description": "A chat's target kind, or agent for flow runs."},
				"flow":        idProp("flow"),
				"node":        prop("string", "Node id."),
				"flow_run":    idProp("flow run"),
				"runtime":     prop("string", "Runtime name."),
				"status":      map[string]any{"type": "string", "enum": []string{"running", "awaiting_approval", "idle", "succeeded", "failed", "killed", "denied", "interrupted", "timeout", "closed"}, "description": "Normalised status."},
				"q":           prop("string", "Text to search for (case-insensitive, at most 200 characters)."),
				"forked_from": idProp("agent run"),
				"since":       prop("string", "Only conversations with activity at or after this time (RFC 3339)."),
				"until":       prop("string", "Only conversations with activity before this time (RFC 3339)."),
			})), nil, list("/api/conversations", []string{"source", "kind", "flow", "node", "flow_run", "runtime", "status", "q", "forked_from", "since", "until"}, nil)),
		restTool("conversation_get", "Read one conversation (an assistant chat id or an agent run id) with the chats forked from it.",
			obj(map[string]any{"id": idProp("conversation")}, "id"), nil, getByID("/api/conversations/")),
		restTool("conversation_messages", "Read a conversation's transcript in one shape for chats and flow runs: user, assistant, tool (arguments and results) and system messages, oldest first, at most the newest 2000.",
			obj(map[string]any{"id": idProp("conversation")}, "id"), nil, idPath("/api/conversations/", "/messages", nil)),

		restTool("sensor_runs_list", "List the polls of one sensor, newest first, with status, error and item counts.",
			obj(withListProps(map[string]any{
				"id":     idProp("sensor"),
				"status": map[string]any{"type": "string", "enum": []string{"running", "succeeded", "failed", "timeout", "killed", "interrupted"}, "description": "Poll status."},
			}), "id"), nil, func(a map[string]any) (apiReq, error) {
				id, err := idArg(a)
				if err != nil {
					return apiReq{}, err
				}
				delete(a, "id")
				q, err := listQuery(a, []string{"status"}, nil)
				return apiReq{method: http.MethodGet, path: "/api/sensors/" + url.PathEscape(id) + "/runs", query: q}, err
			}),
		restTool("sensor_run_get", "Read one sensor poll: status, error, exit code, stdout/stderr and the items it produced.",
			obj(map[string]any{"id": idProp("sensor run")}, "id"), nil, getByID("/api/sensor-runs/")),

		restTool("queue_list", "List the flow runs waiting for a slot, in claim order.", obj(map[string]any{}), nil, get("/api/queue")),

		restTool("tools_list", "List the user's custom tools with their definitions.", obj(map[string]any{}), nil, get("/api/tools")),
		restTool("tool_get", "Read one custom tool: definition, effective argument schema and diagnostics.",
			obj(map[string]any{"id": idProp("tool")}, "id"), nil, getByID("/api/tools/")),
		restTool("prompts_list", "List the prompt files agent nodes use.", obj(map[string]any{}), nil, get("/api/prompts")),
		restTool("prompt_get", "Read one prompt file.",
			obj(map[string]any{"id": idProp("prompt name")}, "id"), nil, getByID("/api/prompts/")),
		restTool("runtimes_list", "List the configured agent runtimes and their last probe result.", obj(map[string]any{}), nil, get("/api/runtimes")),
		restTool("sandboxes_list", "List the sandbox backends: enabled, lifecycle, approval policy, validity.", obj(map[string]any{}), nil, get("/api/sandboxes")),
		restTool("sandbox_get", "Read the config of one sandbox backend (local, docker, depot, kubernetes, ssh) with its diagnostics. Credentials are never included.",
			obj(map[string]any{"id": idProp("sandbox backend")}, "id"), nil, getByID("/api/sandboxes/")),
		restTool("settings_get", "Read config.yaml as the daemon sees it: concurrency, approval timeout, retention, runtimes, auth stacks. Secret values are redacted.",
			obj(map[string]any{}), nil, get("/api/settings")),
	}
}

// intSpec describes an optional integer argument that is passed on as a query parameter.
type intSpec struct {
	key    string
	lo, hi int64
	// def is the value used when the argument is absent; 0 sends nothing.
	def int64
}

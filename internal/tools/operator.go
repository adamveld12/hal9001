package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultOperatorApprovalTimeout bounds how long an operator call waits for the user's decision. It is shorter than
// the agent tool approval wait because an external client holds a request open for it, and clients time out: Claude
// Code aborts an HTTP call that is silent for 5 minutes, Codex defaults to 60 s per tool call (see docs/mcp.md).
const DefaultOperatorApprovalTimeout = 4 * time.Minute

// maxOperatorText caps one tool result so a long list does not flood the client's context.
const maxOperatorText = 128 << 10

// APIFunc performs one request against hal's own REST API on behalf of the MCP client that is calling. The daemon
// supplies it per request so the caller's identity (and with it the auth service each endpoint needs) is the one the
// MCP request authenticated with. It returns the status and body of the response.
type APIFunc func(ctx context.Context, method, path string, query url.Values, body []byte) (status int, out []byte, err error)

type apiKey struct{}

// WithAPI returns a context that carries the API an operator tool runs against.
func WithAPI(ctx context.Context, api APIFunc) context.Context {
	return context.WithValue(ctx, apiKey{}, api)
}

func apiFrom(ctx context.Context) (APIFunc, error) {
	api, ok := ctx.Value(apiKey{}).(APIFunc)
	if !ok || api == nil {
		return nil, errors.New("operator tool called without an API")
	}
	return api, nil
}

// OperatorApproval asks the host for a user's decision on one operator call.
type OperatorApproval struct {
	Tool string
	Args json.RawMessage
	// Reason says what the call would do.
	Reason string
	// Caller is the authenticated principal of the MCP request.
	Caller string
	// Timeout is how long to wait; the host returns Decision.Expired when it elapses.
	Timeout time.Duration
}

// OperatorHost is what the daemon supplies to the operator tool set: the approval queue.
type OperatorHost interface {
	// ApproveOperator registers an approval in the Inbox and blocks until it is decided, the timeout passes, or ctx
	// ends (then it returns ctx's error).
	ApproveOperator(ctx context.Context, req OperatorApproval) (Decision, error)
}

// Operator is the tool set hal offers to MCP clients that are not inside an agent run (an editor or agent the user
// runs themselves), over both transports: the daemon's /mcp endpoint serves it, and `hal9001 mcp` without a run token
// bridges to that endpoint. The tools read through hal's REST API with the caller's own identity. The ones with side
// effects (flow_run, sensor_poll) wait for an approval in the Inbox first. Nothing here can decide an approval.
type Operator struct {
	reg     *Registry
	host    OperatorHost
	timeout time.Duration
	// approval holds the tools whose calls wait for an approval.
	approval map[string]bool
}

// NewOperator returns the operator tool set. timeout bounds approval waits (default DefaultOperatorApprovalTimeout).
func NewOperator(host OperatorHost, timeout time.Duration) (*Operator, error) {
	if host == nil {
		return nil, errors.New("tools: OperatorHost is required")
	}
	if timeout <= 0 {
		timeout = DefaultOperatorApprovalTimeout
	}
	reg := NewRegistry()
	o := &Operator{reg: reg, host: host, timeout: timeout, approval: map[string]bool{}}
	for _, t := range operatorTools() {
		if err := reg.Register(t.Tool); err != nil {
			return nil, err
		}
		o.approval[t.Name] = t.NeedsApproval
	}
	return o, nil
}

// Tools lists the operator tools.
func (o *Operator) Tools() []ToolInfo { return o.reg.List() }

// NeedsApproval reports whether a call of the named tool waits for the user's approval.
func (o *Operator) NeedsApproval(name string) bool { return o.approval[name] }

// Timeout is how long a call that needs approval waits for the user.
func (o *Operator) Timeout() time.Duration { return o.timeout }

// Call runs one tool. A tool with side effects first waits for the user's approval; the returned error is a tool
// error for the client (ErrUnknownTool, *DeniedError, the API's problem text or the tool's own error).
func (o *Operator) Call(ctx context.Context, api APIFunc, caller, name string, args json.RawMessage) (Result, error) {
	t, ok := o.reg.Get(name)
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	if len(args) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage(`{}`)
	}
	verdict, why := approval(t, args)
	switch verdict {
	case VerdictDeny:
		return Result{}, fmt.Errorf("tool %s: %s", name, why)
	case VerdictApprove:
		d, err := o.host.ApproveOperator(ctx, OperatorApproval{Tool: name, Args: args, Reason: why, Caller: caller, Timeout: o.timeout})
		if err != nil {
			return Result{}, fmt.Errorf("tool %s: approval failed: %w", name, err)
		}
		if !d.Allowed {
			return Result{}, &DeniedError{Tool: name, Expired: d.Expired, Cancelled: d.Cancelled, Comment: d.Comment}
		}
	}
	return t.Run(WithAPI(ctx, api), Call{Name: name, Args: args, Now: time.Now()})
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// apiReq is the REST request one tool call maps to.
type apiReq struct {
	method, path string
	query        url.Values
	body         any
}

// operatorTool is a Tool of the operator set and whether a call waits for the user's approval.
type operatorTool struct {
	Tool
	NeedsApproval bool
}

// restTool builds a tool that maps its arguments to one REST request. build validates the arguments; it is also the
// tool's Policy, so invalid arguments are refused before any approval is asked for. need, when set, makes the call
// wait for approval and describes it.
func restTool(name, desc string, schema map[string]any, need func(a map[string]any) string, build func(a map[string]any) (apiReq, error)) operatorTool {
	parse := func(raw json.RawMessage) (map[string]any, error) {
		var a map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&a); err != nil {
			return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
		}
		props, _ := schema["properties"].(map[string]any)
		for k := range a {
			if _, known := props[k]; !known {
				return nil, fmt.Errorf("unknown argument %q", k)
			}
		}
		return a, nil
	}
	return operatorTool{NeedsApproval: need != nil, Tool: Tool{
		Name: name, Description: desc, InputSchema: schema,
		Policy: func(raw json.RawMessage) (Verdict, string) {
			a, err := parse(raw)
			if err == nil {
				_, err = build(a)
			}
			if err != nil {
				return VerdictDeny, err.Error()
			}
			if need != nil {
				return VerdictApprove, need(a)
			}
			return VerdictAllow, ""
		},
		Run: func(ctx context.Context, c Call) (Result, error) {
			api, err := apiFrom(ctx)
			if err != nil {
				return Result{}, err
			}
			a, err := parse(c.Args)
			if err != nil {
				return Result{}, err
			}
			rq, err := build(a)
			if err != nil {
				return Result{}, err
			}
			var body []byte
			if rq.body != nil {
				if body, err = json.Marshal(rq.body); err != nil {
					return Result{}, err
				}
			}
			status, out, err := api(ctx, rq.method, rq.path, rq.query, body)
			if err != nil {
				return Result{}, err
			}
			if status < 200 || status > 299 {
				return Result{}, errors.New(problemText(status, out))
			}
			return Result{Text: truncateText(string(out))}, nil
		},
	}}
}

// problemText is the message of an RFC 9457 problem body, or the status when the body is not one.
func problemText(status int, body []byte) string {
	var p struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &p) == nil && (p.Detail != "" || p.Title != "") {
		msg := p.Detail
		if msg == "" {
			msg = p.Title
		}
		return fmt.Sprintf("hal9001: %d %s: %s", status, http.StatusText(status), msg)
	}
	return fmt.Sprintf("hal9001: %d %s", status, http.StatusText(status))
}

func truncateText(s string) string {
	if len(s) <= maxOperatorText {
		return s
	}
	cut := maxOperatorText
	for cut > 0 && s[cut]&0xC0 == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n[truncated: %d of %d bytes shown; narrow the query or use limit]", cut, len(s))
}

// ---- argument helpers ----

func strArg(a map[string]any, key string) (string, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return s, nil
}

// idArg reads the required "id" argument and checks that it is safe to put in a URL path.
func idArg(a map[string]any) (string, error) {
	s, err := strArg(a, "id")
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", errors.New("id is required")
	}
	if !idRE.MatchString(s) || s == "." || s == ".." {
		return "", fmt.Errorf("id %q is not a valid id", s)
	}
	return s, nil
}

func boolArg(a map[string]any, key string) (*bool, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return nil, nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("%s must be a boolean", key)
	}
	return &b, nil
}

func limitArg(a map[string]any) (string, error) {
	v, ok := a["limit"]
	if !ok || v == nil {
		return "20", nil
	}
	n, ok := v.(json.Number)
	if !ok {
		return "", errors.New("limit must be an integer")
	}
	i, err := n.Int64()
	if err != nil || i < 1 || i > 200 {
		return "", errors.New("limit must be an integer from 1 to 200")
	}
	return strconv.FormatInt(i, 10), nil
}

// listQuery collects the common list arguments: string filters named in strs (sent as given), booleans in bools
// (sent as true/false), the limit and the cursor.
func listQuery(a map[string]any, strs, bools []string) (url.Values, error) {
	q := url.Values{}
	for _, k := range strs {
		s, err := strArg(a, k)
		if err != nil {
			return nil, err
		}
		if s != "" {
			q.Set(k, s)
		}
	}
	for _, k := range bools {
		b, err := boolArg(a, k)
		if err != nil {
			return nil, err
		}
		if b != nil {
			q.Set(k, strconv.FormatBool(*b))
		}
	}
	lim, err := limitArg(a)
	if err != nil {
		return nil, err
	}
	q.Set("limit", lim)
	cur, err := strArg(a, "cursor")
	if err != nil {
		return nil, err
	}
	if cur != "" {
		q.Set("cursor", cur)
	}
	return q, nil
}

var (
	limitProp  = map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Page size (default 20)."}
	cursorProp = map[string]any{"type": "string", "description": "Cursor from the previous page's next_cursor."}
)

func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func idProp(what string) map[string]any { return prop("string", "The "+what+" id.") }

func operatorTools() []operatorTool {
	get := func(path string) func(map[string]any) (apiReq, error) {
		return func(map[string]any) (apiReq, error) { return apiReq{method: http.MethodGet, path: path}, nil }
	}
	getByID := func(prefix string) func(map[string]any) (apiReq, error) {
		return func(a map[string]any) (apiReq, error) {
			id, err := idArg(a)
			if err != nil {
				return apiReq{}, err
			}
			return apiReq{method: http.MethodGet, path: prefix + url.PathEscape(id)}, nil
		}
	}
	list := func(path string, strs, bools []string) func(map[string]any) (apiReq, error) {
		return func(a map[string]any) (apiReq, error) {
			q, err := listQuery(a, strs, bools)
			return apiReq{method: http.MethodGet, path: path, query: q}, err
		}
	}
	withListProps := func(p map[string]any) map[string]any {
		p["limit"], p["cursor"] = limitProp, cursorProp
		return p
	}

	return []operatorTool{
		restTool("status", "hal9001 header counters: pending approvals, unread inbox items, queue depth and the kill switch.",
			obj(map[string]any{}), nil, get("/api/status")),

		restTool("inbox_list", "List inbox notes (the structured results flows and agents leave for the user), most important first. Archived notes are hidden unless archived is true or any.",
			obj(withListProps(map[string]any{
				"q":        prop("string", "Case-insensitive substring search over title and summary."),
				"priority": map[string]any{"type": "string", "enum": []string{"low", "normal", "high", "urgent"}, "description": "Minimum priority."},
				"flow_id":  idProp("flow"),
				"read":     prop("boolean", "Only read (true) or unread (false) items."),
				"starred":  prop("boolean", "Only starred (true) or unstarred (false) items."),
				"archived": map[string]any{"type": "string", "enum": []string{"true", "false", "any"}, "description": "Archived filter (default false)."},
			})), nil, list("/api/inbox", []string{"q", "priority", "flow_id", "archived"}, []string{"read", "starred"})),
		restTool("inbox_get", "Read one inbox note in full: summary, suggested reply, links and its run lineage.",
			obj(map[string]any{"id": idProp("inbox item")}, "id"), nil, getByID("/api/inbox/")),

		restTool("approvals_list", "List approvals: the pending queue (default) or history. Only the user decides approvals; there is no tool to approve or deny.",
			obj(withListProps(map[string]any{
				"status": prop("string", "Comma-separated statuses: pending, approved, denied, expired (default pending)."),
			})), nil, func(a map[string]any) (apiReq, error) {
				if s, _ := a["status"].(string); s == "" {
					a["status"] = "pending"
				}
				q, err := listQuery(a, []string{"status"}, nil)
				return apiReq{method: http.MethodGet, path: "/api/approvals", query: q}, err
			}),
		restTool("approval_get", "Read one approval: what is being asked, its status and the decision.",
			obj(map[string]any{"id": idProp("approval")}, "id"), nil, getByID("/api/approvals/")),

		restTool("flows_list", "List flows with run statistics.", obj(map[string]any{}), nil, get("/api/flows")),
		restTool("flow_get", "Read one flow: its definition, validation diagnostics and subscribed sensors.",
			obj(map[string]any{"id": idProp("flow")}, "id"), nil, getByID("/api/flows/")),
		restTool("flow_runs_list", "List flow runs, newest first.",
			obj(withListProps(map[string]any{
				"flow_id": idProp("flow"),
				"status":  map[string]any{"type": "string", "enum": []string{"queued", "running", "awaiting_approval", "succeeded", "failed", "killed", "interrupted", "cancelled"}, "description": "Run status."},
			})), nil, list("/api/flow-runs", []string{"flow_id", "status"}, nil)),
		restTool("flow_run_get", "Read one flow run with its node runs. With wait (for example 30s, at most 5m) it waits until the run finishes or needs an approval.",
			obj(map[string]any{"id": idProp("flow run"), "wait": prop("string", "Long-poll duration such as 30s.")}, "id"),
			nil, func(a map[string]any) (apiReq, error) {
				id, err := idArg(a)
				if err != nil {
					return apiReq{}, err
				}
				q := url.Values{}
				w, err := strArg(a, "wait")
				if err != nil {
					return apiReq{}, err
				}
				if w != "" {
					q.Set("wait", w)
				}
				return apiReq{method: http.MethodGet, path: "/api/flow-runs/" + url.PathEscape(id), query: q}, nil
			}),

		restTool("sensors_list", "List sensors with their last poll status.", obj(map[string]any{}), nil, get("/api/sensors")),
		restTool("sensor_get", "Read one sensor: its definition and state.",
			obj(map[string]any{"id": idProp("sensor")}, "id"), nil, getByID("/api/sensors/")),
		restTool("events_list", "List events (items sensors produced) with the flow runs they started, newest first.",
			obj(withListProps(map[string]any{
				"sensor_id": idProp("sensor"),
				"flow_id":   idProp("flow"),
				"q":         prop("string", "Case-insensitive substring search over the payload."),
			})), nil, list("/api/events", []string{"sensor_id", "flow_id", "q"}, nil)),

		restTool("flow_run", "Run a flow now: polls the sensors it subscribes to and starts runs of this flow only. Has side effects, so it waits for the user to approve it in the hal9001 Inbox (up to 4 minutes). Returns the polls and the flow run ids; follow them with flow_run_get.",
			obj(map[string]any{
				"id":         idProp("flow"),
				"rerun_seen": prop("boolean", "Also replay items already seen into the flow."),
			}, "id"),
			func(a map[string]any) string {
				id, _ := a["id"].(string)
				s := fmt.Sprintf("Run the flow %q now: poll its sensors and start runs of it.", id)
				if b, _ := a["rerun_seen"].(bool); b {
					s += " Items already seen are replayed too."
				}
				return s
			},
			func(a map[string]any) (apiReq, error) {
				id, err := idArg(a)
				if err != nil {
					return apiReq{}, err
				}
				rerun, err := boolArg(a, "rerun_seen")
				if err != nil {
					return apiReq{}, err
				}
				body := map[string]any{}
				if rerun != nil {
					body["rerun_seen"] = *rerun
				}
				return apiReq{method: http.MethodPost, path: "/api/flows/" + url.PathEscape(id) + "/run", body: body}, nil
			}),
		restTool("sensor_poll", "Poll a sensor now, outside its schedule. New items become events and start runs of the flows subscribed to it. Has side effects, so it waits for the user to approve it in the hal9001 Inbox (up to 4 minutes).",
			obj(map[string]any{
				"id":         idProp("sensor"),
				"rerun_seen": prop("boolean", "Also replay items already seen into the target flows."),
				"flows":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Only send events to these flow ids (default: every enabled subscribed flow)."},
			}, "id"),
			func(a map[string]any) string {
				id, _ := a["id"].(string)
				s := fmt.Sprintf("Poll the sensor %q now; new items start runs of the flows subscribed to it.", id)
				if fl, _ := a["flows"].([]any); len(fl) > 0 {
					names := make([]string, 0, len(fl))
					for _, f := range fl {
						names = append(names, fmt.Sprint(f))
					}
					s += " Only for: " + strings.Join(names, ", ") + "."
				}
				if b, _ := a["rerun_seen"].(bool); b {
					s += " Items already seen are replayed too."
				}
				return s
			},
			func(a map[string]any) (apiReq, error) {
				id, err := idArg(a)
				if err != nil {
					return apiReq{}, err
				}
				body := map[string]any{}
				rerun, err := boolArg(a, "rerun_seen")
				if err != nil {
					return apiReq{}, err
				}
				if rerun != nil {
					body["rerun_seen"] = *rerun
				}
				if v, ok := a["flows"]; ok && v != nil {
					arr, ok := v.([]any)
					if !ok {
						return apiReq{}, errors.New("flows must be an array of flow ids")
					}
					flows := make([]string, 0, len(arr))
					for _, f := range arr {
						s, ok := f.(string)
						if !ok || !idRE.MatchString(s) {
							return apiReq{}, fmt.Errorf("flows: %v is not a valid flow id", f)
						}
						flows = append(flows, s)
					}
					body["flows"] = flows
				}
				return apiReq{method: http.MethodPost, path: "/api/sensors/" + url.PathEscape(id) + "/poll", body: body}, nil
			}),
	}
}

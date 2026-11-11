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

// Limits of request_approval (see operator_ask.go): how long one call waits, and how long the approval stays pending.
const (
	// MaxRequestWait is the longest a request_approval call blocks. It is also its default: the existing operator
	// wait limit, which is under the 5 minutes a client allows an HTTP call to stay silent.
	MaxRequestWait = DefaultOperatorApprovalTimeout
	// DefaultRequestTimeout is how long a request_approval stays pending when the agent gives no timeout.
	DefaultRequestTimeout = 24 * time.Hour
	// MinRequestTimeout and MaxRequestTimeout bound the agent's timeout_seconds.
	MinRequestTimeout = 10 * time.Second
	MaxRequestTimeout = 7 * 24 * time.Hour
	// MaxPendingRequests is how many request_approval approvals may be pending at once.
	MaxPendingRequests = 10
)

// maxOperatorText caps one tool result so a long list does not flood the client's context.
const maxOperatorText = 128 << 10

// APIFunc performs one request against hal's own REST API on behalf of the MCP client that is calling. The daemon
// supplies it per request so the caller's identity (and with it the auth service each endpoint needs) is the one the
// MCP request authenticated with. It returns the status and body of the response.
type APIFunc func(ctx context.Context, method, path string, query url.Values, body []byte) (status int, out []byte, err error)

// OperatorCaller identifies the MCP client behind one call.
type OperatorCaller struct {
	// Principal is the authenticated principal of the MCP request.
	Principal string
	// Client is the client's name: the clientInfo of its initialize request, else its User-Agent. Empty when unknown.
	Client string
}

// Label says who asked, for approval text: "Claude Code", or "an MCP client" when the name is not known.
func (c OperatorCaller) Label() string {
	if c.Client != "" {
		return c.Client
	}
	return "an MCP client"
}

// RequestedBy is the principal and client of a call as one line.
func (c OperatorCaller) RequestedBy() string {
	switch {
	case c.Principal != "" && c.Client != "":
		return c.Principal + " via " + c.Client
	case c.Client != "":
		return c.Client
	case c.Principal != "":
		return c.Principal
	}
	return "an MCP client"
}

// ServiceAdmin is the auth service of the endpoints that change state. A tool that needs it is refused, before
// anybody is asked, when the MCP request is not admitted for it.
const ServiceAdmin = "admin"

// OperatorEnv is what the daemon supplies for one MCP call: the caller's REST API and identity.
type OperatorEnv struct {
	API    APIFunc
	Caller OperatorCaller
	// Authorize reports whether the MCP request is admitted for an auth service ("admin"); the error is the
	// refusal. Nil admits everything.
	Authorize func(ctx context.Context, service string) error
}

type envKey struct{}

func withEnv(ctx context.Context, env OperatorEnv) context.Context {
	return context.WithValue(ctx, envKey{}, env)
}

func envFrom(ctx context.Context) (OperatorEnv, error) {
	env, ok := ctx.Value(envKey{}).(OperatorEnv)
	if !ok || env.API == nil {
		return OperatorEnv{}, errors.New("operator tool called without an API")
	}
	return env, nil
}

func apiFrom(ctx context.Context) (APIFunc, error) {
	env, err := envFrom(ctx)
	return env.API, err
}

// OperatorField is one row of the key/value table of an operator approval.
type OperatorField struct {
	Key, Value string
}

// OperatorApproval asks the host for a user's decision on one operator call.
type OperatorApproval struct {
	Tool string
	Args json.RawMessage
	// Reason says what the call would do.
	Reason string
	// Title, BodyMD and Fields replace the generic "MCP client: <tool>" approval content when set (flow_create shows
	// the spec, for example). The host adds the caller's identity.
	Title string
	// Description is hal's one- or two-sentence text for the call, generated from its facts (never empty).
	Description string
	BodyMD      string
	Fields      []OperatorField
	// Caller identifies the MCP client.
	Caller OperatorCaller
	// Timeout is how long to wait; the host returns Decision.Expired when it elapses.
	Timeout time.Duration
}

// OperatorEditable is one field the approver may edit before approving a request_approval, with its current value
// (JSON).
type OperatorEditable struct {
	Key   string
	Value json.RawMessage
}

// OperatorQuestion is a request_approval call: the agent asks the user for a decision.
type OperatorQuestion struct {
	Title string
	// Description is what the agent wants to do and why, in its own words; required.
	Description string
	BodyMD      string
	Priority    string
	Editable    []OperatorEditable
	// Timeout is how long the approval stays pending before it expires.
	Timeout time.Duration
	// Wait is how long the call blocks for the decision.
	Wait   time.Duration
	Caller OperatorCaller
}

// OperatorNote is a send_note call.
type OperatorNote struct {
	Title    string
	BodyMD   string
	Priority string
	// URL and URLLabel become the note's link.
	URL, URLLabel string
	// Thread groups the note's notifications (a new notification of a thread replaces its previous one).
	Thread string
	Caller OperatorCaller
}

// OperatorNoted is the note a send_note call created.
type OperatorNoted struct {
	ItemID string
	// URL is the note's deep link in the web UI.
	URL string
}

// OperatorHost is what the daemon supplies to the operator tool set: the approval queue and the inbox.
type OperatorHost interface {
	// ApproveOperator registers an approval in the Inbox and blocks until it is decided, the timeout passes, or ctx
	// ends (then it returns ctx's error). The approval belongs to this call: it expires when the call ends.
	ApproveOperator(ctx context.Context, req OperatorApproval) (Decision, error)
	// AskOperator registers a request approval in the Inbox that outlives the call (it is decided, and expires, on
	// its own) and blocks until it is decided, its timeout passes, the wait elapses, or ctx ends. It returns the
	// approval's id; the caller reads the outcome from the approval.
	AskOperator(ctx context.Context, q OperatorQuestion) (approvalID string, err error)
	// PostOperatorNote creates an inbox note and raises its notification.
	PostOperatorNote(ctx context.Context, n OperatorNote) (OperatorNoted, error)
}

// Operator is the tool set hal offers to MCP clients that are not inside an agent run (an editor or agent the user
// runs themselves), over both transports: the daemon's /mcp endpoint serves it, and `hal9001 mcp` without a run token
// bridges to that endpoint. The tools read through hal's REST API with the caller's own identity. The ones with side
// effects (flow_run, sensor_poll, flow_create, sensor_create) wait for an approval in the Inbox first;
// request_approval is an approval the agent asks for, and send_note posts an inbox note. Nothing here can decide an
// approval.
type Operator struct {
	reg     *Registry
	host    OperatorHost
	timeout time.Duration
	// approval holds the tools whose calls wait for an approval; service the auth service a tool needs besides api.
	approval map[string]bool
	service  map[string]string
	notes    noteLimiter
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
	o := &Operator{reg: reg, host: host, timeout: timeout, approval: map[string]bool{}, service: map[string]string{}}
	for _, t := range append(operatorTools(), o.hostTools()...) {
		if err := reg.Register(t.Tool); err != nil {
			return nil, err
		}
		o.approval[t.Name] = t.NeedsApproval
		o.service[t.Name] = t.Service
	}
	return o, nil
}

// Tools lists the operator tools.
func (o *Operator) Tools() []ToolInfo { return o.reg.List() }

// NeedsApproval reports whether a call of the named tool waits for the user's approval.
func (o *Operator) NeedsApproval(name string) bool { return o.approval[name] }

// Service is the auth service the named tool needs besides api (ServiceAdmin), or "" when api is enough.
func (o *Operator) Service(name string) string { return o.service[name] }

// Timeout is how long a call that needs approval waits for the user.
func (o *Operator) Timeout() time.Duration { return o.timeout }

// Call runs one tool. Arguments are checked first, then the caller's auth service for the tool, so a refused call
// asks nobody. A tool with side effects then waits for the user's approval; the returned error is a tool error for
// the client (ErrUnknownTool, *DeniedError, the API's problem text or the tool's own error).
func (o *Operator) Call(ctx context.Context, env OperatorEnv, name string, args json.RawMessage) (Result, error) {
	t, ok := o.reg.Get(name)
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	if len(args) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage(`{}`)
	}
	verdict, why := approval(t, args)
	if verdict == VerdictDeny {
		return Result{}, fmt.Errorf("tool %s: %s", name, why)
	}
	if svc := o.service[name]; svc != "" && env.Authorize != nil {
		if err := env.Authorize(ctx, svc); err != nil {
			return Result{}, fmt.Errorf("tool %s: %w", name, err)
		}
	}
	if verdict == VerdictApprove {
		text := DescribeCall(t, args, why)
		title := ""
		if t.Describe != nil {
			title = text.Title
		}
		d, err := o.host.ApproveOperator(ctx, OperatorApproval{Tool: name, Args: args, Reason: why, Title: title, Description: text.Description, Caller: env.Caller, Timeout: o.timeout})
		if err != nil {
			return Result{}, fmt.Errorf("tool %s: approval failed: %w", name, err)
		}
		if !d.Allowed {
			return Result{}, &DeniedError{Tool: name, Expired: d.Expired, Cancelled: d.Cancelled, Held: d.Held, Comment: d.Comment}
		}
	}
	res, err := t.Run(withEnv(ctx, env), Call{Name: name, Args: args, Now: time.Now()})
	res.Text = truncateText(res.Text) // restTool leaves the cap to the caller: ReadTools shrinks JSON instead
	return res, err
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// apiReq is the REST request one tool call maps to.
type apiReq struct {
	method, path string
	query        url.Values
	body         any
}

// operatorTool is a Tool of the operator set, whether a call waits for the user's approval, and the auth service it
// needs besides api.
type operatorTool struct {
	Tool
	NeedsApproval bool
	Service       string
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
			return Result{Text: string(out)}, nil
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

func adminTool(t operatorTool) operatorTool {
	t.Service = ServiceAdmin
	return t
}

// apiGetPath maps a tool without arguments to GET path.
func apiGetPath(path string) func(map[string]any) (apiReq, error) {
	return func(map[string]any) (apiReq, error) { return apiReq{method: http.MethodGet, path: path}, nil }
}

// getByIDPath maps the tool's id argument to GET prefix+id.
func getByIDPath(prefix string) func(map[string]any) (apiReq, error) {
	return func(a map[string]any) (apiReq, error) {
		id, err := idArg(a)
		if err != nil {
			return apiReq{}, err
		}
		return apiReq{method: http.MethodGet, path: prefix + url.PathEscape(id)}, nil
	}
}

// listPath maps the tool's filters, limit and cursor to GET path.
func listPath(path string, strs, bools []string) func(map[string]any) (apiReq, error) {
	return func(a map[string]any) (apiReq, error) {
		q, err := listQuery(a, strs, bools)
		return apiReq{method: http.MethodGet, path: path, query: q}, err
	}
}

func withListProps(p map[string]any) map[string]any {
	p["limit"], p["cursor"] = limitProp, cursorProp
	return p
}

var (
	flowRunStatuses = []string{"queued", "running", "awaiting_approval", "succeeded", "failed", "killed", "interrupted", "cancelled"}
	flowRunKinds    = []string{"live", "replay", "test", "continuation"}
	nodeDecisions   = []string{"emitted", "dropped", "error", "skipped"}
	sinceProp       = prop("string", "Only records created at or after this time (RFC 3339).")
	untilProp       = prop("string", "Only records created before this time (RFC 3339).")
)

func operatorTools() []operatorTool {
	return append(operatorReadTools(true), operatorWriteTools()...)
}

// operatorReadTools are the read-only tools, each one GET of hal's REST API. ReadTools (the assist session's set)
// reuses them; withWait offers flow_run_get's long poll, which the assist set leaves out so a call never holds a
// turn open.
func operatorReadTools(withWait bool) []operatorTool {
	get, getByID, list := apiGetPath, getByIDPath, listPath

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

		restTool("flows_list", "List flows with run statistics.", obj(map[string]any{}), nil, get("/api/flows")),
		restTool("flow_get", "Read one flow: its definition, validation diagnostics and subscribed sensors.",
			obj(map[string]any{"id": idProp("flow")}, "id"), nil, getByID("/api/flows/")),
		restTool("flow_runs_list", "List flow runs, newest first. Test runs are hidden unless kind is test.",
			obj(withListProps(map[string]any{
				"flow_id":         idProp("flow"),
				"event_id":        idProp("event"),
				"status":          map[string]any{"type": "string", "enum": flowRunStatuses, "description": "Run status."},
				"kind":            map[string]any{"type": "string", "enum": flowRunKinds, "description": "Run kind (default all but test)."},
				"continuation_of": idProp("flow run"),
				"conversation":    idProp("conversation"),
				"since":           sinceProp,
				"until":           untilProp,
			})), nil, list("/api/flow-runs", []string{"flow_id", "event_id", "status", "kind", "continuation_of", "conversation", "since", "until"}, nil)),
		flowRunGetTool(withWait),

		restTool("sensors_list", "List sensors with their last poll status.", obj(map[string]any{}), nil, get("/api/sensors")),
		restTool("sensor_get", "Read one sensor: its definition and state.",
			obj(map[string]any{"id": idProp("sensor")}, "id"), nil, getByID("/api/sensors/")),
		restTool("events_list", "List events (items sensors produced) with the flow runs they started, newest first.",
			obj(withListProps(map[string]any{
				"sensor_id": idProp("sensor"),
				"flow_id":   idProp("flow"),
				"status":    map[string]any{"type": "string", "enum": flowRunStatuses, "description": "Keep events with a flow run of this status."},
				"decision":  map[string]any{"type": "string", "enum": nodeDecisions, "description": "Keep events with a flow run where a node decided this."},
				"kind":      map[string]any{"type": "string", "enum": flowRunKinds, "description": "Flow run kind (default live, replay and continuation)."},
				"q":         prop("string", "Case-insensitive substring search over the payload."),
				"since":     sinceProp,
				"until":     untilProp,
			})), nil, list("/api/events", []string{"sensor_id", "flow_id", "status", "decision", "kind", "q", "since", "until"}, nil)),
	}
}

// flowRunGetTool is flow_run_get; with withWait it takes a long-poll duration.
func flowRunGetTool(withWait bool) operatorTool {
	desc := "Read one flow run with its node runs."
	props := map[string]any{"id": idProp("flow run")}
	if withWait {
		desc += " With wait (for example 30s, at most 5m) it waits until the run finishes or needs an approval."
		props["wait"] = prop("string", "Long-poll duration such as 30s.")
	}
	return restTool("flow_run_get", desc, obj(props, "id"), nil, func(a map[string]any) (apiReq, error) {
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
	})
}

// operatorWriteTools are the tools with side effects: each waits for the user's approval.
func operatorWriteTools() []operatorTool {
	return []operatorTool{

		adminTool(restTool("flow_run", "Run a flow now: polls the sensors it subscribes to and starts runs of this flow only. Has side effects, so it waits for the user to approve it in the hal9001 Inbox (up to 4 minutes). Returns the polls and the flow run ids; follow them with flow_run_get.",
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
			})),
		adminTool(restTool("sensor_poll", "Poll a sensor now, outside its schedule. New items become events and start runs of the flows subscribed to it. Has side effects, so it waits for the user to approve it in the hal9001 Inbox (up to 4 minutes).",
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
			})),
	}
}

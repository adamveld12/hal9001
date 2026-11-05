package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// query builds a url.Values from name/value pairs, skipping empty values.
func query(pairs ...string) url.Values {
	q := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			q.Set(pairs[i], pairs[i+1])
		}
	}
	return q
}

// emit prints a response body in the selected format.
func (c *call) emit(resp *response, cols ...string) error {
	if resp.Status == http.StatusNoContent {
		return nil
	}
	return render(c.stdout(), resp.Body, c.table, cols)
}

// doEmit sends a request and prints the response.
func (c *call) doEmit(ctx context.Context, method, path string, q url.Values, body []byte, cols ...string) error {
	resp, err := c.client().do(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	return c.emit(resp, cols...)
}

// jsonBody marshals v for a request body.
func jsonBody(v any) ([]byte, error) { return json.Marshal(v) }

func esc(s string) string { return url.PathEscape(s) }

// maxInput bounds how much JSON or YAML the CLI reads from a file or stdin.
const maxInput = 32 << 20

// readInput reads JSON/text from a file ("-" or empty means stdin).
func (c *call) readInput(name string) ([]byte, error) {
	if name == "" || name == "-" {
		return io.ReadAll(io.LimitReader(c.env.Stdin, maxInput))
	}
	f, err := os.Open(name) //nolint:gosec // the user names the file on the command line
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxInput))
}

// listFlags registers --limit and --cursor.
type listFlags struct {
	limit  int
	cursor string
}

func (l *listFlags) register(fs *flag.FlagSet) {
	fs.IntVar(&l.limit, "limit", 0, "maximum items to return")
	fs.StringVar(&l.cursor, "cursor", "", "pagination cursor from a previous response")
}

func (l *listFlags) apply(q url.Values) url.Values {
	if l.limit > 0 {
		q.Set("limit", strconv.Itoa(l.limit))
	}
	if l.cursor != "" {
		q.Set("cursor", l.cursor)
	}
	return q
}

func group(name, summary string, subs ...*command) *command {
	return &command{name: name, summary: summary, subs: subs}
}

func cmd(name, summary string, l *leaf) *command {
	return &command{name: name, summary: summary, leaf: l}
}

// tree builds the command tree.
func (a *app) tree() *command {
	root := &command{name: "hal9001", subs: []*command{
		cmd("serve", "run the daemon in the foreground", serveLeaf()),
		cmd("install", "install and start the launchd LaunchAgent", installLeaf()),
		cmd("uninstall", "stop and remove the launchd LaunchAgent", uninstallLeaf()),
		cmd("open", "open the web UI in the browser", openLeaf()),
		cmd("validate", "check sensors, flows, prompts and config offline", validateLeaf()),
		cmd("version", "print the version", versionLeaf()),
		group("auth", "authentication helpers",
			cmd("set-password", "set the password for the password auth module", setPasswordLeaf())),
		group("tailscale", "Tailscale exposure",
			cmd("status", "report the Tailscale and allowed_hosts setup", apiGet("/api/auth/tailscale"))),
		cmd("status", "daemon counters and kill switch state", apiGet("/api/status")),
		group("sensors", "list, inspect and poll sensors", sensorCmds()...),
		group("events", "list, inject and replay events", eventCmds()...),
		group("flows", "list, validate and test flows", flowCmds()...),
		group("runs", "list and inspect flow runs", runCmds()...),
		group("node-runs", "inspect node runs", nodeRunCmds()...),
		group("agent-runs", "list and inspect agent runs and transcripts", agentRunCmds()...),
		group("approvals", "list, approve and deny approvals", approvalCmds()...),
		group("inbox", "list items; mark read, starred, archived", inboxCmds()...),
		group("killswitch", "pause and resume the daemon", killswitchCmds()...),
		group("runtimes", "list and probe ACP runtimes", runtimeCmds()...),
		group("settings", "show settings", cmd("get", "show config.yaml as JSON (secrets redacted)", apiGet("/api/settings"))),
		group("env", "list, set and unset env variables", envCmds(false)...),
	}}
	return root
}

// apiGet is a leaf that GETs a fixed path.
func apiGet(path string, cols ...string) *leaf {
	return &leaf{maxArgs: 0, run: func(ctx context.Context, c *call) error {
		return c.doEmit(ctx, http.MethodGet, path, nil, nil, cols...)
	}}
}

// idGet is a leaf that GETs a path with one escaped id argument.
func idGet(format string) *leaf {
	return &leaf{usage: "<id>", minArgs: 1, maxArgs: 1, run: func(ctx context.Context, c *call) error {
		return c.doEmit(ctx, http.MethodGet, fmt.Sprintf(format, esc(c.args[0])), nil, nil)
	}}
}

func sensorCmds() []*command {
	return []*command{
		cmd("list", "list sensors", apiGet("/api/sensors", "id", "status.state", "status.last_success_at", "status.new_items_24h")),
		cmd("get", "show one sensor", idGet("/api/sensors/%s")),
		cmd("pause", "stop scheduled polls of a sensor (poll now and inject still work)", &leaf{usage: "<id>", minArgs: 1, maxArgs: 1, run: func(ctx context.Context, c *call) error {
			return c.doEmit(ctx, http.MethodPost, "/api/sensors/"+esc(c.args[0])+"/pause", nil, nil)
		}}),
		cmd("resume", "resume scheduled polls of a paused sensor", &leaf{usage: "<id>", minArgs: 1, maxArgs: 1, run: func(ctx context.Context, c *call) error {
			return c.doEmit(ctx, http.MethodPost, "/api/sensors/"+esc(c.args[0])+"/resume", nil, nil)
		}}),
		group("env", "list, set and unset a sensor's own env variables (secrets, write-only)", envCmds(true)...),
		cmd("poll", "poll a sensor now", &leaf{usage: "<id>", minArgs: 1, maxArgs: 1, run: func(ctx context.Context, c *call) error {
			return c.doEmit(ctx, http.MethodPost, "/api/sensors/"+esc(c.args[0])+"/poll", nil, nil)
		}}),
	}
}

func eventCmds() []*command {
	var lf listFlags
	var sensor, flow, status, decision, kind, q, since, until string
	list := &leaf{
		flags: func(fs *flag.FlagSet) {
			lf.register(fs)
			fs.StringVar(&sensor, "sensor", "", "filter by sensor id")
			fs.StringVar(&flow, "flow", "", "filter by flow id")
			fs.StringVar(&status, "status", "", "filter by flow run status")
			fs.StringVar(&decision, "decision", "", "filter by node decision")
			fs.StringVar(&kind, "kind", "", "filter by flow run kind")
			fs.StringVar(&q, "q", "", "search payload text")
			fs.StringVar(&since, "since", "", "only events at or after this RFC 3339 time")
			fs.StringVar(&until, "until", "", "only events before this RFC 3339 time")
		},
		run: func(ctx context.Context, c *call) error {
			qv := lf.apply(query("sensor_id", sensor, "flow_id", flow, "status", status, "decision", decision,
				"kind", kind, "q", q, "since", since, "until", until))
			return c.doEmit(ctx, http.MethodGet, "/api/events", qv, nil, "id", "sensor_id", "dedup_key", "created_at")
		},
	}
	var file string
	inject := &leaf{
		usage: "<sensor-id> [--file item.json]", minArgs: 1, maxArgs: 1,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&file, "file", "", "read the item JSON from this file (default: stdin)")
		},
		run: func(ctx context.Context, c *call) error {
			body, err := c.readInput(file)
			if err != nil {
				return err
			}
			if !json.Valid(body) {
				return usagef("events inject: input is not valid JSON")
			}
			return c.doEmit(ctx, http.MethodPost, "/api/events", query("sensor_id", c.args[0]), body)
		},
	}
	var replayFlow string
	var replayDry bool
	replay := &leaf{
		usage: "<event-id> --flow <flow-id> [--dry-run=true|false]", minArgs: 1, maxArgs: 1,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&replayFlow, "flow", "", "flow to replay the event into (required)")
			fs.BoolVar(&replayDry, "dry-run", false, "override the flow's dry_run for this replay (omit to keep the flow's setting)")
		},
		run: func(ctx context.Context, c *call) error {
			if replayFlow == "" {
				return usagef("events replay: --flow is required")
			}
			req := map[string]any{"flow": replayFlow}
			if c.flagSet("dry-run") {
				req["dry_run"] = replayDry
			}
			body, _ := jsonBody(req)
			return c.doEmit(ctx, http.MethodPost, "/api/events/"+esc(c.args[0])+"/replay", nil, body)
		},
	}
	return []*command{
		cmd("list", "list events", list),
		cmd("get", "show one event with its fan-out", idGet("/api/events/%s")),
		cmd("inject", "inject an event for a sensor from stdin JSON, without polling", inject),
		cmd("replay", "replay an event through the current version of a flow", replay),
	}
}

func flowCmds() []*command {
	var file string
	validate := &leaf{
		usage: "[file.yaml|-]", maxArgs: 1,
		run: func(ctx context.Context, c *call) error {
			if len(c.args) > 0 {
				file = c.args[0]
			}
			yamlText, err := c.readInput(file)
			if err != nil {
				return err
			}
			body, _ := jsonBody(map[string]string{"yaml": string(yamlText)})
			resp, err := c.client().do(ctx, http.MethodPost, "/api/flows/validate", nil, body)
			if err != nil {
				return err
			}
			if err := c.emit(resp, "severity", "code", "message"); err != nil {
				return err
			}
			var res struct {
				Valid bool `json:"valid"`
			}
			if json.Unmarshal(resp.Body, &res) == nil && !res.Valid {
				return &silentError{}
			}
			return nil
		},
	}
	var event, nodeRun, jsonInput, wait string
	var dry bool
	testNode := &leaf{
		usage: "<flow-id> <node-id> [--event <id> | --node-run <id>[:<output-index>] | --json <value>] (default input: stdin JSON)", minArgs: 2, maxArgs: 2,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&event, "event", "", "use a stored event's payload as input")
			fs.StringVar(&nodeRun, "node-run", "", "use an earlier node run's output as input (<id> or <id>:<index>)")
			fs.StringVar(&jsonInput, "json", "", "use this JSON value as input")
			fs.StringVar(&wait, "wait", "", "how long to wait for the node run, e.g. 60s (server default 30s)")
			fs.BoolVar(&dry, "dry-run", false, "run with dry-run semantics")
		},
		run: func(ctx context.Context, c *call) error {
			input, err := testNodeInput(c, event, nodeRun, jsonInput)
			if err != nil {
				return err
			}
			body, _ := jsonBody(map[string]any{"node_id": c.args[1], "input": input, "dry_run": dry})
			return c.doEmit(ctx, http.MethodPost, "/api/flows/"+esc(c.args[0])+"/test-node", query("wait", wait), body)
		},
	}
	return []*command{
		cmd("list", "list flows", apiGet("/api/flows", "id", "enabled", "dry_run", "node_count", "health.last_run_status")),
		cmd("get", "show one flow with its YAML", idGet("/api/flows/%s")),
		cmd("validate", "validate flow YAML from a file or stdin without saving", validate),
		cmd("test-node", "run one node on a sample input", testNode),
	}
}

// testNodeInput builds the TestNodeInput object for exactly one input source;
// with none given it reads JSON from stdin.
func testNodeInput(c *call, event, nodeRun, jsonInput string) (map[string]any, error) {
	n := 0
	for _, s := range []string{event, nodeRun, jsonInput} {
		if s != "" {
			n++
		}
	}
	if n > 1 {
		return nil, usagef("flows test-node: use only one of --event, --node-run, --json")
	}
	switch {
	case event != "":
		return map[string]any{"source": "event", "event_id": event}, nil
	case nodeRun != "":
		id, idx, hasIdx := strings.Cut(nodeRun, ":")
		in := map[string]any{"source": "node_run", "node_run_id": id}
		if hasIdx {
			i, err := strconv.Atoi(idx)
			if err != nil || i < 0 {
				return nil, usagef("flows test-node: --node-run output index %q is not a non-negative integer", idx)
			}
			in["output_index"] = i
		}
		return in, nil
	}
	raw := []byte(jsonInput)
	if jsonInput == "" {
		var err error
		if raw, err = c.readInput("-"); err != nil {
			return nil, err
		}
	}
	if !json.Valid(raw) {
		return nil, usagef("flows test-node: the input is not valid JSON")
	}
	return map[string]any{"source": "json", "json": json.RawMessage(raw)}, nil
}

// pollWait is how long `--wait` asks the server to hold each request.
const pollWait = "30s"

// waitFor repeats a long-polling GET until done reports true or timeout passes.
// The server holds each request up to pollWait, so there is no client-side sleep.
func (c *call) waitFor(ctx context.Context, path string, timeout time.Duration, done func(body []byte) bool) (*response, error) {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := c.client().do(ctx, http.MethodGet, path, query("wait", pollWait), nil)
		if err != nil {
			return nil, err
		}
		if done(resp.Body) || !time.Now().Before(deadline) {
			return resp, nil
		}
	}
}

func flowRunDone(body []byte) bool {
	var d struct {
		FlowRun struct {
			Status string `json:"status"`
		} `json:"flow_run"`
	}
	if json.Unmarshal(body, &d) != nil {
		return true
	}
	switch d.FlowRun.Status {
	case "succeeded", "failed", "killed", "interrupted", "awaiting_approval":
		return true
	}
	return false
}

func nodeRunDone(body []byte) bool {
	var d struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &d) != nil {
		return true
	}
	switch d.Status {
	case "done", "failed", "killed", "interrupted", "skipped", "awaiting_approval":
		return true
	}
	return false
}

func runCmds() []*command {
	var lf listFlags
	var flow, event, status, kind, replayOf, since, until string
	list := &leaf{
		flags: func(fs *flag.FlagSet) {
			lf.register(fs)
			fs.StringVar(&flow, "flow", "", "filter by flow id")
			fs.StringVar(&event, "event", "", "filter by event id")
			fs.StringVar(&status, "status", "", "filter by status")
			fs.StringVar(&kind, "kind", "", "filter by kind: live, replay, test")
			fs.StringVar(&replayOf, "replay-of", "", "runs that replay this flow run")
			fs.StringVar(&since, "since", "", "only runs at or after this RFC 3339 time")
			fs.StringVar(&until, "until", "", "only runs before this RFC 3339 time")
		},
		run: func(ctx context.Context, c *call) error {
			qv := lf.apply(query("flow_id", flow, "event_id", event, "status", status, "kind", kind,
				"replay_of", replayOf, "since", since, "until", until))
			return c.doEmit(ctx, http.MethodGet, "/api/flow-runs", qv, nil, "id", "flow_id", "kind", "status", "dry_run", "created_at")
		},
	}
	var wait bool
	var timeout time.Duration
	get := &leaf{
		usage: "<id> [--wait [--timeout 10m]]", minArgs: 1, maxArgs: 1,
		flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&wait, "wait", false, "block until the run finishes or awaits approval")
			fs.DurationVar(&timeout, "timeout", 10*time.Minute, "with --wait: give up after this long and print the current state")
		},
		run: func(ctx context.Context, c *call) error {
			path := "/api/flow-runs/" + esc(c.args[0])
			if !wait {
				return c.doEmit(ctx, http.MethodGet, path, nil, nil)
			}
			resp, err := c.waitFor(ctx, path, timeout, flowRunDone)
			if err != nil {
				return err
			}
			return c.emit(resp)
		},
	}
	return []*command{cmd("list", "list flow runs", list), cmd("get", "show a flow run with its node runs", get)}
}

func nodeRunCmds() []*command {
	var wait bool
	var timeout time.Duration
	get := &leaf{
		usage: "<id> [--wait [--timeout 10m]]", minArgs: 1, maxArgs: 1,
		flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&wait, "wait", false, "block until the node run finishes or awaits approval")
			fs.DurationVar(&timeout, "timeout", 10*time.Minute, "with --wait: give up after this long and print the current state")
		},
		run: func(ctx context.Context, c *call) error {
			path := "/api/node-runs/" + esc(c.args[0])
			if !wait {
				return c.doEmit(ctx, http.MethodGet, path, nil, nil)
			}
			resp, err := c.waitFor(ctx, path, timeout, nodeRunDone)
			if err != nil {
				return err
			}
			return c.emit(resp)
		},
	}
	return []*command{cmd("get", "show a node run with stdin, stdout, stderr and agent runs", get)}
}

func agentRunCmds() []*command {
	var lf listFlags
	var status, runtime, flowRun, nodeRun string
	list := &leaf{
		flags: func(fs *flag.FlagSet) {
			lf.register(fs)
			fs.StringVar(&status, "status", "", "filter by status")
			fs.StringVar(&runtime, "runtime", "", "filter by runtime")
			fs.StringVar(&flowRun, "flow-run", "", "filter by flow run id")
			fs.StringVar(&nodeRun, "node-run", "", "filter by node run id")
		},
		run: func(ctx context.Context, c *call) error {
			qv := lf.apply(query("status", status, "runtime", runtime, "flow_run_id", flowRun, "node_run_id", nodeRun))
			return c.doEmit(ctx, http.MethodGet, "/api/agent-runs", qv, nil, "id", "runtime", "mode", "status", "flow_id", "created_at")
		},
	}
	var after, limit int
	var evKind string
	events := &leaf{
		usage: "<id> [--after-seq N] [--limit N] [--kind K]", minArgs: 1, maxArgs: 1,
		flags: func(fs *flag.FlagSet) {
			fs.IntVar(&after, "after-seq", 0, "only events with a greater sequence number")
			fs.IntVar(&limit, "limit", 0, "maximum events to return")
			fs.StringVar(&evKind, "kind", "", "filter by event kind")
		},
		run: func(ctx context.Context, c *call) error {
			qv := query("kind", evKind)
			if after > 0 {
				qv.Set("after_seq", strconv.Itoa(after))
			}
			if limit > 0 {
				qv.Set("limit", strconv.Itoa(limit))
			}
			return c.doEmit(ctx, http.MethodGet, "/api/agent-runs/"+esc(c.args[0])+"/events", qv, nil, "seq", "ts", "kind", "tool_call_id")
		},
	}
	return []*command{
		cmd("list", "list agent runs", list),
		cmd("get", "show an agent run", idGet("/api/agent-runs/%s")),
		cmd("events", "show an agent run's transcript events", events),
	}
}

func approvalCmds() []*command {
	var lf listFlags
	var status, typ, agentRun, flowRun string
	list := &leaf{
		flags: func(fs *flag.FlagSet) {
			lf.register(fs)
			fs.StringVar(&status, "status", "", "filter by status: pending, approved, denied, expired")
			fs.StringVar(&typ, "type", "", "filter by type: proposal, tool_call")
			fs.StringVar(&agentRun, "agent-run", "", "filter by agent run id")
			fs.StringVar(&flowRun, "flow-run", "", "filter by flow run id")
		},
		run: func(ctx context.Context, c *call) error {
			qv := lf.apply(query("status", status, "type", typ, "agent_run_id", agentRun, "flow_run_id", flowRun))
			return c.doEmit(ctx, http.MethodGet, "/api/approvals", qv, nil, "id", "type", "status", "flow_id", "node_id", "runtime", "created_at")
		},
	}
	var approveComment string
	approve := &leaf{
		usage: "<id> [--comment text]", minArgs: 1, maxArgs: 1,
		flags: func(fs *flag.FlagSet) { fs.StringVar(&approveComment, "comment", "", "comment passed to the agent") },
		run: func(ctx context.Context, c *call) error {
			req := map[string]any{}
			if approveComment != "" {
				req["comment"] = approveComment
			}
			body, _ := jsonBody(req)
			return c.doEmit(ctx, http.MethodPost, "/api/approvals/"+esc(c.args[0])+"/approve", nil, body)
		},
	}
	var denyComment string
	var cancelRun bool
	deny := &leaf{
		usage: "<id> [--comment text] [--cancel-run]", minArgs: 1, maxArgs: 1,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&denyComment, "comment", "", "reason, passed to the agent")
			fs.BoolVar(&cancelRun, "cancel-run", false, "tool_call approvals: cancel the agent run instead of letting it continue")
		},
		run: func(ctx context.Context, c *call) error {
			req := map[string]any{}
			if denyComment != "" {
				req["comment"] = denyComment
			}
			if cancelRun {
				req["cancel_run"] = true
			}
			body, _ := jsonBody(req)
			return c.doEmit(ctx, http.MethodPost, "/api/approvals/"+esc(c.args[0])+"/deny", nil, body)
		},
	}
	return []*command{
		cmd("list", "list approvals", list),
		cmd("get", "show an approval", idGet("/api/approvals/%s")),
		cmd("approve", "approve a proposal or allow a tool call", approve),
		cmd("deny", "deny a proposal or tool call", deny),
	}
}

func inboxCmds() []*command {
	var lf listFlags
	var priority, flow, q, sort string
	var unread, starred, archived, dry bool
	list := &leaf{
		flags: func(fs *flag.FlagSet) {
			lf.register(fs)
			fs.StringVar(&priority, "priority", "", "filter by priority: low, normal, high, urgent")
			fs.StringVar(&flow, "flow", "", "filter by flow id")
			fs.StringVar(&q, "q", "", "search text")
			fs.StringVar(&sort, "sort", "", "sort order (see the API reference)")
			fs.BoolVar(&unread, "unread", false, "only unread items")
			fs.BoolVar(&starred, "starred", false, "only starred items")
			fs.BoolVar(&archived, "archived", false, "include only archived items")
			fs.BoolVar(&dry, "dry-run", false, "only items from dry-run flows")
		},
		run: func(ctx context.Context, c *call) error {
			qv := lf.apply(query("priority", priority, "flow_id", flow, "q", q, "sort", sort))
			if unread {
				qv.Set("read", "false")
			}
			if starred {
				qv.Set("starred", "true")
			}
			if archived {
				qv.Set("archived", "true")
			}
			if dry {
				qv.Set("dry_run", "true")
			}
			return c.doEmit(ctx, http.MethodGet, "/api/inbox", qv, nil, "id", "priority", "title", "read", "starred", "created_at")
		},
	}
	return []*command{
		cmd("list", "list inbox items", list),
		cmd("get", "show an inbox item", idGet("/api/inbox/%s")),
		cmd("read", "mark an item read", inboxFlag("read", "unread", "mark unread instead")),
		cmd("star", "star an item", inboxFlag("starred", "off", "remove the star instead")),
		cmd("archive", "archive an item", inboxFlag("archived", "undo", "unarchive instead")),
	}
}

// inboxFlag builds a leaf that PATCHes one boolean field of an inbox item.
func inboxFlag(field, offFlag, offHelp string) *leaf {
	var off bool
	return &leaf{
		usage: "<id> [--" + offFlag + "]", minArgs: 1, maxArgs: 1,
		flags: func(fs *flag.FlagSet) { fs.BoolVar(&off, offFlag, false, offHelp) },
		run: func(ctx context.Context, c *call) error {
			body, _ := jsonBody(map[string]bool{field: !off})
			return c.doEmit(ctx, http.MethodPatch, "/api/inbox/"+esc(c.args[0]), nil, body)
		},
	}
}

func killswitchCmds() []*command {
	var kill bool
	var reason string
	pause := &leaf{
		usage: "[--kill] [--reason text]",
		flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&kill, "kill", false, "also terminate running nodes")
			fs.StringVar(&reason, "reason", "", "why the daemon is paused")
		},
		run: func(ctx context.Context, c *call) error {
			req := map[string]any{"kill_running": kill}
			if reason != "" {
				req["reason"] = reason
			}
			body, _ := jsonBody(req)
			return c.doEmit(ctx, http.MethodPost, "/api/killswitch/pause", nil, body)
		},
	}
	return []*command{
		cmd("get", "show the kill switch state", apiGet("/api/killswitch")),
		cmd("pause", "pause sensors and stop starting nodes", pause),
		cmd("resume", "resume the daemon", &leaf{run: func(ctx context.Context, c *call) error {
			return c.doEmit(ctx, http.MethodPost, "/api/killswitch/resume", nil, nil)
		}}),
	}
}

func runtimeCmds() []*command {
	return []*command{
		cmd("list", "list runtimes", apiGet("/api/runtimes", "name", "command", "last_probe.ok")),
		cmd("probe", "start a runtime and report what it advertises", &leaf{usage: "<name>", minArgs: 1, maxArgs: 1, run: func(ctx context.Context, c *call) error {
			return c.doEmit(ctx, http.MethodPost, "/api/runtimes/"+esc(c.args[0])+"/probe", nil, nil)
		}}),
	}
}

// envCmds are the env list/set/unset commands. With perSensor they work on
// one sensor's own env (`sensors env ... <id> ...`): the sensor id is the
// first argument and the paths are under /api/sensors/<id>/env. The values are
// write-only either way.
func envCmds(perSensor bool) []*command {
	var fromStdin bool
	name, who, idArg, n := "env", "", "", 0
	base := func(c *call) (string, []string) { return "/api/env", c.args }
	if perSensor {
		name, who, idArg, n = "sensors env", " on a sensor", "<sensor-id> ", 1
		base = func(c *call) (string, []string) { return "/api/sensors/" + esc(c.args[0]) + "/env", c.args[1:] }
	}
	set := &leaf{
		usage: idArg + "<KEY> [value]   (without a value, or with --stdin, the value is read from stdin)", minArgs: 1 + n, maxArgs: 2 + n,
		flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&fromStdin, "stdin", false, "read the value from stdin (keeps it out of the process list and shell history)")
		},
		run: func(ctx context.Context, c *call) error {
			path, rest := base(c)
			var value string
			switch {
			case len(rest) == 2 && !fromStdin:
				value = rest[1]
			case len(rest) == 2:
				return usagef("%s set: give the value as an argument or with --stdin, not both", name)
			default:
				data, err := c.readInput("-")
				if err != nil {
					return err
				}
				value = strings.TrimRight(string(data), "\r\n")
			}
			body, _ := jsonBody(map[string]string{"value": value})
			return c.doEmit(ctx, http.MethodPut, path+"/"+esc(rest[0]), nil, body)
		},
	}
	list := apiGet("/api/env", "key")
	if perSensor {
		list = &leaf{usage: "<sensor-id>", minArgs: 1, maxArgs: 1, run: func(ctx context.Context, c *call) error {
			path, _ := base(c)
			return c.doEmit(ctx, http.MethodGet, path, nil, nil, "key", "overrides_global")
		}}
	}
	return []*command{
		cmd("list", "list env variable names"+who+" (values are never shown)", list),
		cmd("set", "create or replace an env variable"+who, set),
		cmd("unset", "remove an env variable"+who, &leaf{usage: idArg + "<KEY>", minArgs: 1 + n, maxArgs: 1 + n, run: func(ctx context.Context, c *call) error {
			path, rest := base(c)
			return c.doEmit(ctx, http.MethodDelete, path+"/"+esc(rest[0]), nil, nil)
		}}),
	}
}

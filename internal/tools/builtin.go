package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func builtinTools(gh GHOptions) []Tool {
	ts := ghTools(gh)
	ts = append(ts,
		Tool{
			Name:        "now",
			Description: "Current date and time. Optional timezone is an IANA name such as Europe/Berlin (default: the daemon's local zone).",
			InputSchema: obj(map[string]any{"timezone": map[string]any{"type": "string", "description": "IANA time zone name"}}),
			Run:         runNow,
		},
		Tool{
			Name:        "run_context",
			Description: "Ids and trigger of this run: run, flow and node ids, run kind, sensor, event, when the sensor polled.",
			InputSchema: obj(map[string]any{}),
			Run:         runContext,
		},
		Tool{
			Name:        "node_input",
			Description: "The input payload of this node (JSON), complete and untruncated.",
			InputSchema: obj(map[string]any{}),
			Run: func(_ context.Context, c Call) (Result, error) {
				return jsonOrNull(c.Info.Input), nil
			},
		},
		Tool{
			Name:        "event",
			Description: "The original event payload (JSON) that started this flow run.",
			InputSchema: obj(map[string]any{}),
			Run: func(_ context.Context, c Call) (Result, error) {
				return jsonOrNull(c.Info.Event), nil
			},
		},
	)
	return ts
}

func jsonOrNull(b json.RawMessage) Result {
	if len(b) == 0 {
		return Result{Text: "null"}
	}
	return Result{Text: string(b)}
}

func runNow(_ context.Context, c Call) (Result, error) {
	var a struct {
		Timezone string `json:"timezone"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Result{}, fmt.Errorf("arguments: %w", err)
	}
	t := c.Now
	if a.Timezone != "" {
		loc, err := time.LoadLocation(a.Timezone)
		if err != nil {
			return Result{}, fmt.Errorf("unknown timezone %q", a.Timezone)
		}
		t = t.In(loc)
	} else {
		t = t.Local()
	}
	return Result{Text: formatNow(t)}, nil
}

func formatNow(t time.Time) string {
	zone, _ := t.Zone()
	return fmt.Sprintf("%s (%s, %s, %s)", t.Format(time.RFC3339), t.Weekday(), t.Location(), zone)
}

func runContext(_ context.Context, c Call) (Result, error) {
	i := c.Info
	m := map[string]any{
		"agent_run_id": i.AgentRunID, "node_run_id": i.NodeRunID, "flow_run_id": i.FlowRunID,
		"flow_id": i.FlowID, "node_id": i.NodeID, "assist_session_id": i.AssistSessionID,
		"run_kind": i.RunKind, "dry_run": i.DryRun, "sensor_id": i.SensorID, "event_id": i.EventID,
	}
	if !i.PolledAt.IsZero() {
		m["polled_at"] = i.PolledAt.UTC().Format(time.RFC3339)
	}
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return Result{Text: string(b)}, nil
}

// BuiltinToolNames lists the names of the tools every agent run has (the hal tools, not custom tools or the sandbox
// tools), sorted.
func BuiltinToolNames() []string {
	var names []string
	for _, t := range builtinTools(GHOptions{}) {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}

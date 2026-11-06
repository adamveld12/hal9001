package cli

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"strings"
)

// sessionArg returns the assist session id: the first argument, or
// $HAL9001_ASSIST_SESSION when the argument is omitted.
func (c *call) sessionArg(name string) (string, error) {
	if len(c.args) > 0 {
		return c.args[0], nil
	}
	if id := c.env.Getenv(EnvAssistSession); id != "" {
		return id, nil
	}
	return "", usagef("%s: no session: pass a session id or set %s", name, EnvAssistSession)
}

func assistCmds() []*command {
	var listKind, listID string
	list := &leaf{
		usage: "[--target-kind sensor|flow] [--target-id ID]", maxArgs: 0,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&listKind, "target-kind", "", "only sessions of this target kind (sensor or flow)")
			fs.StringVar(&listID, "target-id", "", "only sessions editing this sensor or flow id")
		},
		run: func(ctx context.Context, c *call) error {
			return c.doEmit(ctx, http.MethodGet, "/api/assist/sessions", query("target_kind", listKind, "target_id", listID), nil,
				"id", "target.id", "runtime", "status", "updated_at")
		},
	}

	var runtime, sensor, flowID, kind, model, createYAML string
	create := &leaf{
		usage: "--runtime NAME [--sensor ID|--flow ID|--kind sensor|flow] [--model M] [--yaml FILE|-]", maxArgs: 0,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&runtime, "runtime", "", "ACP runtime that runs the session (required)")
			fs.StringVar(&sensor, "sensor", "", "sensor to edit (default: author a new sensor)")
			fs.StringVar(&flowID, "flow", "", "flow to edit")
			fs.StringVar(&kind, "kind", "", "what to author when no id is given: sensor (default) or flow")
			fs.StringVar(&model, "model", "", "model for the runtime")
			fs.StringVar(&createYAML, "yaml", "", "initial draft from this file, or - for stdin")
		},
		run: func(ctx context.Context, c *call) error {
			if runtime == "" {
				return usagef("assist create: --runtime is required")
			}
			if sensor != "" && flowID != "" {
				return usagef("assist create: --sensor and --flow are exclusive")
			}
			target := map[string]any{"kind": "sensor"}
			switch {
			case flowID != "":
				target = map[string]any{"kind": "flow", "id": flowID}
			case sensor != "":
				target["id"] = sensor
			case kind != "":
				target["kind"] = kind
			}
			req := map[string]any{"target": target, "runtime": runtime}
			if model != "" {
				req["model"] = model
			}
			if c.flagSet("yaml") {
				text, err := c.readInput(createYAML)
				if err != nil {
					return err
				}
				req["yaml"] = string(text)
			}
			body, _ := jsonBody(req)
			return c.doEmit(ctx, http.MethodPost, "/api/assist/sessions", nil, body)
		},
	}

	var sendYAML string
	send := &leaf{
		usage: "<session> [<text...>|-] [--yaml FILE|-]", minArgs: 1, maxArgs: -1,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&sendYAML, "yaml", "", "replace the draft with this file (or - for stdin) before the turn")
		},
		run: func(ctx context.Context, c *call) error {
			words := c.args[1:]
			text := strings.Join(words, " ")
			if len(words) == 0 || text == "-" {
				if c.flagSet("yaml") && (sendYAML == "" || sendYAML == "-") {
					return usagef("assist send: text and --yaml cannot both come from stdin")
				}
				raw, err := c.readInput("-")
				if err != nil {
					return err
				}
				text = strings.TrimRight(string(raw), "\r\n")
			}
			if text == "" {
				return usagef("assist send: message text is empty")
			}
			req := map[string]any{"text": text}
			if c.flagSet("yaml") {
				y, err := c.readInput(sendYAML)
				if err != nil {
					return err
				}
				req["yaml"] = string(y)
			}
			body, _ := jsonBody(req)
			return c.doEmit(ctx, http.MethodPost, "/api/assist/sessions/"+esc(c.args[0])+"/messages", nil, body)
		},
	}

	cancel := &leaf{
		usage: "[<session>] (default: $" + EnvAssistSession + ")", maxArgs: 1,
		run: func(ctx context.Context, c *call) error {
			id, err := c.sessionArg("assist cancel")
			if err != nil {
				return err
			}
			return c.doEmit(ctx, http.MethodPost, "/api/assist/sessions/"+esc(id)+"/cancel", nil, nil)
		},
	}

	closeSession := &leaf{usage: "<session>", minArgs: 1, maxArgs: 1, run: func(ctx context.Context, c *call) error {
		return c.doEmit(ctx, http.MethodDelete, "/api/assist/sessions/"+esc(c.args[0]), nil, nil)
	}}

	var testSample int
	var testEvent string
	test := &leaf{
		usage: "[--sample N|--event ID] [<session>] (default: $" + EnvAssistSession + ")", maxArgs: 1,
		flags: func(fs *flag.FlagSet) {
			fs.IntVar(&testSample, "sample", 0, "flow sessions: run the draft on sample N (see `assist samples`)")
			fs.StringVar(&testEvent, "event", "", "flow sessions: run the draft on this stored event")
		},
		run: func(ctx context.Context, c *call) error {
			id, err := c.sessionArg("assist test")
			if err != nil {
				return err
			}
			// Inside an assist session (the variable the daemon sets for the agent) the request says
			// so: the daemon runs an agent's test only for a script the user has tested or approved.
			cl := c.client()
			if c.env.Getenv(EnvAssistSession) != "" {
				cl.headers = http.Header{AssistAgentHeader: {"1"}}
			}
			if c.flagSet("sample") && c.flagSet("event") {
				return usagef("assist test: --sample and --event are exclusive")
			}
			req := map[string]any{}
			if c.flagSet("sample") {
				req["sample_index"] = testSample
			}
			if testEvent != "" {
				req["event_id"] = testEvent
			}
			body, _ := jsonBody(req)
			resp, err := cl.do(ctx, http.MethodPost, "/api/assist/sessions/"+esc(id)+"/test", nil, body)
			if err != nil {
				return err
			}
			// A test script that exits non-zero is still a successful request;
			// its exit_code is part of the printed result.
			return c.emit(resp)
		},
	}

	samples := &leaf{
		usage: "[<session>] (default: $" + EnvAssistSession + ")", maxArgs: 1,
		run: func(ctx context.Context, c *call) error {
			id, err := c.sessionArg("assist samples")
			if err != nil {
				return err
			}
			return c.doEmit(ctx, http.MethodGet, "/api/assist/sessions/"+esc(id)+"/samples", nil, nil, "index", "sensor_id")
		},
	}

	draft := &leaf{
		usage: "[<session>] (default: $" + EnvAssistSession + ")", maxArgs: 1,
		run: func(ctx context.Context, c *call) error {
			id, err := c.sessionArg("assist draft")
			if err != nil {
				return err
			}
			resp, err := c.client().do(ctx, http.MethodGet, "/api/assist/sessions/"+esc(id), nil, nil)
			if err != nil {
				return err
			}
			var detail struct {
				Draft json.RawMessage `json:"draft"`
			}
			if err := json.Unmarshal(resp.Body, &detail); err != nil || len(detail.Draft) == 0 {
				return usagef("assist draft: the response has no draft")
			}
			return render(c.stdout(), detail.Draft, c.table, nil)
		},
	}

	return []*command{
		cmd("list", "list assist sessions", list),
		cmd("create", "start an assist session for a new or existing sensor or flow", create),
		cmd("get", "show a session with its draft and messages", idGet("/api/assist/sessions/%s")),
		cmd("send", "send a message and start an agent turn", send),
		cmd("cancel", "cancel the running turn", cancel),
		cmd("close", "end a session and remove its draft directory", closeSession),
		cmd("test", "run the session's draft (a poll for a sensor, one sample for a flow) and print the result", test),
		cmd("samples", "list the sample inputs of a flow session", samples),
		cmd("draft", "print the session's current draft (YAML, hash, diagnostics)", draft),
	}
}

func sensorValidateLeaf() *leaf {
	return &leaf{
		usage: "[file.yaml|-]", maxArgs: 1,
		run: func(ctx context.Context, c *call) error {
			file := ""
			if len(c.args) > 0 {
				file = c.args[0]
			}
			yamlText, err := c.readInput(file)
			if err != nil {
				return err
			}
			body, _ := jsonBody(map[string]string{"yaml": string(yamlText)})
			resp, err := c.client().do(ctx, http.MethodPost, "/api/sensors/validate", nil, body)
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
}

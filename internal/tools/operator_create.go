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
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// flow_create, sensor_create and their validators. A spec is checked by the same REST validators the web UI uses
// (POST /api/flows/validate, POST /api/sensors/validate) before anybody is asked: an invalid spec comes straight
// back to the agent as a tool error listing the diagnostics, so it can fix and retry. A valid one raises an Inbox
// approval that shows what would be written, and the file is created through POST /api/flows (or /api/sensors) only
// after it is approved. Nothing is overwritten: an id that exists is refused, before the approval and again when the
// file is written.

// maxSpecBytes caps a spec so an approval stays readable.
const maxSpecBytes = 64 << 10

// specIDRE is spec.ValidID: ids are file names.
var specIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// specKind is what differs between flows and sensors.
type specKind struct {
	noun       string // "flow"
	collection string // "/api/flows"
	object     string // the parsed object in the validate response: "flow"
	// describe renders the approval: its table and a markdown body, from the parsed object and the YAML.
	describe func(id, yamlText string, obj map[string]any, c OperatorCaller) ([]OperatorField, string)
}

type diagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
	NodeID   string `json:"node_id,omitempty"`
	Line     int    `json:"line,omitempty"`
}

func (d diagnostic) String() string {
	var where []string
	if d.Path != "" {
		where = append(where, d.Path)
	}
	if d.NodeID != "" {
		where = append(where, "node "+d.NodeID)
	}
	if d.Line > 0 {
		where = append(where, fmt.Sprintf("line %d", d.Line))
	}
	s := "- [" + d.Code + "] "
	if len(where) > 0 {
		s += strings.Join(where, ", ") + ": "
	}
	return s + d.Message
}

// split divides diagnostics into errors and warnings.
func splitDiagnostics(ds []diagnostic) (errs, warns []diagnostic) {
	errs, warns = []diagnostic{}, []diagnostic{}
	for _, d := range ds {
		if d.Severity == "error" {
			errs = append(errs, d)
		} else {
			warns = append(warns, d)
		}
	}
	return errs, warns
}

func listDiagnostics(ds []diagnostic) string {
	lines := make([]string, len(ds))
	for i, d := range ds {
		lines[i] = d.String()
	}
	return strings.Join(lines, "\n")
}

// specText returns the spec as YAML text. JSON is YAML, but a JSON spec is rewritten in block style (keeping the
// key order) so the approval and the file read like a flow file.
func specText(raw string) (string, error) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", errors.New("spec is required: the flow or sensor as YAML or JSON text")
	}
	if len(raw) > maxSpecBytes {
		return "", fmt.Errorf("spec is %d bytes; the limit is %d", len(raw), maxSpecBytes)
	}
	if !strings.HasPrefix(t, "{") {
		return strings.TrimRight(raw, "\n") + "\n", nil
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(t), &n); err != nil {
		return "", fmt.Errorf("spec does not parse as JSON or YAML: %w", err)
	}
	blockStyle(&n)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&n); err != nil {
		return "", fmt.Errorf("render spec as YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("render spec as YAML: %w", err)
	}
	return buf.String(), nil
}

func blockStyle(n *yaml.Node) {
	n.Style = 0
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" && strings.Contains(n.Value, "\n") {
		n.Style = yaml.LiteralStyle
	}
	for _, c := range n.Content {
		blockStyle(c)
	}
}

// specID is the top-level id: of a spec, "" when there is none.
func specID(text string) string {
	var doc struct {
		ID string `yaml:"id"`
	}
	_ = yaml.Unmarshal([]byte(text), &doc)
	return doc.ID
}

type validation struct {
	Valid       bool           `json:"valid"`
	Diagnostics []diagnostic   `json:"diagnostics"`
	Parsed      map[string]any `json:"-"`
}

// validate runs the REST validator of kind on text as the file id.
func (k specKind) validate(ctx context.Context, api APIFunc, text, id string) (validation, error) {
	body, _ := json.Marshal(map[string]any{"yaml": text, "id": id})
	status, out, err := api(ctx, http.MethodPost, k.collection+"/validate", nil, body)
	if err != nil {
		return validation{}, err
	}
	if status != http.StatusOK {
		return validation{}, errors.New(problemText(status, out))
	}
	var v validation
	if err := json.Unmarshal(out, &v); err != nil {
		return validation{}, fmt.Errorf("decode validation: %w", err)
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(out, &raw) == nil {
		_ = json.Unmarshal(raw[k.object], &v.Parsed)
	}
	return v, nil
}

// exists reports whether a flow or sensor with this id is on disk.
func (k specKind) exists(ctx context.Context, api APIFunc, id string) (bool, error) {
	status, out, err := api(ctx, http.MethodGet, k.collection+"/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, errors.New(problemText(status, out))
}

// prepared is a spec that passed validation.
type prepared struct {
	id, yaml string
	parsed   map[string]any
	warnings []diagnostic
	// exists reports that the id is taken.
	exists bool
}

// prepare normalizes and validates a spec for id (the id argument, or the spec's own id: line). A spec with errors
// is returned as an error listing them; withExists also checks the id against the files on disk.
func (k specKind) prepare(ctx context.Context, api APIFunc, a map[string]any, tool string) (prepared, error) {
	raw, err := strArg(a, "spec")
	if err != nil {
		return prepared{}, err
	}
	text, err := specText(raw)
	if err != nil {
		return prepared{}, err
	}
	id, err := strArg(a, "id")
	if err != nil {
		return prepared{}, err
	}
	if id == "" {
		id = specID(text)
	}
	if id == "" {
		return prepared{}, fmt.Errorf("the %s needs an id: pass the id argument or put an id: line in the spec", k.noun)
	}
	if !specIDRE.MatchString(id) {
		return prepared{}, fmt.Errorf("id %q is invalid: use letters, digits, - and _ (max 64, starting with a letter or digit)", id)
	}
	v, err := k.validate(ctx, api, text, id)
	if err != nil {
		return prepared{}, err
	}
	errs, warns := splitDiagnostics(v.Diagnostics)
	if !v.Valid || len(errs) > 0 {
		return prepared{}, fmt.Errorf("the %s spec has %d error(s); nothing was created and nobody was asked. Fix them and call %s again (or %s_validate to check first):\n%s",
			k.noun, len(errs), tool, k.noun, listDiagnostics(errs))
	}
	taken, err := k.exists(ctx, api, id)
	if err != nil {
		return prepared{}, err
	}
	return prepared{id: id, yaml: text, parsed: v.Parsed, warnings: warns, exists: taken}, nil
}

func (k specKind) existsError(id string) error {
	return fmt.Errorf("a %s with id %q already exists. %s_create never overwrites; choose another id (use %ss_list to see the ids in use)", k.noun, id, k.noun, k.noun)
}

var specProps = func(noun string) map[string]any {
	return map[string]any{
		"spec": prop("string", "The "+noun+" as YAML or JSON text: the same schema as a "+noun+" file under "+noun+"s/ (see the hal9001 docs). The id may also be given as a top-level id: key."),
		"id":   prop("string", "The "+noun+" id, which becomes the file name: letters, digits, - and _ (max 64, starting with a letter or digit). Optional when the spec has an id: line."),
	}
}

var flowKind = specKind{noun: "flow", collection: "/api/flows", object: "flow", describe: describeFlow}
var sensorKind = specKind{noun: "sensor", collection: "/api/sensors", object: "sensor", describe: describeSensor}

func validateArgs(a map[string]any) error {
	for _, k := range []string{"spec", "id"} {
		if _, err := strArg(a, k); err != nil {
			return err
		}
	}
	if s, _ := a["spec"].(string); strings.TrimSpace(s) == "" {
		return errors.New("spec is required: the YAML or JSON text")
	}
	return nil
}

// specTools builds <noun>_validate and <noun>_create.
func (o *Operator) specTools(k specKind) []operatorTool {
	validateName, createName := k.noun+"_validate", k.noun+"_create"
	return []operatorTool{
		o.hostTool(validateName,
			"Check a "+k.noun+" spec without creating anything and without asking anybody: parses it, runs the same validation the hal9001 UI does (graph, fields, templates, references) and says whether the id is free. Returns {valid, id, id_available, errors, warnings}.",
			obj(specProps(k.noun), "spec"), "", false, validateArgs,
			func(ctx context.Context, env OperatorEnv, a map[string]any) (Result, error) {
				return o.runValidate(ctx, env, k, a)
			}),
		o.hostTool(createName,
			"Create a "+k.noun+" from a spec. The spec is validated first and its errors are returned to you at once (fix and call again). A valid spec raises an approval in the hal9001 Inbox that shows the id, description and the spec, and the "+k.noun+" is only created when the user approves (up to 4 minutes). Never overwrites: an existing id is refused. "+createNote(k),
			obj(specProps(k.noun), "spec"), ServiceAdmin, true, validateArgs,
			func(ctx context.Context, env OperatorEnv, a map[string]any) (Result, error) {
				return o.runCreate(ctx, env, k, createName, a)
			}),
	}
}

func createNote(k specKind) string {
	if k.noun == "flow" {
		return "A flow is created disabled unless the spec says enabled: true (the approval states which). Reference sensors and prompts that already exist."
	}
	return "A sensor does nothing on a schedule until an enabled flow subscribes to it; with neither interval nor cron it only runs when polled."
}

func (o *Operator) runValidate(ctx context.Context, env OperatorEnv, k specKind, a map[string]any) (Result, error) {
	raw, _ := strArg(a, "spec")
	text, err := specText(raw)
	if err != nil {
		return Result{}, err
	}
	id, _ := strArg(a, "id")
	if id == "" {
		id = specID(text)
	}
	res := map[string]any{"id": id}
	if id != "" && !specIDRE.MatchString(id) {
		res["valid"], res["id_available"] = false, false
		res["errors"] = []diagnostic{{Severity: "error", Code: "invalid_id", Path: "id", Message: fmt.Sprintf("id %q is invalid: use letters, digits, - and _ (max 64, starting with a letter or digit)", id)}}
		res["warnings"] = []diagnostic{}
		return jsonResult(res)
	}
	v, err := k.validate(ctx, env.API, text, id)
	if err != nil {
		return Result{}, err
	}
	errs, warns := splitDiagnostics(v.Diagnostics)
	res["valid"] = v.Valid && len(errs) == 0
	res["errors"], res["warnings"] = errs, warns
	if id == "" {
		res["id_available"] = nil
	} else {
		taken, err := k.exists(ctx, env.API, id)
		if err != nil {
			return Result{}, err
		}
		res["id_available"] = !taken
	}
	return jsonResult(res)
}

func (o *Operator) runCreate(ctx context.Context, env OperatorEnv, k specKind, tool string, a map[string]any) (Result, error) {
	p, err := k.prepare(ctx, env.API, a, tool)
	if err != nil {
		return Result{}, err
	}
	if p.exists {
		return Result{}, k.existsError(p.id)
	}
	fields, body := k.describe(p.id, p.yaml, p.parsed, env.Caller)
	d, err := o.host.ApproveOperator(ctx, OperatorApproval{
		Tool: tool, Reason: "Create the " + k.noun + " " + p.id, Title: "Create " + k.noun + ": " + p.id,
		Description: fmt.Sprintf("Create the %s %q from the spec in the details; it is saved only if you approve.", k.noun, p.id),
		BodyMD:      body, Fields: fields, Caller: env.Caller, Timeout: o.timeout,
	})
	if err != nil {
		return Result{}, fmt.Errorf("tool %s: approval failed: %w", tool, err)
	}
	if !d.Allowed {
		return Result{}, &DeniedError{Tool: tool, Expired: d.Expired, Cancelled: d.Cancelled, Held: d.Held, Comment: d.Comment}
	}
	reqBody, _ := json.Marshal(map[string]any{"id": p.id, "yaml": p.yaml})
	status, out, err := env.API(ctx, http.MethodPost, k.collection, nil, reqBody)
	if err != nil {
		return Result{}, err
	}
	switch {
	case status == http.StatusConflict:
		return Result{}, k.existsError(p.id)
	case status != http.StatusCreated:
		return Result{}, fmt.Errorf("the user approved, but the %s was not created: %s", k.noun, problemText(status, out))
	}
	var created struct {
		ID          string       `json:"id"`
		Path        string       `json:"path"`
		Hash        string       `json:"hash"`
		Diagnostics []diagnostic `json:"diagnostics"`
	}
	_ = json.Unmarshal(out, &created)
	_, warns := splitDiagnostics(created.Diagnostics)
	res := map[string]any{"created": true, "id": created.ID, "path": created.Path, "hash": created.Hash, "warnings": warns}
	if k.noun == "flow" {
		res["enabled"], _ = p.parsed["enabled"].(bool)
		res["dry_run"], _ = p.parsed["dry_run"].(bool)
	}
	return jsonResult(res)
}

func jsonResult(v any) (Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return Result{}, err
	}
	return Result{Text: string(b)}, nil
}

// hostTool builds a tool the operator runs itself (no single REST call behind it). validate checks the arguments
// before the auth check and before anybody is asked; run does the rest.
func (o *Operator) hostTool(name, desc string, schema map[string]any, service string, needsApproval bool, validate func(map[string]any) error,
	run func(ctx context.Context, env OperatorEnv, a map[string]any) (Result, error)) operatorTool {
	return operatorTool{NeedsApproval: needsApproval, Service: service, Tool: Tool{
		Name: name, Description: desc, InputSchema: schema,
		Policy: func(raw json.RawMessage) (Verdict, string) {
			a, err := parseArgs(schema, raw)
			if err == nil && validate != nil {
				err = validate(a)
			}
			if err != nil {
				return VerdictDeny, err.Error()
			}
			return VerdictAllow, ""
		},
		Run: func(ctx context.Context, c Call) (Result, error) {
			env, err := envFrom(ctx)
			if err != nil {
				return Result{}, err
			}
			a, err := parseArgs(schema, c.Args)
			if err != nil {
				return Result{}, err
			}
			return run(ctx, env, a)
		},
	}}
}

// parseArgs decodes a call's arguments as an object and refuses names the schema does not list.
func parseArgs(schema map[string]any, raw json.RawMessage) (map[string]any, error) {
	var a map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
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

// ---- approval text ----

// fence returns text in a markdown code fence longer than any backtick run inside it.
func fence(lang, text string) string {
	f := "```"
	for strings.Contains(text, f) {
		f += "`"
	}
	return f + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + f
}

func strOf(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func strList(v any) []string {
	var out []string
	arr, _ := v.([]any)
	for _, x := range arr {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case map[string]any: // {id: ..., approve: ...} tool refs
			if id := strOf(t, "id"); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func codeList(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = "`" + s + "`"
	}
	return strings.Join(q, ", ")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func quote(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

func describeFlow(id, yamlText string, flow map[string]any, c OperatorCaller) ([]OperatorField, string) {
	enabled, _ := flow["enabled"].(bool)
	dry, _ := flow["dry_run"].(bool)
	subscribe := strList(flow["subscribe"])
	var prompts, toolIDs, mcpServers []string
	byType := map[string][]string{}
	nodes, _ := flow["nodes"].([]any)
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		typ := strOf(node, "type")
		byType[typ] = append(byType[typ], strOf(node, "id"))
		if p := strOf(node, "prompt"); p != "" {
			prompts = append(prompts, p)
		}
		toolIDs = append(toolIDs, strList(node["tools"])...)
		servers, _ := node["mcp_servers"].([]any)
		for _, s := range servers {
			if m, ok := s.(map[string]any); ok {
				mcpServers = append(mcpServers, strOf(m, "name"))
			}
		}
	}
	prompts, toolIDs, mcpServers = uniqueSorted(prompts), uniqueSorted(toolIDs), uniqueSorted(mcpServers)

	state := "disabled: it does not run until you enable it"
	if enabled {
		state = "**ENABLED**: it starts running as soon as its sensors produce items"
	}
	var counts []string
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		counts = append(counts, fmt.Sprintf("%d %s", len(byType[t]), t))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s asked to create the flow `%s` (file `flows/%s.yaml`). It is only created if you approve; denying tells the client it was refused. Nothing is overwritten.\n\n", c.Label(), id, id)
	if d := strOf(flow, "description"); d != "" {
		b.WriteString(quote(d) + "\n\n")
	}
	fmt.Fprintf(&b, "- **State:** %s\n- **Dry run:** %s\n- **Subscribes to sensors:** %s\n- **Prompts:** %s\n- **Custom tools:** %s\n- **Nodes (%d):** %s\n",
		state, yesNo(dry), codeList(subscribe), codeList(prompts), codeList(toolIDs), len(nodes), strings.Join(counts, ", "))
	if len(mcpServers) > 0 {
		fmt.Fprintf(&b, "- **MCP servers passed to agents:** %s\n", codeList(mcpServers))
	}
	if cmds := byType["command"]; len(cmds) > 0 {
		fmt.Fprintf(&b, "- **Runs code on this Mac:** command nodes %s execute scripts or commands with your permissions\n", codeList(cmds))
	}
	if ags := byType["agent"]; len(ags) > 0 {
		fmt.Fprintf(&b, "- **Starts agents:** agent nodes %s\n", codeList(ags))
	}
	b.WriteString("\nThe spec:\n\n" + fence("yaml", yamlText) + "\n")

	fields := []OperatorField{
		{Key: "flow id", Value: id},
		{Key: "enabled", Value: yesNo(enabled)},
		{Key: "dry run", Value: yesNo(dry)},
		{Key: "subscribes to", Value: strings.Join(subscribe, ", ")},
		{Key: "requested by", Value: c.RequestedBy()},
	}
	return fields, b.String()
}

func describeSensor(id, yamlText string, sensor map[string]any, c OperatorCaller) ([]OperatorField, string) {
	schedule := "manual only: it runs when polled (sensor_poll or Run now)"
	if v := fmt.Sprint(sensor["interval"]); sensor["interval"] != nil && v != "" && v != "0" {
		schedule = "every " + v
	}
	if cron := strOf(sensor, "cron"); cron != "" {
		schedule = "cron `" + cron + "`"
	}
	lang := strOf(sensor, "lang")

	var b strings.Builder
	fmt.Fprintf(&b, "%s asked to create the sensor `%s` (file `sensors/%s.yaml`). It is only created if you approve; denying tells the client it was refused. Nothing is overwritten.\n\n", c.Label(), id, id)
	if d := strOf(sensor, "description"); d != "" {
		b.WriteString(quote(d) + "\n\n")
	}
	fmt.Fprintf(&b, "- **Schedule:** %s\n- **Language:** %s\n- **Runs code on this Mac:** yes, with your permissions, each time it polls\n", schedule, lang)
	b.WriteString("- **When it polls:** only while an enabled flow subscribes to it (or when polled by hand)\n")
	b.WriteString("\nThe spec:\n\n" + fence("yaml", yamlText) + "\n")

	return []OperatorField{
		{Key: "sensor id", Value: id},
		{Key: "language", Value: lang},
		{Key: "schedule", Value: strings.NewReplacer("`", "").Replace(schedule)},
		{Key: "requested by", Value: c.RequestedBy()},
	}, b.String()
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

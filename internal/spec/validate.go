package spec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"hal9001/internal/transform"
)

// Refs is what a file may refer to elsewhere in the home dir. A nil map means
// "unknown, do not check that kind of reference"; an empty non-nil map means
// nothing exists.
type Refs struct {
	Sensors  map[string]bool
	Prompts  map[string]bool
	Runtimes map[string]Runtime
	// Tools are the custom tool ids (valid or not). Nil skips the check of an agent node's tools.
	Tools map[string]bool
}

var (
	idRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	portRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:\d{1,5})?$`)
)

// ValidID reports whether id is usable as a sensor, flow, node or prompt id
// (and therefore as a file name and state directory name).
func ValidID(id string) bool { return idRE.MatchString(id) }

func (c *collector) id(path, what, id string) bool {
	switch {
	case id == "":
		c.errorf(CodeRequired, path, "%s is required", what)
	case !ValidID(id):
		c.errorf(CodeInvalidID, path, "%s %q is invalid: use letters, digits, - and _ (max 64, starting with a letter or digit)", what, id)
	default:
		return true
	}
	return false
}

func (c *collector) enum(path, val string, allowed []string, required bool) {
	if val == "" {
		if required {
			c.errorf(CodeRequired, path, "%s is required (one of %s)", lastSegment(path), strings.Join(allowed, " | "))
		}
		return
	}
	if !slices.Contains(allowed, val) {
		c.errorf(CodeInvalidEnum, path, "%s %q is not valid (one of %s)", lastSegment(path), val, strings.Join(allowed, " | "))
	}
}

func lastSegment(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func (c *collector) template(path, text string) {
	if te := CheckTemplate(path, text); te != nil {
		c.errorf(CodeInvalidTemplate, path, "invalid template: %s", te.Msg)
	}
}

func (c *collector) positiveDuration(path string, d Duration, what string) {
	if d < 0 {
		c.errorf(CodeInvalidValue, path, "%s must not be negative", what)
	}
}

func (c *collector) regexps(path string, exprs []string) {
	for i, e := range exprs {
		if _, err := regexp.Compile(e); err != nil {
			c.errorf(CodeInvalidRegex, fmt.Sprintf("%s[%d]", path, i), "invalid regular expression %q: %s", e, regexpMsg(err))
		}
	}
}

func regexpMsg(err error) string { return strings.TrimPrefix(err.Error(), "error parsing regexp: ") }

// cwd validates a working-directory selection. Command nodes and sensors only
// support dir mode.
func (c *collector) cwd(path string, cw *Cwd, dirOnly bool) {
	if cw == nil {
		return
	}
	switch cw.Mode {
	case "":
		c.errorf(CodeRequired, path+".mode", "cwd.mode is required (dir | worktree)")
		return
	case CwdDir:
		if cw.Path == "" {
			c.errorf(CodeRequired, path+".path", "cwd.path is required for mode dir")
		}
		if cw.Repo != "" {
			c.errorf(CodeFieldNotAllowed, path+".repo", "cwd.repo is only valid with mode worktree")
		}
		if cw.Ref != "" {
			c.errorf(CodeFieldNotAllowed, path+".ref", "cwd.ref is only valid with mode worktree")
		}
	case CwdWorktree:
		if dirOnly {
			c.errorf(CodeFieldNotAllowed, path+".mode", "cwd.mode worktree is only valid on agent nodes")
			return
		}
		if cw.Repo == "" {
			c.errorf(CodeRequired, path+".repo", "cwd.repo is required for mode worktree")
		}
		if cw.Ref == "" {
			c.errorf(CodeRequired, path+".ref", "cwd.ref is required for mode worktree")
		}
		if cw.Path != "" {
			c.errorf(CodeFieldNotAllowed, path+".path", "cwd.path is only valid with mode dir")
		}
	default:
		c.errorf(CodeInvalidEnum, path+".mode", "cwd.mode %q is not valid (one of dir | worktree)", cw.Mode)
		return
	}
	c.template(path+".path", cw.Path)
	c.template(path+".repo", cw.Repo)
	c.template(path+".ref", cw.Ref)
}

// runSpec validates the lang/script/command trio shared by sensors and
// command nodes.
func (c *collector) runSpec(prefix, lang, script string, command []string) {
	c.enum(prefix+"lang", lang, Languages, true)
	if lang == "" || !slices.Contains(Languages, lang) {
		return
	}
	if lang == LangExec {
		if len(command) == 0 {
			c.errorf(CodeRequired, prefix+"command", "command (argv) is required when lang is exec")
		} else if command[0] == "" {
			c.errorf(CodeInvalidValue, prefix+"command[0]", "command[0] (the program) must not be empty")
		}
		if script != "" {
			c.errorf(CodeFieldNotAllowed, prefix+"script", "script is not valid when lang is exec; use command")
		}
		return
	}
	if strings.TrimSpace(script) == "" {
		c.errorf(CodeRequired, prefix+"script", "script is required when lang is %s", lang)
	}
	if len(command) > 0 {
		c.errorf(CodeFieldNotAllowed, prefix+"command", "command is only valid when lang is exec")
	}
}

// ValidateConfig checks a Config. Defaults are applied to a copy first, so
// callers may pass either parsed or defaulted values.
func ValidateConfig(cfg *Config) []Diagnostic {
	g := cfg.Clone()
	g.ApplyDefaults()
	c := &collector{}
	for _, f := range []struct {
		path string
		v    int
	}{
		{"max_agent_runs", g.MaxAgentRuns}, {"max_command_runs", g.MaxCommandRuns},
		{"retention_days", g.RetentionDays}, {"dedup_retention_days", g.DedupRetentionDays},
	} {
		if f.v < 0 {
			c.errorf(CodeInvalidValue, f.path, "%s must be positive, got %d", f.path, f.v)
		}
	}
	if g.DedupRetentionDays > 0 && g.RetentionDays > 0 && g.DedupRetentionDays < g.RetentionDays {
		c.warnf(CodeInvalidValue, "dedup_retention_days", "dedup_retention_days (%d) is shorter than retention_days (%d): pruned items could re-fire", g.DedupRetentionDays, g.RetentionDays)
	}
	c.enum("notify_min_priority", g.NotifyMinPriority, Priorities, false)
	c.positiveDuration("approval_timeout", g.ApprovalTimeout, "approval_timeout")
	c.positiveDuration("session_ttl", g.SessionTTL, "session_ttl")
	for i, h := range g.AllowedHosts {
		if !hostRE.MatchString(h) {
			c.errorf(CodeInvalidValue, fmt.Sprintf("allowed_hosts[%d]", i), "allowed host %q must be a bare hostname with optional port (no scheme or path)", h)
		}
	}
	for i, l := range g.AllowedLogins {
		if strings.TrimSpace(l) == "" {
			c.errorf(CodeInvalidValue, fmt.Sprintf("allowed_logins[%d]", i), "allowed login must not be empty")
		}
	}
	for _, name := range sortedKeys(g.Runtimes) {
		rt := g.Runtimes[name]
		p := "runtimes." + name
		if !ValidID(name) {
			c.errorf(CodeInvalidID, p, "runtime name %q is invalid: use letters, digits, - and _", name)
		}
		switch {
		case len(rt.Command) == 0:
			c.errorf(CodeRequired, p+".command", "runtime %q needs a command (argv)", name)
		case rt.Command[0] == "":
			c.errorf(CodeInvalidValue, p+".command[0]", "runtime %q: command[0] (the program) must not be empty", name)
		}
		c.enum(p+".kind", rt.Kind, []string{"claude", "codex", "omp"}, false)
		validateEnvNames(c, p+".env", rt.Env)
		validateEnvNames(c, p+".ask.env", rt.Ask.Env)
		validateEnvNames(c, p+".full.env", rt.Full.Env)
		for _, side := range []struct {
			path  string
			spawn RuntimeSpawn
		}{{p + ".ask.args", rt.Ask}, {p + ".full.args", rt.Full}} {
			for i, a := range side.spawn.Args {
				if a == "" {
					c.errorf(CodeInvalidValue, fmt.Sprintf("%s[%d]", side.path, i), "runtime %q: argument must not be empty", name)
				}
			}
		}
		if len(rt.SessionMeta) > 0 {
			if _, err := json.Marshal(rt.SessionMeta); err != nil {
				c.errorf(CodeInvalidValue, p+".session_meta", "runtime %q: session_meta must be JSON-compatible: %v", name, err)
			}
		}
	}
	validateAuth(c, &g.Auth)
	return c.ds
}

// validateEnvNames checks that every key of env is a legal variable name.
func validateEnvNames(c *collector, path string, env map[string]string) {
	for _, k := range sortedKeys(env) {
		if !envKeyRE.MatchString(k) {
			c.errorf(CodeInvalidValue, path+"."+k, "invalid environment variable name %q (letters, digits and _, not starting with a digit)", k)
		}
	}
}

func validateAuth(c *collector, a *Auth) {
	controls := []string{ControlRequired, ControlRequisite, ControlSufficient, ControlOptional}
	for _, name := range sortedKeys(a.Stacks) {
		stack := a.Stacks[name]
		p := "auth.stacks." + name
		if len(stack) == 0 {
			c.warnf(CodeInvalidValue, p, "stack %q has no modules: every request using it will be denied", name)
		}
		for i, m := range stack {
			mp := fmt.Sprintf("%s[%d]", p, i)
			switch {
			case m.Module == "":
				c.errorf(CodeRequired, mp+".module", "module is required")
			case !slices.Contains(KnownAuthModules, m.Module):
				c.errorf(CodeUnknownModule, mp+".module", "unknown auth module %q (known: %s)", m.Module, strings.Join(KnownAuthModules, ", "))
			}
			c.enum(mp+".control", m.Control, controls, true)
		}
	}
	for _, svc := range sortedKeys(a.Services) {
		p := "auth.services." + svc
		if !slices.Contains(Services, svc) {
			c.errorf(CodeInvalidEnum, p, "unknown service %q (one of %s)", svc, strings.Join(Services, " | "))
			continue
		}
		if _, ok := a.Stacks[a.Services[svc]]; !ok {
			c.errorf(CodeUnknownStack, p, "service %q uses stack %q, which is not defined under auth.stacks", svc, a.Services[svc])
		}
	}
}

// ValidateSensor checks a Sensor (not its id against a file name; see
// CheckSensor).
func ValidateSensor(s *Sensor) []Diagnostic {
	g := s.Clone()
	g.ApplyDefaults()
	c := &collector{}
	c.id("id", "id", g.ID)
	if _, err := g.Schedule(); err != nil {
		path := "interval"
		if g.Cron != "" {
			path = "cron"
		}
		c.errorf(CodeInvalidSchedule, path, "%s", err)
	}
	c.runSpec("", g.Lang, g.Script, g.Command)
	if g.Cwd != "" {
		c.template("cwd", g.Cwd)
	}
	if g.Key == "" {
		c.errorf(CodeRequired, "key", "key is required: a template over one item that yields its dedup key, for example {{.id}}")
	} else {
		c.template("key", g.Key)
	}
	c.enum("first_run", g.FirstRun, []string{FirstRunBaseline, FirstRunFire}, false)
	c.positiveDuration("timeout", g.Timeout, "timeout")
	return c.ds
}

// ValidatePrompt checks a Prompt.
func ValidatePrompt(p *Prompt) []Diagnostic {
	c := &collector{}
	c.id("name", "name", p.Name)
	c.enum("output", p.Output, []string{OutputNote, OutputNone}, false)
	if strings.TrimSpace(p.Body) == "" {
		c.errorf(CodeRequired, "body", "prompt body is empty")
	} else {
		c.template("body", p.Body)
	}
	return c.ds
}

var nodeFieldsAllowed = map[string][]string{
	NodeCommand:   {"lang", "ports", "script", "command", "cwd", "timeout", "side_effect"},
	NodeAgent:     {"runtime", "mode", "approve", "prompt", "instructions", "output", "extra_prompt", "model", "cwd", "timeout", "approval_timeout", "allow", "mcp_servers", "sandbox", "image", "repo", "ref", "tools"},
	NodeInbox:     {"title", "priority", "summary_md", "suggested_reply", "links"},
	NodeTransform: {"steps", "timeout"},
}

// setFields lists the optional node fields that are set, in struct order.
func (n *Node) setFields() []string {
	var s []string
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"lang", n.Lang != ""}, {"ports", len(n.Ports) > 0}, {"script", n.Script != ""}, {"command", len(n.Command) > 0},
		{"steps", len(n.Steps) > 0},
		{"runtime", n.Runtime != ""}, {"mode", n.Mode != ""}, {"approve", n.Approve != ""}, {"prompt", n.Prompt != ""},
		{"instructions", n.Instructions != ""}, {"output", n.Output != ""}, {"extra_prompt", n.ExtraPrompt != ""}, {"model", n.Model != ""},
		{"title", n.Title != ""}, {"priority", n.Priority != ""}, {"summary_md", n.SummaryMD != ""},
		{"suggested_reply", n.SuggestedReply != ""}, {"links", len(n.Links) > 0},
		{"cwd", n.Cwd != nil}, {"timeout", n.Timeout != 0}, {"approval_timeout", n.ApprovalTimeout != 0},
		{"side_effect", n.SideEffect}, {"allow", n.Allow != nil}, {"mcp_servers", len(n.MCPServers) > 0},
		{"sandbox", n.Sandbox != ""}, {"image", n.Image != ""}, {"repo", n.Repo != ""}, {"ref", n.Ref != ""}, {"tools", len(n.Tools) > 0},
	} {
		if f.set {
			s = append(s, f.name)
		}
	}
	return s
}

// ValidateFlow checks a flow: field validity per node type, references
// (against refs when non-nil), edges, ports, templates, and that the graph is a
// DAG whose every node is reachable from an entry node. Defaults are applied
// to a copy first.
func ValidateFlow(f *Flow, refs *Refs) []Diagnostic {
	if refs == nil {
		refs = &Refs{}
	}
	g := f.Clone()
	g.ApplyDefaults()
	c := &collector{}
	c.id("id", "id", g.ID)
	if g.MaxConcurrent < 0 {
		c.errorf(CodeInvalidValue, "max_concurrent", "max_concurrent must be positive, got %d", g.MaxConcurrent)
	}
	if len(g.Subscribe) == 0 {
		c.warnf(CodeNoSubscription, "subscribe", "flow subscribes to no sensor and will never run")
	}
	seenSub := map[string]bool{}
	for i, s := range g.Subscribe {
		p := fmt.Sprintf("subscribe[%d]", i)
		if seenSub[s] {
			c.warnf(CodeDuplicateID, p, "sensor %q is subscribed twice", s)
		}
		seenSub[s] = true
		if refs.Sensors != nil && !refs.Sensors[s] {
			c.errorf(CodeUnknownSensor, p, "unknown sensor %q%s", s, availableList(keysOf(refs.Sensors)))
		}
	}
	if len(g.Nodes) == 0 {
		c.warnf(CodeEmptyFlow, "nodes", "flow has no nodes")
	}

	index := map[string]int{} // node id -> first declaration
	for i := range g.Nodes {
		n := &g.Nodes[i]
		c.node = n.ID
		p := fmt.Sprintf("nodes[%d]", i)
		if c.id(p+".id", "node id", n.ID) {
			if first, dup := index[n.ID]; dup {
				c.errorf(CodeDuplicateID, p+".id", "duplicate node id %q (first used by nodes[%d])", n.ID, first)
			} else {
				index[n.ID] = i
			}
		}
		validateNode(c, p, n, refs)
	}
	c.node = ""

	validateEdges(c, &g, index)
	return c.ds
}

func validateNode(c *collector, p string, n *Node, refs *Refs) {
	allowed, known := nodeFieldsAllowed[n.Type]
	switch {
	case n.Type == "":
		c.errorf(CodeRequired, p+".type", "type is required (command | agent | inbox | transform)")
	case !known:
		c.errorf(CodeInvalidEnum, p+".type", "type %q is not valid (one of command | agent | inbox | transform)", n.Type)
	}
	if !known {
		return
	}
	for _, name := range n.setFields() {
		if !slices.Contains(allowed, name) {
			c.errorf(CodeFieldNotAllowed, p+"."+name, "field %q is not valid on %s nodes", name, n.Type)
		}
	}
	c.positiveDuration(p+".timeout", n.Timeout, "timeout")
	switch n.Type {
	case NodeCommand:
		c.runSpec(p+".", n.Lang, n.Script, n.Command)
		c.cwd(p+".cwd", n.Cwd, true)
		validatePorts(c, p, n.Ports)
	case NodeAgent:
		validateAgent(c, p, n, refs)
	case NodeInbox:
		validateInbox(c, p, n)
	case NodeTransform:
		validateTransform(c, p, n)
	}
}

// validateTransform reports the transform steps' problems with paths
// like nodes[2].steps[1].conditions[0].value.
func validateTransform(c *collector, p string, n *Node) {
	for _, is := range transform.Validate(n.Steps) {
		sev := SeverityError
		if is.Severity == transform.SeverityWarning {
			sev = SeverityWarning
		}
		c.add(sev, is.Code, p+"."+is.Path, "%s", is.Message)
	}
}

func validatePorts(c *collector, p string, ports []string) {
	seen := map[string]bool{}
	for i, port := range ports {
		pp := fmt.Sprintf("%s.ports[%d]", p, i)
		switch {
		case port == PortError || port == PortDenied:
			c.errorf(CodeReservedPort, pp, "port %q is implicit and cannot be declared", port)
		case !portRE.MatchString(port):
			c.errorf(CodeInvalidID, pp, "port name %q is invalid: use a letter followed by letters, digits, - or _", port)
		case seen[port]:
			c.errorf(CodeDuplicateID, pp, "port %q is declared twice", port)
		}
		seen[port] = true
	}
}

func validateAgent(c *collector, p string, n *Node, refs *Refs) {
	if n.Runtime == "" {
		c.errorf(CodeRequired, p+".runtime", "runtime is required (a key of runtimes in config.yaml)")
	} else if refs.Runtimes != nil {
		if _, ok := refs.Runtimes[n.Runtime]; !ok {
			c.errorf(CodeUnknownRuntime, p+".runtime", "unknown runtime %q%s", n.Runtime, availableList(sortedKeys(refs.Runtimes)))
		}
	}
	c.enum(p+".mode", n.Mode, []string{ModeAuto, ModeApprove, ModeReadonly}, true)
	if n.Approve != "" {
		c.enum(p+".approve", n.Approve, []string{ApproveProposal, ApprovePerTool}, false)
		if n.Mode != ModeApprove {
			c.errorf(CodeFieldNotAllowed, p+".approve", "approve is only valid with mode approve")
		}
	}
	switch {
	case n.Prompt != "" && n.Instructions != "":
		c.errorf(CodeFieldNotAllowed, p+".instructions", "set either prompt (a file in prompts/) or instructions (inline), not both")
	case n.Prompt == "" && n.Instructions == "":
		c.errorf(CodeRequired, p+".instructions", "set instructions (inline prompt) or prompt (a file in prompts/)")
	}
	if n.Output != "" {
		c.enum(p+".output", n.Output, []string{OutputNote, OutputNone}, false)
		if n.Instructions == "" {
			c.errorf(CodeFieldNotAllowed, p+".output", "output is only valid with instructions; a prompt file sets its own output")
		}
	}
	c.template(p+".instructions", n.Instructions)
	if n.Prompt != "" {
		if !ValidID(n.Prompt) {
			c.errorf(CodeInvalidID, p+".prompt", "prompt name %q is invalid", n.Prompt)
		} else if refs.Prompts != nil && !refs.Prompts[n.Prompt] {
			c.errorf(CodeUnknownPrompt, p+".prompt", "unknown prompt %q%s", n.Prompt, availableList(keysOf(refs.Prompts)))
		}
	}
	c.template(p+".extra_prompt", n.ExtraPrompt)
	c.cwd(p+".cwd", n.Cwd, false)
	c.positiveDuration(p+".approval_timeout", n.ApprovalTimeout, "approval_timeout")
	if n.Allow != nil {
		c.regexps(p+".allow.tools", n.Allow.Tools)
	}
	seenTools := map[string]bool{}
	for i, t := range n.Tools {
		tp := fmt.Sprintf("%s.tools[%d]", p, i)
		switch {
		case !ValidID(t.ID):
			c.errorf(CodeInvalidID, tp, "tool id %q is invalid", t.ID)
		case seenTools[t.ID]:
			c.errorf(CodeDuplicateID, tp, "tool %q is listed twice", t.ID)
		case refs.Tools != nil && !refs.Tools[t.ID]:
			c.errorf(CodeUnknownTool, tp, "unknown tool %q%s", t.ID, availableList(keysOf(refs.Tools)))
		}
		seenTools[t.ID] = true
		if m := t.Approve; m != nil && m.Mode != ToolApproveNever && m.Mode != ToolApproveAlways && m.Mode != ToolApproveWhen {
			c.errorf(CodeInvalidEnum, tp+".approve", "approve must be never, always or {when: ...}")
		}
	}
	seen := map[string]bool{}
	for i, m := range n.MCPServers {
		mp := fmt.Sprintf("%s.mcp_servers[%d]", p, i)
		switch {
		case m.Name == "":
			c.errorf(CodeRequired, mp+".name", "mcp server name is required")
		case seen[m.Name]:
			c.errorf(CodeDuplicateID, mp+".name", "mcp server %q is declared twice", m.Name)
		case m.Name == "hal":
			c.errorf(CodeInvalidValue, mp+".name", "mcp server name \"hal\" is reserved for hal's own tools")
		}
		seen[m.Name] = true
		c.enum(mp+".type", m.Type, []string{MCPStdio, MCPHTTP, MCPSSE}, true)
		switch m.Type {
		case MCPStdio:
			if m.Command == "" {
				c.errorf(CodeRequired, mp+".command", "command is required for a stdio mcp server")
			}
			for field, set := range map[string]bool{"url": m.URL != "", "headers": len(m.Headers) > 0} {
				if set {
					c.errorf(CodeFieldNotAllowed, mp+"."+field, "%s is not valid on a stdio mcp server", field)
				}
			}
		case MCPHTTP, MCPSSE:
			if m.URL == "" {
				c.errorf(CodeRequired, mp+".url", "url is required for a %s mcp server", m.Type)
			}
			for field, set := range map[string]bool{"command": m.Command != "", "args": len(m.Args) > 0, "env": len(m.Env) > 0} {
				if set {
					c.errorf(CodeFieldNotAllowed, mp+"."+field, "%s is not valid on a %s mcp server", field, m.Type)
				}
			}
		}
	}
}

func validateInbox(c *collector, p string, n *Node) {
	if n.Title == "" {
		c.errorf(CodeRequired, p+".title", "title is required")
	}
	c.template(p+".title", n.Title)
	c.enum(p+".priority", n.Priority, Priorities, false)
	c.template(p+".summary_md", n.SummaryMD)
	c.template(p+".suggested_reply", n.SuggestedReply)
	for i, l := range n.Links {
		lp := fmt.Sprintf("%s.links[%d]", p, i)
		if l.Label == "" {
			c.errorf(CodeRequired, lp+".label", "link label is required")
		}
		if l.URL == "" {
			c.errorf(CodeRequired, lp+".url", "link url is required")
		}
		c.template(lp+".label", l.Label)
		c.template(lp+".url", l.URL)
	}
}

type edgeRef struct{ to, edge int }

func validateEdges(c *collector, g *Flow, index map[string]int) {
	adj := make([][]edgeRef, len(g.Nodes))
	hasIncoming := make([]bool, len(g.Nodes))
	seen := map[Edge]int{}
	for i, e := range g.Edges {
		p := fmt.Sprintf("edges[%d]", i)
		from, fromOK := index[e.From]
		to, toOK := index[e.To]
		c.node = e.From
		if e.From == "" {
			c.errorf(CodeRequired, p+".from", "edge from is required")
		} else if !fromOK {
			c.errorf(CodeUnknownNode, p+".from", "edge from unknown node %q", e.From)
		}
		c.node = e.To
		if e.To == "" {
			c.errorf(CodeRequired, p+".to", "edge to is required")
		} else if !toOK {
			c.errorf(CodeUnknownNode, p+".to", "edge to unknown node %q", e.To)
		}
		c.node = e.From
		if fromOK {
			ports := g.Nodes[from].OutPorts()
			if !slices.Contains(ports, e.Port) {
				c.errorf(CodeUndeclaredPort, p+".port", "node %q has no port %q (ports: %s)", e.From, e.Port, strings.Join(ports, ", "))
			}
		}
		c.node = ""
		if first, dup := seen[e]; dup {
			c.errorf(CodeDuplicateEdge, p, "duplicate of edges[%d]", first)
			continue
		}
		seen[e] = i
		if fromOK && toOK {
			adj[from] = append(adj[from], edgeRef{to: to, edge: i})
			hasIncoming[to] = true
		}
	}

	// Cycle detection by depth-first search; each back edge reports its cycle.
	const (
		white = iota
		grey
		black
	)
	state := make([]int, len(g.Nodes))
	var stack []int
	var visit func(int)
	visit = func(u int) {
		state[u] = grey
		stack = append(stack, u)
		for _, r := range adj[u] {
			switch state[r.to] {
			case white:
				visit(r.to)
			case grey:
				start := slices.Index(stack, r.to)
				names := make([]string, 0, len(stack)-start+1)
				for _, s := range stack[start:] {
					names = append(names, g.Nodes[s].ID)
				}
				names = append(names, g.Nodes[r.to].ID)
				c.node = g.Nodes[r.to].ID
				c.errorf(CodeCycle, fmt.Sprintf("edges[%d]", r.edge), "flow graph has a cycle: %s", strings.Join(names, " -> "))
				c.node = ""
			}
		}
		stack = stack[:len(stack)-1]
		state[u] = black
	}
	for i := range g.Nodes {
		if state[i] == white && index[g.Nodes[i].ID] == i {
			visit(i)
		}
	}

	// Reachability from entry nodes (nodes with no incoming edge).
	reached := make([]bool, len(g.Nodes))
	var queue []int
	for i := range g.Nodes {
		if !hasIncoming[i] && index[g.Nodes[i].ID] == i {
			reached[i] = true
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, r := range adj[u] {
			if !reached[r.to] {
				reached[r.to] = true
				queue = append(queue, r.to)
			}
		}
	}
	for i := range g.Nodes {
		if !reached[i] && index[g.Nodes[i].ID] == i {
			c.node = g.Nodes[i].ID
			c.errorf(CodeUnreachable, fmt.Sprintf("nodes[%d]", i), "node %q is unreachable: no entry node (a node without incoming edges) leads to it", g.Nodes[i].ID)
		}
	}
	c.node = ""
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func availableList(names []string) string {
	if len(names) == 0 {
		return " (none defined)"
	}
	return " (available: " + strings.Join(names, ", ") + ")"
}

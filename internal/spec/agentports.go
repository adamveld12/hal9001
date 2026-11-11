package spec

import (
	"fmt"
	"regexp"
	"strings"

	"hal9001/internal/outschema"
)

// agentPortRE is the name of an agent port: it becomes part of the tool name emit_<port>.
var agentPortRE = regexp.MustCompile(`^[a-z0-9_]{1,59}$`)

// EmitToolPrefix starts the name of the tool an agent calls to finish through a port.
const EmitToolPrefix = "emit_"

// EmitToolName is the tool of an agent node's port.
func EmitToolName(port string) string { return EmitToolPrefix + port }

// validateAgentPorts checks the ports and emit fields of an agent node (see docs/flows.md, "Output ports").
func validateAgentPorts(c *collector, p string, n *Node, refs *Refs) {
	if n.Emit != "" {
		c.enum(p+".emit", n.Emit, []string{EmitOne, EmitMany}, false)
		if n.Ports.IsZero() {
			c.errorf(CodeFieldNotAllowed, p+".emit", "emit is only valid with ports")
		}
	}
	if n.Ports.IsZero() {
		return
	}
	if !n.Ports.Map {
		c.errorf(CodeInvalidValue, p+".ports", "an agent node's ports map each name to {description, schema}, for example ports: {reviewed: {description: ..., schema: {...}}}")
	}
	switch {
	case n.Output == OutputNote || n.Output == OutputNone:
		c.errorf(CodeFieldNotAllowed, p+".ports", "ports cannot be combined with output: %s: the agent returns its result through the emit_<port> tools", n.Output)
	case n.Prompt != "" && refs.PromptOutputs[n.Prompt] == OutputNote:
		c.errorf(CodeFieldNotAllowed, p+".ports", "ports cannot be combined with prompt %q, whose output is note: the agent returns its result through the emit_<port> tools", n.Prompt)
	}
	seen := map[string]bool{}
	for _, d := range n.Ports.Decls {
		pp := p + ".ports." + d.Name
		switch {
		case d.Name == PortError || d.Name == PortDenied || d.Name == PortOut:
			c.errorf(CodeReservedPort, pp, "port %q is reserved and cannot be declared on an agent node", d.Name)
		case !agentPortRE.MatchString(d.Name):
			c.errorf(CodeInvalidID, pp, "port name %q is invalid: use lowercase letters, digits and _ (it becomes the tool %s)", d.Name, EmitToolName(d.Name))
		case seen[d.Name]:
			c.errorf(CodeDuplicateID, pp, "port %q is declared twice", d.Name)
		}
		seen[d.Name] = true
		if len(d.Schema) > 0 {
			if _, err := outschema.Compile(d.Schema); err != nil {
				c.errorf(CodeInvalidValue, pp+".schema", "invalid schema: %s", err)
			}
		}
	}
}

// validateAgentPortEdges checks what the edges of the flow make of each agent node's ports: a port without a schema
// must lead only to inbox nodes, a port without an edge has no tool, and templates should read declared fields.
func validateAgentPortEdges(c *collector, g *Flow, index map[string]int) {
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Type != NodeAgent || n.Ports.IsZero() || index[n.ID] != i {
			continue
		}
		c.node = n.ID
		for _, d := range n.Ports.Decls {
			pp := fmt.Sprintf("nodes[%d].ports.%s", i, d.Name)
			var targets []*Node
			for _, e := range g.Edges {
				if e.From != n.ID || e.Port != d.Name {
					continue
				}
				if j, ok := index[e.To]; ok {
					targets = append(targets, &g.Nodes[j])
				}
			}
			if len(targets) == 0 {
				c.warnf(CodeUnusedPort, pp, "port %q has no outgoing edge, so the agent gets no %s tool for it; connect it or remove it", d.Name, EmitToolName(d.Name))
				continue
			}
			schema := d.Schema
			if len(schema) == 0 {
				if !allInbox(targets) {
					c.errorf(CodePortNeedsSchema, pp+".schema", "port %q has no schema, which is only allowed when every edge from it goes directly to an inbox node (then the inbox note schema is used); add a schema", d.Name)
					continue
				}
				schema = JSONSchema(outschema.NoteDoc())
			}
			warnMissingFields(c, g, index, n, d.Name, schema)
		}
	}
	c.node = ""
}

func allInbox(ns []*Node) bool {
	for _, n := range ns {
		if n.Type != NodeInbox {
			return false
		}
	}
	return len(ns) > 0
}

// outputFieldRE finds .input.agent.output.<field> in a template.
var outputFieldRE = regexp.MustCompile(`\.input\.agent\.output\.([A-Za-z_][A-Za-z0-9_]*)`)

// warnMissingFields warns for every template of a node directly after the port that reads a field the port's schema
// does not declare. It is a best-effort text scan (templates only, not jq or commands).
func warnMissingFields(c *collector, g *Flow, index map[string]int, agent *Node, port string, schema JSONSchema) {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return
	}
	switch ap := schema["additionalProperties"].(type) {
	case bool:
		if ap {
			return
		}
	case map[string]any:
		return
	}
	for _, e := range g.Edges {
		if e.From != agent.ID || e.Port != port {
			continue
		}
		j, ok := index[e.To]
		if !ok {
			continue
		}
		down := &g.Nodes[j]
		prev := c.node
		c.node = down.ID
		for _, t := range templateFields(fmt.Sprintf("nodes[%d]", j), down) {
			for _, m := range outputFieldRE.FindAllStringSubmatch(t.text, -1) {
				if _, ok := props[m[1]]; !ok {
					c.warnf(CodeTemplateField, t.path, "the template reads .input.agent.output.%s, which port %q of %q does not declare in its schema", m[1], port, agent.ID)
				}
			}
		}
		c.node = prev
	}
}

type templateText struct{ path, text string }

// templateFields lists the template texts of a node.
func templateFields(p string, n *Node) []templateText {
	var out []templateText
	add := func(field, text string) {
		if text != "" {
			out = append(out, templateText{p + "." + field, text})
		}
	}
	switch n.Type {
	case NodeInbox:
		add("title", n.Title)
		add("priority", n.Priority)
		add("summary_md", n.SummaryMD)
		add("suggested_reply", n.SuggestedReply)
		for i, l := range n.Links {
			add(fmt.Sprintf("links[%d].label", i), l.Label)
			add(fmt.Sprintf("links[%d].url", i), l.URL)
		}
	case NodeApproval:
		add("title", n.Title)
		add("body", n.Body)
	case NodeAgent:
		add("instructions", n.Instructions)
		add("extra_prompt", n.ExtraPrompt)
	}
	if n.Cwd != nil && n.Type != NodeCommand {
		add("cwd.path", n.Cwd.Path)
		add("cwd.repo", n.Cwd.Repo)
		add("cwd.ref", n.Cwd.Ref)
	}
	return out
}

// IsTemplate reports that s contains a template action.
func IsTemplate(s string) bool { return strings.Contains(s, "{{") }

// validateInboxTitles requires the title of every inbox node, except one whose incoming edges all come from agent ports
// without a schema (the inbox shortcut): its title and summary default to the agent's emission.
func validateInboxTitles(c *collector, g *Flow, index map[string]int) {
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Type != NodeInbox || n.Title != "" || index[n.ID] != i {
			continue
		}
		if !fedByShortcutPorts(g, index, n) {
			c.node = n.ID
			c.errorf(CodeRequired, fmt.Sprintf("nodes[%d].title", i), "title is required")
			c.node = ""
		}
	}
}

// fedByShortcutPorts reports that the node has incoming edges and every one comes from an agent port that has no schema.
func fedByShortcutPorts(g *Flow, index map[string]int, n *Node) bool {
	fed := false
	for _, e := range g.Edges {
		if e.To != n.ID {
			continue
		}
		j, ok := index[e.From]
		if !ok || g.Nodes[j].Type != NodeAgent {
			return false
		}
		d, ok := g.Nodes[j].Ports.Get(e.Port)
		if !ok || len(d.Schema) > 0 {
			return false
		}
		fed = true
	}
	return fed
}

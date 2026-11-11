package spec

import (
	"regexp"
	"strings"

	"hal9001/internal/outschema"
)

// EmitTool is the emit_<port> tool of one declared port of an agent node. It exists for ports that have at least one
// outgoing edge; hal derives it from the flow graph at run time and nothing of it is stored in the flow file.
type EmitTool struct {
	Port string
	// Name is emit_<port>.
	Name string
	// Description is the port's description, or a default naming the port.
	Description string
	// LeadsTo says where the first edge from the port goes, e.g. `inbox note "Review: ..."`.
	LeadsTo string
	// Schema is the input schema of the tool: the declared schema, or the inbox note schema for a port without one
	// (valid only when every edge from the port goes directly to an inbox node).
	Schema JSONSchema
	// Shortcut reports the inbox note schema.
	Shortcut bool
}

// ToolDescription is the description of the tool as the agent's runtime lists it: the port description and the
// "Leads to" summary on one line.
func (t EmitTool) ToolDescription() string {
	return strings.TrimSpace(t.Description + " Leads to: " + t.LeadsTo + ".")
}

// EmitTools lists the tools of the declared ports of agent node n that have an outgoing edge, in declaration order.
// Call it on a flow that has had defaults applied.
func (f *Flow) EmitTools(n *Node) []EmitTool {
	var out []EmitTool
	for _, d := range n.Ports.Decls {
		to := f.firstTarget(n.ID, d.Name)
		if to == nil {
			continue
		}
		t := EmitTool{Port: d.Name, Name: EmitToolName(d.Name), Description: strings.TrimSpace(d.Description), LeadsTo: LeadsTo(to), Schema: d.Schema}
		if t.Description == "" {
			t.Description = "Finish through the port " + d.Name + "."
		}
		if len(t.Schema) == 0 {
			t.Schema, t.Shortcut = JSONSchema(outschema.NoteDoc()), true
		}
		out = append(out, t)
	}
	return out
}

// firstTarget is the node of the first edge (in file order) from the port, or nil.
func (f *Flow) firstTarget(from, port string) *Node {
	for _, e := range f.Edges {
		if e.From == from && (e.Port == port || (e.Port == "" && port == PortOut)) {
			return f.NodeByID(e.To)
		}
	}
	return nil
}

var templateActionRE = regexp.MustCompile(`(\{\{.*?\}\})+`)

// maxLeadsToTitle bounds the title quoted in a "Leads to" line.
const maxLeadsToTitle = 60

// LeadsTo describes a downstream node for an agent: its type, and for inbox and approval nodes its title with every
// template expression shown as "...".
func LeadsTo(n *Node) string {
	switch n.Type {
	case NodeInbox:
		return quoted("inbox note", n.Title)
	case NodeApproval:
		return quoted("approval", n.Title)
	}
	return n.Type + " node"
}

func quoted(kind, title string) string {
	title = strings.Join(strings.Fields(templateActionRE.ReplaceAllString(title, "...")), " ")
	if title == "" {
		return kind
	}
	if r := []rune(title); len(r) > maxLeadsToTitle {
		title = string(r[:maxLeadsToTitle]) + "..."
	}
	return kind + ` "` + title + `"`
}

// OutputsSection is the "## Outputs" section appended to the rendered prompt of an agent node with ports (many: one
// output per call). It starts with "## Outputs" and has no leading or trailing blank line.
func OutputsSection(tools []EmitTool, many bool) string {
	var b strings.Builder
	b.WriteString("## Outputs\n")
	if many {
		b.WriteString("Finish by calling one or more of these tools (each call is a separate output):\n")
	} else {
		b.WriteString("Finish by calling exactly one of these tools:\n")
	}
	for _, t := range tools {
		b.WriteString("- " + t.Name + ": " + t.Description + "\n")
		b.WriteString("  Leads to: " + t.LeadsTo + "\n")
	}
	b.WriteString("Do not put the output in your final message; it is ignored.")
	return b.String()
}

package assist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"hal9001/internal/spec"
)

// scriptID is what a test of a draft executes with the user's credentials: the interpreter, the script or argv,
// the working directory, and which of the credentials available to the sensor the code mentions. Two drafts with
// the same scriptID run the same code on the same secrets; anything else in the draft (interval, key, timeout,
// first_run) does not change what a test executes.
type scriptID struct {
	Lang    string   `json:"lang"`
	Script  string   `json:"script,omitempty"`
	Command []string `json:"command,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	// Env are the names, from those available to the sensor (global env file plus the target sensor's own), that
	// the script, command or cwd mentions. Adding a credential the script already mentions changes the identity.
	Env []string `json:"env,omitempty"`
	// Flow marks the identity of a flow draft: Nodes are its command nodes (the other fields stay empty).
	Flow  bool         `json:"flow,omitempty"`
	Nodes []nodeScript `json:"nodes,omitempty"`
	// Sensors are draft sensors that run as part of the same test (a flow test fed by a sensor draft's output).
	Sensors []nodeScript `json:"sensors,omitempty"`
	// Tool marks the identity of a custom tool draft (Lang, Script, Command and Env are set).
	Tool bool `json:"tool,omitempty"`
}

// nodeScript is what one command node of a flow executes. ID names the node in the text shown to the user; it is
// not part of the identity, so renaming a node does not need a new approval.
type nodeScript struct {
	ID      string   `json:"-"`
	Lang    string   `json:"lang"`
	Script  string   `json:"script,omitempty"`
	Command []string `json:"command,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// scriptOfFlow returns the identity of a flow draft: every command node's lang, script, command, cwd and the
// credential names it mentions. Transform nodes are generated jq and do not count.
func scriptOfFlow(f *spec.Flow, available []string) scriptID {
	id := scriptID{Flow: true}
	for _, n := range f.Nodes {
		if n.Type != spec.NodeCommand {
			continue
		}
		ns := nodeScript{ID: n.ID, Lang: n.Lang, Script: n.Script, Command: n.Command}
		if n.Cwd != nil {
			ns.Cwd = strings.Join([]string{n.Cwd.Mode, n.Cwd.Path, n.Cwd.Repo, n.Cwd.Ref}, "\x00")
		}
		text := n.Script + "\n" + strings.Join(n.Command, "\n") + "\n" + ns.Cwd
		for _, name := range available {
			if mentions(text, name) {
				ns.Env = append(ns.Env, name)
			}
		}
		sort.Strings(ns.Env)
		id.Nodes = append(id.Nodes, ns)
	}
	return id
}

// empty reports whether the identity runs nothing with credentials (a flow without command nodes).
func (id scriptID) empty() bool { return id.Flow && len(id.Nodes) == 0 && len(id.Sensors) == 0 }

// withSensor adds a draft sensor's script to a flow identity: the flow test runs the sensor first to get its input.
func (id scriptID) withSensor(sensorID string, s *spec.Sensor, available []string) scriptID {
	one := scriptOf(s, available)
	id.Sensors = append(id.Sensors, nodeScript{ID: sensorID, Lang: one.Lang, Script: one.Script, Command: one.Command, Cwd: one.Cwd, Env: one.Env})
	return id
}

// scriptOf returns the identity of a sensor's script given the credential names available to it.
func scriptOf(s *spec.Sensor, available []string) scriptID {
	id := scriptID{Lang: s.Lang, Script: s.Script, Command: s.Command, Cwd: s.Cwd}
	text := s.Script + "\n" + strings.Join(s.Command, "\n") + "\n" + s.Cwd
	for _, name := range available {
		if mentions(text, name) {
			id.Env = append(id.Env, name)
		}
	}
	sort.Strings(id.Env)
	return id
}

// mentions reports whether text contains name as a whole identifier ($NAME, ${NAME}, os.environ["NAME"], ...).
func mentions(text, name string) bool {
	re, err := regexp.Compile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_])`)
	return err == nil && re.MatchString(text)
}

func (id scriptID) hash() string {
	b, _ := json.Marshal(id)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// text is the canonical rendering shown to the user and stored as the approved version, so a changed script can be
// shown as a diff against it.
func (id scriptID) text() string {
	var b strings.Builder
	if id.Flow {
		for _, n := range id.Nodes {
			fmt.Fprintf(&b, "# node %s, lang: %s\n", n.ID, n.Lang)
			if n.Cwd != "" {
				fmt.Fprintf(&b, "# cwd: %s\n", strings.ReplaceAll(n.Cwd, "\x00", " "))
			}
			if len(n.Env) == 0 {
				b.WriteString("# credentials referenced: none\n")
			} else {
				fmt.Fprintf(&b, "# credentials referenced: %s\n", strings.Join(n.Env, ", "))
			}
			if len(n.Command) > 0 {
				b.WriteString("# command (argv, one per line):\n")
				for _, a := range n.Command {
					b.WriteString(a + "\n")
				}
			}
			if n.Script != "" {
				b.WriteString(n.Script)
				if !strings.HasSuffix(n.Script, "\n") {
					b.WriteString("\n")
				}
			}
			b.WriteString("\n")
		}
		for _, n := range id.Sensors {
			fmt.Fprintf(&b, "# sensor %s, lang: %s\n", n.ID, n.Lang)
			if n.Cwd != "" {
				fmt.Fprintf(&b, "# cwd: %s\n", n.Cwd)
			}
			if len(n.Env) == 0 {
				b.WriteString("# credentials referenced: none\n")
			} else {
				fmt.Fprintf(&b, "# credentials referenced: %s\n", strings.Join(n.Env, ", "))
			}
			if len(n.Command) > 0 {
				b.WriteString("# command (argv, one per line):\n")
				for _, a := range n.Command {
					b.WriteString(a + "\n")
				}
			}
			if n.Script != "" {
				b.WriteString(n.Script)
				if !strings.HasSuffix(n.Script, "\n") {
					b.WriteString("\n")
				}
			}
			b.WriteString("\n")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "# lang: %s\n", id.Lang)
	if id.Cwd != "" {
		fmt.Fprintf(&b, "# cwd: %s\n", id.Cwd)
	}
	if len(id.Env) == 0 {
		b.WriteString("# credentials referenced: none\n")
	} else {
		fmt.Fprintf(&b, "# credentials referenced: %s\n", strings.Join(id.Env, ", "))
	}
	if len(id.Command) > 0 {
		b.WriteString("# command (argv, one per line):\n")
		for _, a := range id.Command {
			b.WriteString(a + "\n")
		}
	}
	if id.Script != "" {
		b.WriteString(id.Script)
		if !strings.HasSuffix(id.Script, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// pendingTest is an agent test of a changed script that waits for the user's decision.
type pendingTest struct {
	id scriptID
	// base is the text of the last version the user approved (or the saved sensor's script), "" when there is none.
	base string
}

// content is the description shown with the approval: why it asks, what runs, and the diff against the last
// approved version. It is stored as ACP tool call content on the tool message of the test.
func (p *pendingTest) content() []json.RawMessage {
	cred := "none of the credentials available to this sensor"
	env := p.id.Env
	subject, path := "the draft's script", "sensor script ("+p.id.Lang+")"
	if p.id.Tool {
		cred, path = "none of the credentials available to this tool", "tool script ("+p.id.Lang+")"
	}
	if p.id.Flow {
		subject, path = "the draft flow's command nodes", "flow command nodes"
		if len(p.id.Sensors) > 0 {
			subject, path = "the draft flow's command nodes and a draft sensor's script", "flow command nodes and sensor script"
		}
		cred = "none of the credentials available to command nodes"
		env = nil
		for _, n := range append(append([]nodeScript(nil), p.id.Nodes...), p.id.Sensors...) {
			for _, e := range n.Env {
				if !slices.Contains(env, e) {
					env = append(env, e)
				}
			}
		}
		sort.Strings(env)
	}
	if len(env) > 0 {
		cred = "the credentials " + strings.Join(env, ", ")
	}
	basis := "It has not been tested or approved yet."
	if p.base != "" {
		basis = "It differs from the version you last tested or approved; the diff follows."
	}
	why := fmt.Sprintf("The agent wants to run `hal9001 assist test`. That executes %s with your real credentials, and it mentions %s. %s "+
		"Allow only if you have read it. Allowing marks this version approved, so the agent can test it again without asking.", subject, cred, basis)
	text, _ := json.Marshal(map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": why}})
	diff, _ := json.Marshal(map[string]any{"type": "diff", "path": path, "oldText": p.base, "newText": p.id.text()})
	return []json.RawMessage{text, diff}
}

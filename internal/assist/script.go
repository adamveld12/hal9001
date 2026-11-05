package assist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
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
	if len(p.id.Env) > 0 {
		cred = "the credentials " + strings.Join(p.id.Env, ", ")
	}
	basis := "It has not been tested or approved yet."
	if p.base != "" {
		basis = "It differs from the version you last tested or approved; the diff follows."
	}
	why := fmt.Sprintf("The agent wants to run `hal9001 assist test`. That executes the draft's script with your real credentials, and it mentions %s. %s "+
		"Allow only if you have read it. Allowing marks this version approved, so the agent can test it again without asking.", cred, basis)
	text, _ := json.Marshal(map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": why}})
	diff, _ := json.Marshal(map[string]any{"type": "diff", "path": "sensor script (" + p.id.Lang + ")", "oldText": p.base, "newText": p.id.text()})
	return []json.RawMessage{text, diff}
}

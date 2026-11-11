package conversation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// DefaultBudget is the size in bytes Compact aims for when a forked chat replays a run as context.
const DefaultBudget = 48 << 10

// Per-item limits of Compact, from the first rendering to the tightest.
const (
	argLimit       = 1000 // a tool call's arguments
	argLimitTight  = 300
	outputLimit    = 2000 // a tool call's result
	keepEnds       = 3    // tool calls at each end of the transcript whose outputs are dropped last
	truncationMark = " … [truncated]"
	omittedOutput  = "[output omitted]"
)

// outputLimits are the steps by which the results of the tool calls that keep their output are cut once dropping the
// outputs in the middle was not enough.
var outputLimits = []int{outputLimit, 600, 200}

// block is one entry of the transcript Compact renders.
type block struct {
	role string // user, assistant, system or tool
	text string // user, assistant and system text
	// Tool call fields.
	title, kind, status string
	args, output        string // full text, cut when rendered
	// State chosen by Compact.
	outLimit int  // bytes of output kept; -1: the output is omitted
	argLimit int  // bytes of arguments kept
	elided   bool // the whole block is dropped (a tool call only)
	// size is the length of the block's rendering; it is kept current by state.change.
	size int
}

// omitsOutput reports that the block is a tool call whose output was dropped (it has one and is not elided itself).
func (b *block) omitsOutput() bool {
	return b.role == "tool" && !b.elided && b.outLimit < 0 && b.output != ""
}

// state is the set of blocks with the length of what render would write, kept up to date while Compact changes blocks
// one at a time (rendering the whole transcript after every change would be quadratic).
type state struct {
	blocks  []*block
	tools   []*block
	sum     int // of the blocks' sizes
	omitted int // tool calls whose output was dropped
	elided  int // tool calls that were dropped
}

func newState(blocks []*block) *state {
	s := &state{blocks: blocks}
	for _, b := range blocks {
		b.outLimit, b.argLimit = outputLimit, argLimit
		b.size = len(renderBlock(b))
		s.sum += b.size
		if b.role == "tool" {
			s.tools = append(s.tools, b)
		}
	}
	return s
}

// change applies f to b and updates the totals.
func (s *state) change(b *block, f func(*block)) {
	s.sum -= b.size
	if b.omitsOutput() {
		s.omitted--
	}
	if b.elided {
		s.elided--
	}
	f(b)
	b.size = len(renderBlock(b))
	s.sum += b.size
	if b.omitsOutput() {
		s.omitted++
	}
	if b.elided {
		s.elided++
	}
}

// len is the length of the rendering, give or take the one trailing newline render trims.
func (s *state) len() int {
	n := s.sum
	if s.omitted > 0 {
		n += len(outputsMark(s.omitted))
	}
	if s.elided > 0 {
		n += len(callsMark(s.elided))
	}
	return n
}

func outputsMark(n int) string { return fmt.Sprintf("[... %d tool outputs omitted ...]\n\n", n) }
func callsMark(n int) string   { return fmt.Sprintf("[... %d tool calls omitted ...]\n\n", n) }

// Compact renders a transcript as text for a new agent session that cannot load the old one. It is deterministic and
// calls no model. Every user and assistant message is kept verbatim. Tool calls keep their title and arguments; their
// results are cut per item. When the text is longer than budget bytes, Compact first drops the output of the tool calls
// in the middle (the first and the last three keep theirs), then cuts the outputs and arguments that remain, then drops
// those outputs too, and only then drops whole tool calls, oldest first. User and assistant text is never dropped, so
// the result can exceed budget when the messages themselves do. Dropped parts are marked: `result: [output omitted]`
// on a call, `[... N tool outputs omitted ...]` at the start, and `[... N tool calls omitted ...]` where the oldest
// dropped call was. Dividers and notices are not part of the transcript.
func Compact(msgs []Message, budget int) string {
	var blocks []*block
	for _, m := range msgs {
		if m.Kind != "" {
			continue
		}
		switch m.Role {
		case "user", "assistant", "system":
			if m.Text != nil && strings.TrimSpace(*m.Text) != "" {
				blocks = append(blocks, &block{role: m.Role, text: strings.TrimRight(*m.Text, "\n")})
			}
		case "tool":
			if b := toolBlock(m); b != nil {
				blocks = append(blocks, b)
			}
		}
	}
	st := newState(blocks)
	if st.len() <= budget {
		return render(blocks)
	}

	// 1. Drop the outputs of the middle tool calls, the one in the center first.
	if n := len(st.tools); n > 2*keepEnds {
		for _, i := range centerOut(keepEnds, n-keepEnds) {
			st.change(st.tools[i], func(b *block) { b.outLimit = -1 })
			if st.len() <= budget {
				return render(blocks)
			}
		}
	}
	// 2. Cut what remains: outputs, then arguments, then the outputs themselves.
	for _, lim := range outputLimits[1:] {
		for _, b := range st.tools {
			if b.outLimit > lim {
				st.change(b, func(b *block) { b.outLimit = lim })
			}
		}
		if st.len() <= budget {
			return render(blocks)
		}
	}
	for _, b := range st.tools {
		st.change(b, func(b *block) {
			b.argLimit = argLimitTight
			b.outLimit = -1
		})
	}
	if st.len() <= budget {
		return render(blocks)
	}
	// 3. Last resort: whole tool calls, oldest first.
	for _, b := range st.tools {
		st.change(b, func(b *block) { b.elided = true })
		if st.len() <= budget {
			break
		}
	}
	return render(blocks)
}

// centerOut lists the indices lo..hi-1 starting at the middle and moving outward, alternating sides, so the outputs
// closest to either end are dropped last.
func centerOut(lo, hi int) []int {
	var out []int
	mid := lo + (hi-lo)/2
	l, r := mid-1, mid
	for l >= lo || r < hi {
		if r < hi {
			out = append(out, r)
			r++
		}
		if l >= lo {
			out = append(out, l)
			l--
		}
	}
	return out
}

// toolBlock builds the block of a tool message; nil when it has no usable tool call.
func toolBlock(m Message) *block {
	var c ToolCall
	if len(m.ToolCall) == 0 || json.Unmarshal(m.ToolCall, &c) != nil {
		return nil
	}
	b := &block{role: "tool", title: c.Title, kind: c.Kind, status: c.Status}
	if len(c.RawInput) > 0 && string(c.RawInput) != "null" {
		var buf bytes.Buffer
		if json.Compact(&buf, c.RawInput) == nil {
			b.args = buf.String()
		} else {
			b.args = string(c.RawInput)
		}
	}
	var out []string
	for _, p := range c.Content {
		if t := contentText(p); t != "" {
			out = append(out, t)
		}
	}
	b.output = strings.Join(out, "\n")
	return b
}

// contentText is the text of one tool call content part: text as it is, a diff as its path and new text.
func contentText(part json.RawMessage) string {
	var p struct {
		Type    string `json:"type"`
		Path    string `json:"path"`
		NewText string `json:"newText"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Text string `json:"text"`
	}
	if json.Unmarshal(part, &p) != nil {
		return ""
	}
	switch p.Type {
	case "content":
		return p.Content.Text
	case "diff":
		return "diff of " + p.Path + ":\n" + p.NewText
	case "terminal":
		return "[terminal output not included]"
	}
	return p.Text
}

// renderBlock writes one block; an elided tool call has no rendering of its own (render marks where they were).
func renderBlock(bl *block) string {
	switch {
	case bl.role == "tool" && bl.elided:
		return ""
	case bl.role == "tool":
		var b strings.Builder
		b.WriteString("[tool] " + bl.title)
		if bl.kind != "" || bl.status != "" {
			b.WriteString(" (" + strings.Trim(bl.kind+", "+bl.status, ", ") + ")")
		}
		b.WriteString("\n")
		if bl.args != "" {
			b.WriteString("arguments: " + cut(bl.args, bl.argLimit) + "\n")
		}
		switch {
		case bl.output == "":
		case bl.outLimit < 0:
			b.WriteString("result: " + omittedOutput + "\n")
		default:
			b.WriteString("result: " + cut(bl.output, bl.outLimit) + "\n")
		}
		b.WriteString("\n")
		return b.String()
	}
	return "[" + bl.role + "]\n" + bl.text + "\n\n"
}

// render writes the blocks. It is a pure function of the blocks' state.
func render(blocks []*block) string {
	var b strings.Builder
	omitted, elided := 0, 0
	for _, bl := range blocks {
		if bl.omitsOutput() {
			omitted++
		}
		if bl.elided {
			elided++
		}
	}
	if omitted > 0 {
		b.WriteString(outputsMark(omitted))
	}
	marked := false
	for _, bl := range blocks {
		if bl.elided {
			if !marked {
				b.WriteString(callsMark(elided))
				marked = true
			}
			continue
		}
		b.WriteString(renderBlock(bl))
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// cut shortens s to at most n bytes (on a character boundary) and marks the cut.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return CutUTF8(s, n) + truncationMark + " (" + strconv.Itoa(len(s)) + " bytes)"
}

package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MaxPreambleInput is the size above which the node input is truncated in the preamble.
const MaxPreambleInput = 8 << 10

// PreambleData is what the context preamble describes.
type PreambleData struct {
	Info RunInfo
	Now  time.Time
	// Tools are the session's tools (Session.Tools).
	Tools []ToolInfo
	// Output, when set, is the run's output sink: the preamble tells the agent how the result is returned.
	Output *OutputSink
	// SystemPrompt is the user's app-wide system prompt (Config.SystemPrompt); empty adds nothing.
	SystemPrompt string
}

// Preamble renders the context block that starts the first prompt of an agent run: run, flow and node ids, current
// time and zone, the trigger, the node input (pretty JSON, truncated with a note when large) and a tool summary.
// The text ends with a blank line so the prompt can follow directly.
func Preamble(d PreambleData) string {
	i := d.Info
	var b strings.Builder
	if sp := strings.TrimSpace(d.SystemPrompt); sp != "" {
		b.WriteString("## User context (from Settings → Context)\n")
		b.WriteString("The user wrote this to tell you who they are and what to always know. Treat it as background facts and preferences. " +
			"The hal context and instructions that follow, and your tool restrictions, take precedence over it.\n\n")
		b.WriteString(sp)
		b.WriteString("\n\n## End of user context\n\n")
	}
	b.WriteString("<hal_context>\n")
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	line("agent_run", i.AgentRunID)
	line("flow_run", i.FlowRunID)
	line("flow", i.FlowID)
	line("node", i.NodeID)
	line("node_run", i.NodeRunID)
	line("assist_session", i.AssistSessionID)
	now := d.Now
	if !now.IsZero() {
		line("time", formatNow(now.Local()))
	}
	line("run_kind", i.RunKind)
	if i.DryRun {
		line("dry_run", "true")
	}
	line("sensor", i.SensorID)
	line("event", i.EventID)
	if !i.PolledAt.IsZero() {
		line("polled_at", i.PolledAt.UTC().Format(time.RFC3339))
	}
	if len(i.Input) > 0 {
		b.WriteString("node_input:\n")
		b.WriteString(indentJSON(i.Input))
		b.WriteString("\n")
	}
	if len(d.Tools) > 0 {
		b.WriteString("hal tools (MCP server \"hal\"):\n")
		for _, t := range d.Tools {
			fmt.Fprintf(&b, "- %s: %s\n", t.Name, firstSentence(t.Description))
		}
	}
	if d.Output != nil {
		b.WriteString(d.Output.PreambleLine() + "\n")
	}
	b.WriteString("</hal_context>\n\n")
	return b.String()
}

func indentJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	text := string(raw)
	if err := json.Indent(&buf, raw, "", "  "); err == nil {
		text = buf.String()
	}
	if len(text) > MaxPreambleInput {
		text = text[:MaxPreambleInput]
		// Do not cut inside a UTF-8 sequence.
		for len(text) > 0 && text[len(text)-1]&0xC0 == 0x80 {
			text = text[:len(text)-1]
		}
		text += fmt.Sprintf("\n[truncated at %d bytes; call node_input for the full payload]", MaxPreambleInput)
	}
	return text
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

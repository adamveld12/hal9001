package conversation

import (
	"encoding/json"
	"unicode/utf8"
)

// MaxToolText bounds each text block of a stored tool call (command output, diffs); the full transcript stays on the
// agent run. It is above the largest github_issue_link result, which the chat parses.
const MaxToolText = 24 << 10

// CutUTF8 returns at most n bytes of s, cut on a character boundary.
func CutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// ClipContent bounds the text of tool call content parts (ACP content blocks, diffs) to MaxToolText bytes each.
func ClipContent(parts []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(parts))
	for _, p := range parts {
		var m map[string]any
		if json.Unmarshal(p, &m) != nil {
			out = append(out, p)
			continue
		}
		changed := false
		clip := func(v any) any {
			if s, ok := v.(string); ok && len(s) > MaxToolText {
				changed = true
				return CutUTF8(s, MaxToolText) + "\n… (clipped; the full output is in the agent run transcript)"
			}
			return v
		}
		for k, v := range m {
			switch k {
			case "text", "oldText", "newText":
				m[k] = clip(v)
			case "content":
				if inner, ok := v.(map[string]any); ok {
					if txt, ok := inner["text"]; ok {
						inner["text"] = clip(txt)
					}
				}
			}
		}
		if !changed {
			out = append(out, p)
			continue
		}
		if b, err := json.Marshal(m); err == nil {
			out = append(out, b)
		} else {
			out = append(out, p)
		}
	}
	return out
}

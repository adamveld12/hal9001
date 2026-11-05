package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Link is a labelled URL in a note.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Note is the structured result of a prompt with `output: note` (§7.4).
type Note struct {
	Title          string `json:"title"`
	Priority       string `json:"priority"`
	SummaryMD      string `json:"summary_md"`
	SuggestedReply string `json:"suggested_reply,omitempty"`
	Links          []Link `json:"links,omitempty"`
}

var validPriorities = map[string]bool{"low": true, "normal": true, "high": true, "urgent": true}

// ParseNote extracts the final JSON object from the agent's final message (it
// may be fenced in ``` or followed by prose) and validates it. A missing
// priority defaults to normal. On error the caller falls back to an inbox item
// holding the raw message with priority normal and a parse_error flag.
func ParseNote(final string) (Note, error) {
	obj, ok := lastJSONObject(final)
	if !ok {
		return Note{}, errors.New("no JSON object found in the final message")
	}
	var n Note
	dec := json.NewDecoder(bytes.NewReader(obj))
	if err := dec.Decode(&n); err != nil {
		return Note{}, fmt.Errorf("note does not match the schema: %w", err)
	}
	n.Title = strings.TrimSpace(n.Title)
	if n.Title == "" {
		return Note{}, errors.New("note has no title")
	}
	if n.Priority == "" {
		n.Priority = "normal"
	}
	if !validPriorities[n.Priority] {
		return Note{}, fmt.Errorf("note priority %q is not one of low, normal, high, urgent", n.Priority)
	}
	for i, l := range n.Links {
		if strings.TrimSpace(l.URL) == "" {
			return Note{}, fmt.Errorf("note link %d has no url", i)
		}
	}
	return n, nil
}

// lastJSONObject returns the last top-level balanced {...} in s that is valid
// JSON. Braces inside JSON strings are respected; fences need no special
// handling because their content is just text around the braces.
func lastJSONObject(s string) ([]byte, bool) {
	var last []byte
	const maxScan = 1 << 20 // bound the worst case of the quadratic rescan
	if len(s) > maxScan {
		s = s[len(s)-maxScan:]
	}
	for i := 0; i < len(s); {
		if s[i] != '{' {
			i++
			continue
		}
		end := matchBrace(s, i)
		if end < 0 {
			i++
			continue
		}
		if cand := []byte(s[i : end+1]); json.Valid(cand) {
			last = cand
			i = end + 1
			continue
		}
		i++
	}
	return last, last != nil
}

// matchBrace returns the index of the brace closing the one at s[start], or -1.
func matchBrace(s string, start int) int {
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

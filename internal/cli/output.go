package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"unicode/utf8"
)

// maxCell is the widest a table cell gets before it is truncated.
const maxCell = 60

// render writes a response body: indented JSON by default, a table with --table.
// cols names the columns of a list's items (dotted paths reach into nested
// objects); without cols, or for a single object, scalar fields are shown.
func render(w io.Writer, body []byte, table bool, cols []string) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if !table || !json.Valid(body) {
		return writeJSON(w, body)
	}
	var doc any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return writeJSON(w, body)
	}
	switch v := doc.(type) {
	case map[string]any:
		if items, ok := v["items"].([]any); ok {
			if err := writeItemsTable(w, items, cols); err != nil {
				return err
			}
			if next, _ := v["next_cursor"].(string); next != "" {
				_, err := fmt.Fprintf(w, "\nnext cursor: %s\n", next)
				return err
			}
			return nil
		}
		return writeObjectTable(w, body)
	case []any:
		return writeItemsTable(w, v, cols)
	default:
		return writeJSON(w, body)
	}
}

func writeJSON(w io.Writer, body []byte) error {
	var out bytes.Buffer
	if json.Valid(body) && json.Indent(&out, bytes.TrimSpace(body), "", "  ") == nil {
		out.WriteByte('\n')
	} else {
		out.Reset()
		out.Write(body)
		if !bytes.HasSuffix(body, []byte("\n")) {
			out.WriteByte('\n')
		}
	}
	_, err := w.Write(out.Bytes())
	return err
}

func writeItemsTable(w io.Writer, items []any, cols []string) error {
	if len(items) == 0 {
		_, err := io.WriteString(w, "(none)\n")
		return err
	}
	if len(cols) == 0 {
		if first, ok := items[0].(map[string]any); ok {
			cols = scalarKeys(first)
		}
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	heads := make([]string, len(cols))
	for i, c := range cols {
		heads[i] = strings.ToUpper(c[strings.LastIndex(c, ".")+1:])
	}
	_, _ = fmt.Fprintln(tw, strings.Join(heads, "\t"))
	for _, it := range items {
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = cell(lookup(it, c))
		}
		_, _ = fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}

// writeObjectTable prints a single object as aligned "key  value" lines, in
// document order.
func writeObjectTable(w io.Writer, body []byte) error {
	keys, vals, err := orderedObject(body)
	if err != nil {
		return writeJSON(w, body)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for i, k := range keys {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", k, cell(vals[i]))
	}
	return tw.Flush()
}

// orderedObject decodes the top-level keys of a JSON object in document order.
func orderedObject(body []byte) ([]string, []any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if _, err := dec.Token(); err != nil {
		return nil, nil, err
	}
	var keys []string
	var vals []any
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, _ := kt.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		vals = append(vals, v)
	}
	return keys, vals, nil
}

// scalarKeys lists an object's non-container fields, sorted, capped to a few
// so a table fits a terminal.
func scalarKeys(m map[string]any) []string {
	var keys []string
	for k, v := range m {
		switch v.(type) {
		case map[string]any, []any:
			continue
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if len(keys) > 8 {
		keys = keys[:8]
	}
	return keys
}

// lookup follows a dotted path through nested objects.
func lookup(v any, path string) any {
	for _, part := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[part]
	}
	return v
}

func cell(v any) string {
	var s string
	switch t := v.(type) {
	case nil:
		return "-"
	case string:
		s = t
	case json.Number:
		s = t.String()
	case bool:
		if t {
			s = "yes"
		} else {
			s = "no"
		}
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return "?"
		}
		s = string(b)
	}
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxCell {
		r := []rune(s)
		s = string(r[:maxCell-1]) + "…"
	}
	if s == "" {
		return "-"
	}
	return s
}

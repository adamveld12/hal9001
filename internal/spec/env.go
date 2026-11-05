package spec

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Env is a parsed env file (REQUIREMENTS §3): secrets injected into every
// node. Values are sensitive; Env has no JSON form on purpose.
type Env struct {
	// Vars maps name to value.
	Vars map[string]string
	// Keys lists names in file order (first occurrence).
	Keys []string
	// Plain are the names marked as not secret by a "# hal:plain KEY ..."
	// comment line (see PlainMarker). Names without a value are ignored by
	// every consumer; use IsPlain and SecretVars rather than reading this.
	Plain map[string]bool

	markerLines []int // 0-based line numbers of the marker lines
}

// PlainMarker starts a comment line that lists env names whose values are not
// secret: they are not redacted from output and the API returns them. Names
// are separated by spaces or commas; several marker lines add up. Everything
// else in the file stays an ordinary KEY=VALUE file, so a marker survives hand
// edits and tools that preserve comments.
const PlainMarker = "# hal:plain"

// IsPlain reports whether key has a value and is marked plain.
func (e *Env) IsPlain(key string) bool {
	_, ok := e.Vars[key]
	return ok && e.Plain[key]
}

// SecretVars returns the variables that are secret (every one not marked
// plain): the values that must be redacted from output.
func (e *Env) SecretVars() map[string]string {
	out := make(map[string]string, len(e.Vars))
	for k, v := range e.Vars {
		if !e.Plain[k] {
			out[k] = v
		}
	}
	return out
}

// plainMarkerKeys parses a marker comment line; ok is false for any other line.
func plainMarkerKeys(line string) (keys []string, ok bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(strings.TrimRight(line, "\r")), "#")
	if !ok {
		return nil, false
	}
	rest, ok = strings.CutPrefix(strings.TrimSpace(rest), "hal:plain")
	if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != ',') {
		return nil, false
	}
	for _, f := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' }) {
		if envKeyRE.MatchString(f) {
			keys = append(keys, f)
		}
	}
	return keys, true
}

// SetEnvPlain returns the env file text with key marked plain (plain=true) or
// secret. Marker lines that do not mention key, and every other line, stay as
// they are; a marker line left without names is dropped.
func SetEnvPlain(raw []byte, key string, plain bool) []byte {
	env, _ := ParseEnv(raw)
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(raw) == 0 {
		lines = nil
	}
	present := false
	var out []string
	marker := map[int]bool{}
	for _, i := range env.markerLines {
		marker[i] = true
	}
	for i, l := range lines {
		if !marker[i] {
			out = append(out, l)
			continue
		}
		keys, _ := plainMarkerKeys(l)
		if !slices.Contains(keys, key) {
			out = append(out, l)
			continue
		}
		if plain {
			present = true
			out = append(out, l)
			continue
		}
		keys = slices.DeleteFunc(keys, func(k string) bool { return k == key })
		if len(keys) > 0 {
			out = append(out, PlainMarker+" "+strings.Join(keys, " "))
		}
	}
	if plain && !present {
		out = append(out, PlainMarker+" "+key)
	}
	if len(out) == 0 {
		return nil
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseEnv parses KEY=VALUE lines.
//
//   - Blank lines and lines starting with # are ignored (a "# hal:plain KEY ..."
//     line marks names as not secret, see PlainMarker); a leading "export "
//     is accepted.
//   - Unquoted values are trimmed, and end at a # preceded by whitespace
//     (so "a#b" is the value a#b, and "a #b" is a).
//   - Double-quoted values support the escapes \n \r \t \" \\ \$ (other
//     backslashes stay literal); single-quoted values are literal. Both may
//     span several lines. Only whitespace or a # comment may follow the
//     closing quote.
//   - Values are not expanded: $VAR stays $VAR.
//   - A repeated key keeps the last value and produces a warning.
//
// Bad lines produce error diagnostics and are skipped; the rest still parse.
func ParseEnv(b []byte) (*Env, []Diagnostic) {
	env := &Env{Vars: map[string]string{}, Plain: map[string]bool{}}
	var ds []Diagnostic
	bad := func(line int, code, format string, args ...any) {
		ds = append(ds, Diagnostic{Line: line, Severity: SeverityError, Code: code, Message: fmt.Sprintf(format, args...)})
	}
	lines := strings.Split(strings.TrimPrefix(string(b), "\ufeff"), "\n")
	for i := 0; i < len(lines); i++ {
		start := i + 1
		line := strings.TrimSpace(strings.TrimRight(lines[i], "\r"))
		if line == "" || line[0] == '#' {
			if keys, ok := plainMarkerKeys(line); ok {
				env.markerLines = append(env.markerLines, i)
				for _, k := range keys {
					env.Plain[k] = true
				}
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok {
			bad(start, CodeInvalidValue, "expected KEY=VALUE, got %q", truncateForMessage(line))
			continue
		}
		if !envKeyRE.MatchString(key) {
			bad(start, CodeInvalidValue, "invalid variable name %q (letters, digits and _, not starting with a digit)", key)
			continue
		}
		val = strings.TrimLeft(val, " \t")
		var value string
		if val != "" && (val[0] == '"' || val[0] == '\'') {
			raw := strings.TrimSuffix(lines[i], "\r")
			rawVal := strings.TrimLeft(raw[strings.Index(raw, "=")+1:], " \t")
			first := i
			v, rest, closed := scanQuoted(val[0], rawVal[1:], lines, &i)
			if !closed {
				i = first // do not swallow the lines that follow
				bad(start, CodeInvalidValue, "%s: unterminated %c quote", key, val[0])
				continue
			}
			if rest = strings.TrimSpace(rest); rest != "" && rest[0] != '#' {
				bad(start, CodeInvalidValue, "%s: unexpected text after the closing quote", key)
				continue
			}
			value = v
		} else {
			value = stripInlineComment(val)
		}
		if _, dup := env.Vars[key]; dup {
			ds = append(ds, Diagnostic{Line: start, Severity: SeverityWarning, Code: CodeDuplicateKey, Message: fmt.Sprintf("%s is defined more than once; the last value wins", key)})
		} else {
			env.Keys = append(env.Keys, key)
		}
		env.Vars[key] = value
	}
	return env, ds
}

func stripInlineComment(s string) string {
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			s = s[:i]
			break
		}
	}
	if strings.HasPrefix(s, "#") {
		return ""
	}
	return strings.TrimSpace(s)
}

// scanQuoted reads a quoted value that starts after the opening quote in
// first, continuing onto following lines (advancing *idx) until the closing
// quote. rest is what follows the closing quote on its line.
func scanQuoted(q byte, first string, lines []string, idx *int) (val, rest string, closed bool) {
	var b strings.Builder
	cur := strings.TrimSuffix(first, "\r")
	for {
		for j := 0; j < len(cur); j++ {
			ch := cur[j]
			if q == '"' && ch == '\\' && j+1 < len(cur) {
				j++
				switch cur[j] {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				case '"', '\\', '$':
					b.WriteByte(cur[j])
				default:
					b.WriteByte('\\')
					b.WriteByte(cur[j])
				}
				continue
			}
			if ch == q {
				return b.String(), cur[j+1:], true
			}
			b.WriteByte(ch)
		}
		if *idx+1 >= len(lines) {
			return "", "", false
		}
		*idx++
		cur = strings.TrimSuffix(lines[*idx], "\r")
		b.WriteByte('\n')
	}
}

func truncateForMessage(s string) string {
	// Never echo what may be a secret in full.
	if len(s) > 12 {
		return s[:12] + "..."
	}
	return s
}

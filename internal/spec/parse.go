package spec

import (
	"bytes"
	"fmt"
	"strings"
)

// Parse functions decode bytes, apply defaults, and report syntax and
// type-level problems. They do not run semantic validation (see the Check
// functions) and return a nil value only when the file is empty or not valid
// YAML. A non-nil value may be partial when diagnostics contain errors.

func parseYAMLFile[T any](b []byte) (*T, []Diagnostic) {
	var v T
	ds, empty, fatal := decodeYAML(b, &v)
	switch {
	case empty:
		return nil, []Diagnostic{{Severity: SeverityError, Code: CodeEmptyFile, Message: "file is empty"}}
	case fatal:
		return nil, ds
	}
	return &v, ds
}

// ParseConfig parses config.yaml. An empty file yields the default config.
func ParseConfig(b []byte) (*Config, []Diagnostic) {
	var cfg Config
	ds, _, fatal := decodeYAML(b, &cfg)
	if fatal {
		return nil, ds
	}
	cfg.ApplyDefaults()
	return &cfg, ds
}

// ParseSensor parses a sensor file.
func ParseSensor(name string, b []byte) (*Sensor, []Diagnostic) {
	s, ds := parseYAMLFile[Sensor](b)
	if s != nil {
		s.ApplyDefaults()
		ds = append(ds, adoptName(KindSensor, name, &s.ID, &s.LegacyID)...)
	}
	return s, ds
}

// ParseFlow parses a flow file.
func ParseFlow(name string, b []byte) (*Flow, []Diagnostic) {
	f, ds := parseYAMLFile[Flow](b)
	if f != nil {
		f.ApplyDefaults()
		ds = append(ds, adoptName(KindFlow, name, &f.ID, &f.LegacyID)...)
	}
	return f, ds
}

// CheckConfig parses and validates config.yaml bytes. File is "config.yaml".
func CheckConfig(b []byte) (*Config, []Diagnostic) {
	cfg, ds := ParseConfig(b)
	if cfg != nil {
		ds = append(ds, ValidateConfig(cfg)...)
	}
	return cfg, withFile(KindConfig.Rel(""), ds)
}

// CheckSensor parses and validates sensor bytes. name is the file stem the
// sensor is (or will be) stored under; it becomes the sensor's id. An empty
// name leaves the id to a legacy id: field, or empty. refs may be nil to skip reference checks (mcp_servers).
func CheckSensor(name string, b []byte, refs *Refs) (*Sensor, []Diagnostic) {
	s, ds := ParseSensor(name, b)
	if s != nil {
		ds = append(ds, ValidateSensor(s, refs)...)
	}
	return s, withFile(KindSensor.Rel(name), ds)
}

// CheckFlow parses and validates flow bytes; see CheckSensor for name. refs
// may be nil to skip reference checks.
func CheckFlow(name string, b []byte, refs *Refs) (*Flow, []Diagnostic) {
	f, ds := ParseFlow(name, b)
	if f != nil {
		ds = append(ds, ValidateFlow(f, refs)...)
	}
	return f, withFile(KindFlow.Rel(name), ds)
}

// CheckPrompt parses and validates prompt bytes; name is the file stem.
func CheckPrompt(name string, b []byte) (*Prompt, []Diagnostic) {
	p, ds := ParsePrompt(name, b)
	if p != nil {
		ds = append(ds, ValidatePrompt(p)...)
	}
	return p, withFile(KindPrompt.Rel(name), ds)
}

// adoptName sets the id of a parsed file from its file name. A legacy id:
// field is ignored (and not kept, so the next write drops it); when no name is
// given it supplies the id, and when it disagrees with the name that is a
// warning.
func adoptName(k Kind, name string, id, legacy *string) []Diagnostic {
	l := *legacy
	*legacy = ""
	if name == "" {
		*id = l
		return nil
	}
	*id = name
	if l == "" || l == name {
		return nil
	}
	return []Diagnostic{{
		Path: "id", Severity: SeverityWarning, Code: CodeIDMismatch,
		Message: fmt.Sprintf("id %q is ignored: the file name %q (%s) is the id; remove the id line", l, name, k.Rel(name)),
	}}
}

// MarshalFlow renders f as YAML with defaults omitted and fields in a stable,
// readable order. Parsing the result and applying defaults yields f (after
// defaults).
func MarshalFlow(f Flow) ([]byte, error) { return encodeYAML(f.Minimal()) }

// MarshalSensor renders s as YAML with defaults omitted.
func MarshalSensor(s Sensor) ([]byte, error) { return encodeYAML(s.Minimal()) }

// MarshalConfig renders c as YAML with defaults omitted.
func MarshalConfig(c Config) ([]byte, error) { return encodeYAML(c.Minimal()) }

// ParsePrompt parses a prompt file: optional YAML frontmatter between "---"
// lines, then the template body. name is the file stem; it is used when the
// frontmatter has no name. A missing frontmatter is a warning, an
// unterminated one an error.
func ParsePrompt(name string, b []byte) (*Prompt, []Diagnostic) {
	text := strings.TrimPrefix(string(b), "\ufeff")
	p := &Prompt{Name: name, Body: text}
	first, rest, _ := strings.Cut(text, "\n")
	if strings.TrimRight(first, " \t\r") != "---" {
		return p, []Diagnostic{{Severity: SeverityWarning, Code: CodeRequired, Message: "prompt has no frontmatter (---, then description, output, then ---)"}}
	}
	var fm strings.Builder
	var ds []Diagnostic
	body, closed := "", false
	for rest != "" {
		var line string
		line, rest, _ = strings.Cut(rest, "\n")
		if strings.TrimRight(line, " \t\r") == "---" {
			body, closed = rest, true
			break
		}
		fm.WriteString(line)
		fm.WriteByte('\n')
	}
	if !closed {
		return p, []Diagnostic{{Line: 1, Severity: SeverityError, Code: CodeYAMLSyntax, Message: "frontmatter is not closed: add a --- line after it"}}
	}
	p.Body = body
	if strings.TrimSpace(fm.String()) != "" {
		var meta Prompt
		d, _, fatal := decodeYAML([]byte(fm.String()), &meta)
		for i := range d {
			if d[i].Line > 0 {
				d[i].Line++ // frontmatter starts on line 2 of the file
			}
		}
		ds = append(ds, d...)
		if !fatal {
			p.Name, p.Description, p.Output = meta.Name, meta.Description, meta.Output
			if name != "" {
				p.Name = ""
				if meta.Name != "" && meta.Name != name {
					ds = append(ds, Diagnostic{
						Path: "name", Severity: SeverityWarning, Code: CodeIDMismatch,
						Message: fmt.Sprintf("frontmatter name %q is ignored: the file name %q is the name; remove the name line", meta.Name, name),
					})
				}
			}
		}
	}
	if p.Name == "" {
		p.Name = name
	}
	if p.Output == "" {
		p.Output = OutputNone
	}
	return p, ds
}

// MarshalPrompt renders p as a prompt file.
func MarshalPrompt(p Prompt) ([]byte, error) {
	if p.Output == OutputNone {
		p.Output = ""
	}
	p.Name = "" // the file name is the name
	fm, err := encodeYAML(p)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(fm)
	buf.WriteString("---\n")
	buf.WriteString(p.Body)
	return buf.Bytes(), nil
}

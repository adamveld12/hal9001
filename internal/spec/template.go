package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"text/template"
)

// Templates are Go text/template with missingkey=error and three helpers:
//
//	json VALUE            VALUE as compact JSON (no HTML escaping)
//	default DEF VALUE     DEF when VALUE is nil, "", 0, false or empty
//	trunc N STRING        the first N runes of STRING
//
// default and trunc take the subject last so they work in pipelines:
// {{.input.title | trunc 80}}. Because missing keys are errors, default only
// helps for present-but-empty values; for an optional key write
// {{default "n/a" (index .input "maybe")}}, since index yields nil rather than
// an error for a missing map key.
var templateFuncs = template.FuncMap{
	"json": func(v any) (string, error) {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return "", err
		}
		return strings.TrimSuffix(buf.String(), "\n"), nil
	},
	"default": func(def, v any) any {
		if isEmpty(v) {
			return def
		}
		return v
	},
	"trunc": func(n int, s any) (string, error) {
		if n < 0 {
			return "", fmt.Errorf("trunc: negative length %d", n)
		}
		str := fmt.Sprint(s)
		if s == nil {
			str = ""
		}
		r := []rune(str)
		if len(r) <= n {
			return str, nil
		}
		return string(r[:n]), nil
	},
}

func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array:
		return rv.Len() == 0
	case reflect.Bool:
		return !rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() == 0
	case reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

// FlowInfo is the {{.flow}} template value.
type FlowInfo struct {
	ID     string
	Hash   string
	DryRun bool
}

// RunInfo is the {{.run}} template value.
type RunInfo struct {
	ID      string
	EventID string
	Kind    string
}

// NewData builds the template data model of REQUIREMENTS §6: .event is the
// original event payload, .input the node input payload (both usually
// map[string]any decoded from JSON), .flow and .run identify the execution.
// Keys are snake_case: .flow.id, .flow.hash, .flow.dry_run, .run.id,
// .run.event_id, .run.kind.
func NewData(event, input any, flow FlowInfo, run RunInfo) map[string]any {
	return map[string]any{
		"event": event,
		"input": input,
		"flow":  map[string]any{"id": flow.ID, "hash": flow.Hash, "dry_run": flow.DryRun},
		"run":   map[string]any{"id": run.ID, "event_id": run.EventID, "kind": run.Kind},
	}
}

// ParseTemplate parses text with the standard helpers and missingkey=error.
func ParseTemplate(name, text string) (*template.Template, error) {
	return template.New(name).Option("missingkey=error").Funcs(templateFuncs).Parse(text)
}

// TemplateError is a template parse or execution failure.
type TemplateError struct {
	Name string // what was being rendered, e.g. "nodes[2].title"
	Msg  string
	Err  error
}

func (e *TemplateError) Error() string {
	if e.Name == "" {
		return "template: " + e.Msg
	}
	return fmt.Sprintf("template %s: %s", e.Name, e.Msg)
}
func (e *TemplateError) Unwrap() error { return e.Err }

var (
	execPrefixRE = regexp.MustCompile(`^template: [^:]*:\d+(?::\d+)?: (?:executing "[^"]*" at (<[^>]*>): )?`)
	missingKeyRE = regexp.MustCompile(`map has no entry for key "([^"]*)"`)
)

func templateError(name string, err error) *TemplateError {
	msg := err.Error()
	at := ""
	if m := execPrefixRE.FindStringSubmatch(msg); m != nil {
		at = m[1]
		msg = msg[len(m[0]):]
	}
	if m := missingKeyRE.FindStringSubmatch(msg); m != nil {
		msg = fmt.Sprintf("missing key %q", m[1])
	}
	if at != "" {
		msg += " at " + at
	}
	return &TemplateError{Name: name, Msg: msg, Err: err}
}

// Render executes tmpl with data. Failures are *TemplateError; a missing key
// error names the key and the template action, for example
// `template: missing key "url" at <.input.subject.url>`.
func Render(tmpl string, data any) (string, error) {
	return RenderNamed("", tmpl, data)
}

// RenderNamed is Render with a name (such as "nodes[2].title") used in errors.
// The text is parsed on every call; callers that render often should keep the
// result of ParseTemplate.
func RenderNamed(name, tmpl string, data any) (string, error) {
	t, err := ParseTemplate(name, tmpl)
	if err != nil {
		return "", templateError(name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", templateError(name, err)
	}
	return buf.String(), nil
}

// CheckTemplate reports whether text parses; it does not execute it.
func CheckTemplate(name, text string) *TemplateError {
	if !strings.Contains(text, "{{") {
		return nil
	}
	if _, err := ParseTemplate(name, text); err != nil {
		return templateError(name, err)
	}
	return nil
}

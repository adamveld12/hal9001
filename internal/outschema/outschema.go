// Package outschema compiles and applies the JSON Schemas of agent output: the `output_schema` of an agent node with
// `output: tool`, and the default note schema. The schema becomes the input schema of the run's `submit_output` tool,
// and every call is validated against it.
package outschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

const resourceURL = "https://hal9001.invalid/output-schema.json"

// Schema is a compiled output schema.
type Schema struct {
	doc      map[string]any
	compiled *jsonschema.Schema
}

// noRemote refuses every resource other than the schema itself and the built-in metaschemas, so a `$ref` to a URL or
// a file is a compile error instead of a fetch.
type noRemote struct{}

func (noRemote) Load(url string) (any, error) {
	return nil, errors.New("remote and file references are not supported: only local references such as \"#/$defs/name\" can be used")
}

// Compile checks doc and compiles it. The schema must describe an object (it is a tool's input schema), be valid
// JSON Schema (draft 2020-12 unless $schema says otherwise) and reference nothing outside itself.
func Compile(doc map[string]any) (*Schema, error) {
	if doc == nil {
		return nil, errors.New("the schema is empty")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("the schema is not JSON: %w", err)
	}
	if t, ok := doc["type"]; !ok || t != "object" {
		return nil, errors.New(`the top level must have type: object (the agent passes its result as the arguments of a tool)`)
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(noRemote{})
	if err := c.AddResource(resourceURL, parsed); err != nil {
		return nil, err
	}
	compiled, err := c.Compile(resourceURL)
	if err != nil {
		return nil, cleanCompileError(err)
	}
	var norm map[string]any
	if err := json.Unmarshal(raw, &norm); err != nil {
		return nil, err
	}
	return &Schema{doc: norm, compiled: compiled}, nil
}

// cleanCompileError names the failing keywords of a schema that is not valid JSON Schema, one "<path>: <reason>" per
// line (the path leads into the schema), and hides the placeholder URL.
func cleanCompileError(err error) error {
	var sve *jsonschema.SchemaValidationError
	var ve *jsonschema.ValidationError
	if errors.As(err, &sve) && errors.As(sve.Err, &ve) {
		if lines := leafMessages(ve); len(lines) > 0 {
			return errors.New("not valid JSON Schema:\n" + strings.Join(lines, "\n"))
		}
	}
	return errors.New(strings.ReplaceAll(err.Error(), resourceURL, "output_schema"))
}

// Doc is the schema as a JSON object (the tool's input schema).
func (s *Schema) Doc() map[string]any { return s.doc }

// JSON is the schema as compact JSON.
func (s *Schema) JSON() string {
	b, _ := json.Marshal(s.doc)
	return string(b)
}

// Validate checks args (a JSON document). The error lists every mismatch as "<path>: <reason>", one per line, with
// "(root)" for the top level.
func (s *Schema) Validate(args []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return fmt.Errorf("the arguments are not valid JSON: %w", err)
	}
	verr := s.compiled.Validate(inst)
	if verr == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(verr, &ve) {
		return verr
	}
	lines := leafMessages(ve)
	if len(lines) == 0 {
		return errors.New("(root): does not match the schema")
	}
	return errors.New(strings.Join(lines, "\n"))
}

var printer = message.NewPrinter(language.English)

// leafMessages lists the distinct "<path>: <reason>" of the failing leaves of a validation error tree, sorted.
func leafMessages(e *jsonschema.ValidationError) []string {
	seen := map[string]bool{}
	var lines []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			path := "(root)"
			if len(e.InstanceLocation) > 0 {
				path = "/" + strings.Join(e.InstanceLocation, "/")
			}
			line := path + ": " + e.ErrorKind.LocalizedString(printer)
			if !seen[line] {
				seen[line] = true
				lines = append(lines, line)
			}
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(e)
	sort.Strings(lines)
	return lines
}

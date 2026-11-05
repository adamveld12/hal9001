package api

import (
	"encoding/json"

	"github.com/getkin/kin-openapi/openapi3"

	"hal9001/internal/apispec"
	"hal9001/internal/spec"
)

// conforms reports whether v, as JSON, satisfies the named component schema of
// the contract. Parsed spec values reach the API even when the file has
// errors (a lang that is not one of the five, a name that is not a slug); the
// contract promises a well-formed object or null, so those are sent as null
// and the diagnostics say what is wrong.
func conforms(schema string, v any) bool {
	ref := apispec.MustLoad().Components.Schemas[schema]
	if ref == nil || ref.Value == nil {
		return true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	var x any
	if json.Unmarshal(b, &x) != nil {
		return false
	}
	return ref.Value.VisitJSON(x, openapi3.EnableJSONSchema2020()) == nil
}

// sensorValue returns s for the API, or nil when it would not match the Sensor schema.
func sensorValue(s *spec.Sensor) *spec.Sensor {
	if s == nil || !conforms("Sensor", s) {
		return nil
	}
	return s
}

// flowValue returns f normalized for the API, or nil when it does not match the Flow schema.
func flowValue(f *spec.Flow) *spec.Flow {
	n := normalizeFlow(f)
	if n == nil || !conforms("Flow", n) {
		return nil
	}
	return n
}

func promptValue(p *spec.Prompt) *spec.Prompt {
	if p == nil || !conforms("Prompt", p) {
		return nil
	}
	return p
}

package outschema

import "sync"

// NoteDoc returns a fresh copy of the default output schema: the inbox note (docs/flows.md, "Prompts and notes").
func NoteDoc() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"title", "summary_md"},
		"properties": map[string]any{
			"title":           map[string]any{"type": "string", "pattern": `\S`, "description": "Short headline of the inbox item."},
			"priority":        map[string]any{"enum": []any{"low", "normal", "high", "urgent"}, "description": "How soon a person should look. Default normal."},
			"summary_md":      map[string]any{"type": "string", "description": "Markdown for a reader who has not seen the item."},
			"suggested_reply": map[string]any{"type": "string", "description": "Optional reply the person can send."},
			"links": map[string]any{
				"type": "array", "description": "Optional links to the sources.",
				"items": map[string]any{
					"type": "object", "required": []any{"label", "url"}, "additionalProperties": false,
					"properties": map[string]any{"label": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"}},
				},
			},
		},
		"additionalProperties": false,
	}
}

var (
	noteOnce   sync.Once
	noteSchema *Schema
)

// Note is the compiled default schema. It is a constant of the program: a compile failure is a bug.
func Note() *Schema {
	noteOnce.Do(func() {
		s, err := Compile(NoteDoc())
		if err != nil {
			panic("outschema: default note schema: " + err.Error())
		}
		noteSchema = s
	})
	return noteSchema
}

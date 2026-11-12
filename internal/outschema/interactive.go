package outschema

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Limits of the interactive parts of a note (docs/flows.md, "Questions and actions").
const (
	MaxQuestions      = 4
	MaxOptions        = 5
	MinOptions        = 2
	MaxActions        = 5
	MaxHeaderRunes    = 12
	MaxIDRunes        = 40
	MaxQuestionRunes  = 300
	MaxLabelRunes     = 80
	MaxDescriptionLen = 300
	MaxActionLabel    = 40
	MaxPromptRunes    = 2000
	// ActionPrompt is the only action type.
	ActionPrompt = "prompt"
)

// idPattern is the slug of a question id.
const idPattern = `^[a-z0-9][a-z0-9_-]{0,39}$`

var idRE = regexp.MustCompile(idPattern)

// Option is one choice of a Question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is a decision the agent needs before it acts, shaped like Ask User Question: a short chip (Header), the
// question, two to five options, an optional recommended option (an index into Options) and Multi for a multiple choice.
// Every question also offers "Other" in the UI; that is not an option here.
type Question struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Header      string   `json:"header,omitempty"`
	Options     []Option `json:"options"`
	Recommended *int     `json:"recommended,omitempty"`
	Multi       bool     `json:"multi,omitempty"`
}

// Action is an optional next step the user can start with one click: the prompt is sent to the agent verbatim.
type Action struct {
	Type   string `json:"type"`
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
}

// questionsDoc and actionsDoc are the schema properties of the note schema.
func questionsDoc() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": MaxQuestions,
		"description": "Optional. Up to 4 decisions you need from the person before you act. Each has an id (a slug, unique in the note), the question, a short header (at most 12 characters) and 2 to 5 options. " +
			"Set recommended to the index of the option you suggest and multi to true when several options may be chosen. The person can always answer with their own text instead. Do not add an Other option.",
		"items": map[string]any{
			"type": "object", "required": []any{"id", "question", "options"}, "additionalProperties": false,
			"properties": map[string]any{
				"id":          map[string]any{"type": "string", "pattern": idPattern, "description": "Slug of the question, unique in this note, e.g. merge_method."},
				"question":    map[string]any{"type": "string", "pattern": `\S`, "maxLength": MaxQuestionRunes},
				"header":      map[string]any{"type": "string", "maxLength": MaxHeaderRunes, "description": "Short chip label, at most 12 characters."},
				"options":     map[string]any{"type": "array", "minItems": MinOptions, "maxItems": MaxOptions, "items": map[string]any{"type": "object", "required": []any{"label"}, "additionalProperties": false, "properties": map[string]any{"label": map[string]any{"type": "string", "pattern": `\S`, "maxLength": MaxLabelRunes}, "description": map[string]any{"type": "string", "maxLength": MaxDescriptionLen}}}},
				"recommended": map[string]any{"type": "integer", "minimum": 0, "maximum": MaxOptions - 1, "description": "Index of the option you recommend."},
				"multi":       map[string]any{"type": "boolean", "description": "True when more than one option may be chosen."},
			},
		},
	}
}

func actionsDoc() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": MaxActions,
		"description": "Optional. Up to 5 next steps the person can start with one click. The prompt is sent to you in a follow-up message exactly as written, so write it as an instruction to yourself with everything you need. Side effects still need approval.",
		"items": map[string]any{
			"type": "object", "required": []any{"label", "prompt"}, "additionalProperties": false,
			"properties": map[string]any{
				"type":   map[string]any{"enum": []any{ActionPrompt}, "description": "Only prompt exists. Default prompt."},
				"label":  map[string]any{"type": "string", "pattern": `\S`, "maxLength": MaxActionLabel, "description": "Button text, at most 40 characters."},
				"prompt": map[string]any{"type": "string", "pattern": `\S`, "maxLength": MaxPromptRunes},
			},
		},
	}
}

// ParseInteractive decodes the questions and actions of a note (a JSON object) and checks what the JSON Schema cannot:
// question ids are unique, recommended points at an option, option labels of a question are distinct. Actions get their
// default type. A note without either returns nil slices.
func ParseInteractive(note []byte) ([]Question, []Action, error) {
	var n struct {
		Questions []Question `json:"questions"`
		Actions   []Action   `json:"actions"`
	}
	if err := json.Unmarshal(note, &n); err != nil {
		return nil, nil, fmt.Errorf("questions or actions do not match the schema: %w", err)
	}
	if err := Check(n.Questions, n.Actions); err != nil {
		return nil, nil, err
	}
	for i := range n.Actions {
		if n.Actions[i].Type == "" {
			n.Actions[i].Type = ActionPrompt
		}
	}
	return n.Questions, n.Actions, nil
}

// Check applies every limit of questions and actions, one error naming the first problem.
func Check(qs []Question, as []Action) error {
	if len(qs) > MaxQuestions {
		return fmt.Errorf("/questions: at most %d questions, got %d", MaxQuestions, len(qs))
	}
	if len(as) > MaxActions {
		return fmt.Errorf("/actions: at most %d actions, got %d", MaxActions, len(as))
	}
	seen := map[string]bool{}
	for i, q := range qs {
		p := fmt.Sprintf("/questions/%d", i)
		if !idRE.MatchString(q.ID) {
			return fmt.Errorf("%s/id: %q is not a slug (lowercase letters, digits, _ and -, starting with a letter or digit, at most %d characters)", p, q.ID, MaxIDRunes)
		}
		if seen[q.ID] {
			return fmt.Errorf("%s/id: %q is used by an earlier question; ids must be unique", p, q.ID)
		}
		seen[q.ID] = true
		if strings.TrimSpace(q.Question) == "" {
			return fmt.Errorf("%s/question: must not be empty", p)
		}
		if n := len([]rune(q.Question)); n > MaxQuestionRunes {
			return fmt.Errorf("%s/question: %d characters, at most %d", p, n, MaxQuestionRunes)
		}
		if n := len([]rune(q.Header)); n > MaxHeaderRunes {
			return fmt.Errorf("%s/header: %d characters, at most %d", p, n, MaxHeaderRunes)
		}
		if len(q.Options) < MinOptions || len(q.Options) > MaxOptions {
			return fmt.Errorf("%s/options: %d options, want %d to %d", p, len(q.Options), MinOptions, MaxOptions)
		}
		labels := map[string]bool{}
		for j, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" {
				return fmt.Errorf("%s/options/%d/label: must not be empty", p, j)
			}
			if n := len([]rune(o.Label)); n > MaxLabelRunes {
				return fmt.Errorf("%s/options/%d/label: %d characters, at most %d", p, j, n, MaxLabelRunes)
			}
			if n := len([]rune(o.Description)); n > MaxDescriptionLen {
				return fmt.Errorf("%s/options/%d/description: %d characters, at most %d", p, j, n, MaxDescriptionLen)
			}
			if labels[o.Label] {
				return fmt.Errorf("%s/options/%d/label: %q repeats an earlier option", p, j, o.Label)
			}
			labels[o.Label] = true
		}
		if q.Recommended != nil && (*q.Recommended < 0 || *q.Recommended >= len(q.Options)) {
			return fmt.Errorf("%s/recommended: %d is not an index of the %d options", p, *q.Recommended, len(q.Options))
		}
	}
	for i, a := range as {
		p := fmt.Sprintf("/actions/%d", i)
		if a.Type != "" && a.Type != ActionPrompt {
			return fmt.Errorf("%s/type: %q is not a known action type (only %q)", p, a.Type, ActionPrompt)
		}
		if strings.TrimSpace(a.Label) == "" {
			return fmt.Errorf("%s/label: must not be empty", p)
		}
		if n := len([]rune(a.Label)); n > MaxActionLabel {
			return fmt.Errorf("%s/label: %d characters, at most %d", p, n, MaxActionLabel)
		}
		if strings.TrimSpace(a.Prompt) == "" {
			return fmt.Errorf("%s/prompt: must not be empty", p)
		}
		if n := len([]rune(a.Prompt)); n > MaxPromptRunes {
			return fmt.Errorf("%s/prompt: %d characters, at most %d", p, n, MaxPromptRunes)
		}
	}
	return nil
}

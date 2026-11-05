package spec

import (
	"errors"
	"strings"
	"testing"
)

func sampleData() map[string]any {
	return NewData(
		map[string]any{"id": "42", "updated_at": "2026-11-05T10:00:00Z", "n": 3},
		map[string]any{
			"subject": map[string]any{"title": "Fix the thing", "url": "https://example.test/pr/1"},
			"labels":  []any{"a", "b"},
			"empty":   "",
		},
		FlowInfo{ID: "triage", Hash: "abc123", DryRun: true},
		RunInfo{ID: "run1", EventID: "ev1", Kind: "live"},
	)
}

func TestRenderDataModel(t *testing.T) {
	got, err := Render(`{{.event.id}}:{{.event.updated_at}} {{.input.subject.title}} {{.flow.id}}@{{.flow.hash}} {{.run.id}} {{.run.event_id}} {{.run.kind}} {{.flow.dry_run}}`, sampleData())
	if err != nil {
		t.Fatal(err)
	}
	want := "42:2026-11-05T10:00:00Z Fix the thing triage@abc123 run1 ev1 live true"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderMissingKeyNamesTheKey(t *testing.T) {
	_, err := Render(`PR: {{.input.subject.number}}`, sampleData())
	if err == nil {
		t.Fatal("missing key must be an error, not <no value>")
	}
	var te *TemplateError
	if !errors.As(err, &te) {
		t.Fatalf("error type %T", err)
	}
	if !strings.Contains(err.Error(), `missing key "number"`) || !strings.Contains(err.Error(), ".input.subject.number") {
		t.Errorf("error does not name the key and action: %v", err)
	}
}

func TestRenderNamedIncludesWhere(t *testing.T) {
	_, err := RenderNamed("nodes[2].title", `{{.input.nope}}`, sampleData())
	if err == nil || !strings.HasPrefix(err.Error(), "template nodes[2].title: missing key \"nope\"") {
		t.Errorf("error = %v", err)
	}
}

func TestRenderSyntaxError(t *testing.T) {
	_, err := Render(`{{.input.title`, sampleData())
	if err == nil || !strings.Contains(err.Error(), "unclosed action") {
		t.Errorf("error = %v", err)
	}
	_, err = Render(`{{.input.title | shout}}`, sampleData())
	if err == nil || !strings.Contains(err.Error(), `function "shout" not defined`) {
		t.Errorf("error = %v", err)
	}
}

func TestTemplateHelpers(t *testing.T) {
	tests := []struct{ tmpl, want string }{
		{`{{json .input.subject}}`, `{"title":"Fix the thing","url":"https://example.test/pr/1"}`},
		{`{{json .input.labels}}`, `["a","b"]`},
		{`{{json "a<b&c"}}`, `"a<b&c"`}, // no HTML escaping: output goes to prompts and shell, not HTML
		{`{{.input.subject.title | trunc 7}}`, `Fix the`},
		{`{{trunc 100 .input.subject.title}}`, `Fix the thing`},
		{`{{trunc 2 "héllo"}}`, `hé`}, // counts runes, not bytes
		{`{{trunc 3 .event.n}}`, `3`},
		{`{{default "fallback" .input.empty}}`, `fallback`},
		{`{{default "fallback" .input.subject.title}}`, `Fix the thing`},
		{`{{default "n/a" (index .input "absent")}}`, `n/a`}, // optional key idiom
		{`{{default 7 .event.n}}`, `3`},
	}
	for _, tt := range tests {
		got, err := Render(tt.tmpl, sampleData())
		if err != nil || got != tt.want {
			t.Errorf("%s = %q, %v; want %q", tt.tmpl, got, err, tt.want)
		}
	}
	if _, err := Render(`{{trunc -1 "x"}}`, nil); err == nil {
		t.Error("negative trunc length must be an error")
	}
}

func TestSensorKeyRendersOverTheItem(t *testing.T) {
	// Sensor keys template the item itself, not the {.event,...} model.
	got, err := Render("{{.id}}:{{.updated_at}}", map[string]any{"id": "7", "updated_at": "t1"})
	if err != nil || got != "7:t1" {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := Render("{{.id}}:{{.updated_at}}", map[string]any{"id": "7"}); err == nil || !strings.Contains(err.Error(), `"updated_at"`) {
		t.Errorf("missing item field must fail naming it: %v", err)
	}
}

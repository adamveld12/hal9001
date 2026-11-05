package spec

import (
	"strings"
	"testing"
)

const requirementsPrompt = `---
name: pr-review
description: Review a PR and leave comments
output: note            # note = structured inbox schema (§7.4); none = free text
---
You are reviewing {{.input.subject.url}}.
...
`

func TestRequirementsPromptLoadsClean(t *testing.T) {
	p, ds := CheckPrompt("pr-review", []byte(requirementsPrompt))
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	if p.Name != "pr-review" || p.Description != "Review a PR and leave comments" || p.Output != OutputNote {
		t.Errorf("prompt = %+v", p)
	}
	if p.Body != "You are reviewing {{.input.subject.url}}.\n...\n" {
		t.Errorf("body = %q", p.Body)
	}
}

func TestPromptDefaultsAndFallbacks(t *testing.T) {
	p, ds := ParsePrompt("plain", []byte("---\ndescription: d\n---\nbody\n"))
	if len(ds) != 0 || p.Name != "plain" || p.Output != OutputNone {
		t.Errorf("name %q output %q diags %v", p.Name, p.Output, ds)
	}

	p, ds = ParsePrompt("bare", []byte("just a body {{.event.id}}\n"))
	if p.Body != "just a body {{.event.id}}\n" || len(ds) != 1 || ds[0].Severity != SeverityWarning {
		t.Errorf("no frontmatter: body %q diags %v", p.Body, ds)
	}

	p, ds = ParsePrompt("file-name", []byte("---\nname: other\n---\nb\n"))
	if p.Name != "other" || len(ds) != 1 || ds[0].Code != CodeIDMismatch || ds[0].Severity != SeverityWarning {
		t.Errorf("name mismatch: %+v %v", p, ds)
	}

	p, _ = ParsePrompt("crlf", []byte("---\r\nname: crlf\r\noutput: note\r\n---\r\nbody\r\n"))
	if p.Name != "crlf" || p.Output != OutputNote || p.Body != "body\r\n" {
		t.Errorf("CRLF prompt = %+v", p)
	}
}

func TestPromptErrors(t *testing.T) {
	_, ds := ParsePrompt("p", []byte("---\nname: p\nbody text without end\n"))
	if len(ds) != 1 || ds[0].Code != CodeYAMLSyntax || !strings.Contains(ds[0].Message, "not closed") || ds[0].Line != 1 {
		t.Errorf("unterminated frontmatter: %v", ds)
	}

	_, ds = ParsePrompt("p", []byte("---\nname: p\nverbose: yes\n---\nb\n"))
	if len(ds) != 1 || ds[0].Code != CodeUnknownField || ds[0].Line != 3 || ds[0].Path != "verbose" {
		t.Errorf("unknown frontmatter field: %+v", ds)
	}

	_, ds = CheckPrompt("p", []byte("---\nname: p\noutput: json\n---\nb\n"))
	if len(ds) != 1 || ds[0].Code != CodeInvalidEnum || ds[0].Path != "output" || ds[0].File != "prompts/p.md" {
		t.Errorf("output enum: %v", ds)
	}

	_, ds = CheckPrompt("p", []byte("---\nname: p\n---\nHello {{.input.title\n"))
	if len(ds) != 1 || ds[0].Code != CodeInvalidTemplate || ds[0].Path != "body" {
		t.Errorf("body template: %v", ds)
	}

	_, ds = CheckPrompt("p", []byte("---\nname: p\n---\n  \n"))
	if len(ds) != 1 || ds[0].Code != CodeRequired || ds[0].Path != "body" {
		t.Errorf("empty body: %v", ds)
	}
}

func TestPromptMarshalRoundTrip(t *testing.T) {
	p, _ := ParsePrompt("pr-review", []byte(requirementsPrompt))
	out, err := MarshalPrompt(*p)
	if err != nil {
		t.Fatal(err)
	}
	back, ds := ParsePrompt("pr-review", out)
	if len(ds) != 0 || *back != *p {
		t.Errorf("round trip changed prompt: %v\n%+v\n%+v\n%s", ds, back, p, out)
	}
	none, _ := MarshalPrompt(Prompt{Name: "n", Output: OutputNone, Body: "x\n"})
	if strings.Contains(string(none), "output") {
		t.Errorf("default output written:\n%s", none)
	}
}

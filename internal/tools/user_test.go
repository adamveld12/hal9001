package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"hal9001/internal/runner"
	"hal9001/internal/spec"
)

func mustTool(t *testing.T, y string) *spec.Tool {
	t.Helper()
	tool, ds := spec.CheckTool("", []byte(y))
	if spec.HasErrors(ds) {
		t.Fatalf("tool: %v", ds)
	}
	return tool
}

const echoTool = `id: echo
description: d
params:
  name: {type: string, required: true}
  count: {type: integer, default: 2}
  tags: {type: array}
lang: bash
script: |
  echo "env=$ARG_NAME/$ARG_COUNT/$ARG_TAGS"
  echo "stdin=$(cat)"
`

func TestRunUserToolPassesArgsOnStdinAndEnv(t *testing.T) {
	tool := mustTool(t, echoTool)
	res, err := RunUserTool(context.Background(), tool, json.RawMessage(`{"name":"ada","tags":["a","b"]}`), UserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "env=ada/2/[\"a\",\"b\"]\nstdin={\"count\":2,\"name\":\"ada\",\"tags\":[\"a\",\"b\"]}"
	if res.Stdout != want {
		t.Errorf("stdout = %q, want %q", res.Stdout, want)
	}
	if res.Structured != nil {
		t.Errorf("text output marked structured: %s", res.Structured)
	}
}

func TestRunUserToolStructuredOutput(t *testing.T) {
	tool := mustTool(t, "id: j\ndescription: d\nlang: bash\nscript: |\n  echo '{\"a\": [1,2]}'\n")
	res, err := RunUserTool(context.Background(), tool, nil, UserOptions{})
	if err != nil || string(res.Structured) != `{"a": [1,2]}` {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestRunUserToolFailureCarriesStderrTail(t *testing.T) {
	tool := mustTool(t, "id: f\ndescription: d\nlang: bash\nscript: |\n  echo oops >&2\n  exit 3\n")
	res, err := RunUserTool(context.Background(), tool, nil, UserOptions{})
	var te *ToolError
	if !errors.As(err, &te) || te.ExitCode != 3 || !strings.Contains(te.Error(), "oops") {
		t.Fatalf("err = %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit = %d", res.ExitCode)
	}
}

func TestRunUserToolRedactsSecrets(t *testing.T) {
	tool := mustTool(t, "id: r\ndescription: d\nlang: bash\nscript: |\n  echo \"token=$API_TOKEN\"\n  echo \"err $API_TOKEN\" >&2\n  exit 1\n")
	opts := UserOptions{
		Env:      map[string]string{"API_TOKEN": "s3cr3t-value-123"},
		Redactor: runner.NewRedactor(map[string]string{"API_TOKEN": "s3cr3t-value-123"}),
	}
	res, err := RunUserTool(context.Background(), tool, nil, opts)
	if err == nil || strings.Contains(err.Error(), "s3cr3t") || strings.Contains(res.Stdout, "s3cr3t") || strings.Contains(res.Stderr, "s3cr3t") {
		t.Fatalf("secret leaked: %q %q %v", res.Stdout, res.Stderr, err)
	}
	if !strings.Contains(res.Stdout, "«API_TOKEN»") {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

func TestRunUserToolRejectsBadArgs(t *testing.T) {
	tool := mustTool(t, echoTool)
	for _, args := range []string{`{}`, `{"name":3}`, `{"name":"a","extra":1}`, `[1]`, `{"name":"a","count":1.5}`} {
		_, err := RunUserTool(context.Background(), tool, json.RawMessage(args), UserOptions{})
		var ae *ArgError
		if !errors.As(err, &ae) {
			t.Errorf("args %s: err = %v, want ArgError", args, err)
		}
	}
}

func TestApprovalNeeded(t *testing.T) {
	ctx := context.Background()
	never := mustTool(t, "id: n\ndescription: d\nlang: bash\nscript: x\napprove: never\n")
	always := mustTool(t, "id: a\ndescription: d\nlang: bash\nscript: x\n")
	for tool, want := range map[*spec.Tool]bool{never: false, always: true} {
		if got, err := ApprovalNeeded(ctx, tool, nil, nil); err != nil || got != want {
			t.Errorf("%s: got %v, %v", tool.ID, got, err)
		}
	}
	when := mustTool(t, `id: w
description: d
params:
  url: {type: string, required: true}
  method: {type: string, default: GET}
lang: bash
script: x
approve:
  when:
    match: any
    conditions:
      - {field: method, op: not_equals, value: GET}
      - {field: url, op: contains, value: internal}
`)
	for args, want := range map[string]bool{
		`{"url":"https://example.com"}`:                 false,
		`{"url":"https://example.com","method":"POST"}`: true,
		`{"url":"https://internal.example.com"}`:        true,
	} {
		got, err := ApprovalNeeded(ctx, when, json.RawMessage(args), nil)
		if err != nil || got != want {
			t.Errorf("args %s: got %v, %v; want %v", args, got, err, want)
		}
	}
}

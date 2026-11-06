package content

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"hal9001/internal/spec"
	"hal9001/internal/tools"
)

func loadTool(t *testing.T, name string) *spec.Tool {
	t.Helper()
	raw, err := FS.ReadFile("tools/" + name + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	tool, ds := spec.CheckTool(name, raw)
	if len(ds) != 0 {
		t.Fatalf("tools/%s.yaml: %v", name, ds)
	}
	return tool
}

func TestStarterToolsAreValidWithTheirApproval(t *testing.T) {
	want := map[string]struct{ lang, approve string }{
		"now":           {spec.LangBash, spec.ToolApproveNever},
		"http_get_json": {spec.LangPython, spec.ToolApproveAlways},
	}
	for name, w := range want {
		tool := loadTool(t, name)
		if tool.Lang != w.lang || tool.Approve.Mode != w.approve {
			t.Errorf("%s: lang %q approve %q, want %q %q", name, tool.Lang, tool.Approve.Mode, w.lang, w.approve)
		}
	}
	if !loadTool(t, "http_get_json").Params["url"].Required {
		t.Errorf("http_get_json: url is not required")
	}
}

func TestNowToolRuns(t *testing.T) {
	tool := loadTool(t, "now")
	iso := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{4}$`)
	for _, args := range []string{`{}`, `{"timezone":"Europe/Berlin"}`} {
		res, err := tools.RunUserTool(context.Background(), tool, json.RawMessage(args), tools.UserOptions{})
		if err != nil || !iso.MatchString(res.Stdout) {
			t.Errorf("args %s: %q, %v", args, res.Stdout, err)
		}
	}
}

package content

import (
	"testing"
)

func TestSensorsDeclareKeysAndBaseline(t *testing.T) {
	wantKey := map[string]string{
		"slack-mentions-dms":   "{{.channel}}:{{.ts}}",
		"github-notifications": "{{.id}}:{{.updated_at}}",
		"linear-assigned":      "{{.id}}:{{.updatedAt}}",
		"gmail-unread":         "{{.id}}",
		"imap-mail":            "{{.id}}",
		"demo-ticker":          "{{.id}}:{{.updated_at}}",
	}
	for id, key := range wantKey {
		def := loadSensor(t, id)
		wantLang := "bash"
		if id == "imap-mail" {
			wantLang = "python"
		}
		if def.ID != id || def.Key != key || def.Lang != wantLang || def.Script == "" || def.Timeout == "" || def.Interval == "" {
			t.Errorf("%s: unexpected definition %+v", id, def)
		}
		wantFirst := "baseline"
		if id == "demo-ticker" {
			wantFirst = "fire"
		}
		if def.FirstRun != wantFirst {
			t.Errorf("%s: first_run = %q, want %q", id, def.FirstRun, wantFirst)
		}
	}
}

package content

import (
	"io/fs"
	"strings"
	"testing"

	"hal9001/internal/spec"
)

func TestEmbeddedLayout(t *testing.T) {
	for _, p := range []string{"config.yaml", "env.example"} {
		if _, err := fs.Stat(FS, p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	for dir, want := range map[string][]string{
		"sensors": {"slack-mentions-dms", "github-notifications", "linear-assigned", "gmail-unread", "imap-mail", "demo-ticker"},
		"flows":   {"triage-github", "triage-slack", "linear-triage", "email-secretary", "demo"},
		"prompts": {"triage-slack", "pr-review", "linear-triage", "email-secretary"},
	} {
		ext := ".yaml"
		if dir == "prompts" {
			ext = ".md"
		}
		got := map[string]bool{}
		for _, n := range names(t, dir, ext) {
			got[n] = true
		}
		for _, w := range want {
			if !got[w] {
				t.Errorf("%s/%s%s missing", dir, w, ext)
			}
		}
	}
}

func TestEnvExampleListsEveryCredential(t *testing.T) {
	raw, err := FS.ReadFile("env.example")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"SLACK_USER_TOKEN=", "LINEAR_API_KEY=", "search:read", "im:history", "im:read", "users:read", "GOG_ACCOUNT", "IMAP_USER", "IMAP_PASSWORD", "IMAP_HOST", "IMAP_PORT", "IMAP_TLS", "IMAP_MAILBOX", "IMAP_SEARCH", "IMAP_MAX"} {
		if !strings.Contains(text, want) {
			t.Errorf("env.example lacks %q", want)
		}
	}
	// Every credential a sensor requires must be documented.
	for _, sensor := range names(t, "sensors", ".yaml") {
		for _, v := range []string{"SLACK_USER_TOKEN", "LINEAR_API_KEY"} {
			if strings.Contains(loadSensor(t, sensor).Script, "${"+v+":?") && !strings.Contains(text, v+"=") {
				t.Errorf("%s needs %s but env.example lacks it", sensor, v)
			}
		}
	}
}

// env.example is copied to the global env file as is: it must not mark anything plain by accident.
func TestEnvExampleMarksNothingPlain(t *testing.T) {
	raw, err := FS.ReadFile("env.example")
	if err != nil {
		t.Fatal(err)
	}
	env, ds := spec.ParseEnv(raw)
	if spec.HasErrors(ds) {
		t.Fatal(ds)
	}
	if len(env.Plain) != 0 {
		t.Errorf("env.example marks %v plain", env.Plain)
	}
}

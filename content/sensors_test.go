package content

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type sensorDef struct {
	ID       string `yaml:"id"`
	Interval string `yaml:"interval"`
	Lang     string `yaml:"lang"`
	Script   string `yaml:"script"`
	Key      string `yaml:"key"`
	FirstRun string `yaml:"first_run"`
	Timeout  string `yaml:"timeout"`
}

func loadSensor(t *testing.T, name string) sensorDef {
	t.Helper()
	raw, err := FS.ReadFile("sensors/" + name + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	var def sensorDef
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&def); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return def
}

// rule makes the fake executable answer when every substring of match appears
// in its joined arguments. The first matching rule wins.
type rule struct {
	match []string
	body  string // testdata file, relative to testdata/
	exit  int
}

// sandbox is a temp dir whose bin/ holds fake executables and whose args.log
// records every invocation (NUL-separated args, one "\x01" line per call).
type sandbox struct {
	t   *testing.T
	dir string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &sandbox{t: t, dir: dir}
}

func (s *sandbox) fake(name string, stderr string, rs ...rule) {
	s.t.Helper()
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	fmt.Fprintf(&b, "log=%q\n", filepath.Join(s.dir, name+".log"))
	b.WriteString("{ printf '%s\\0' \"$@\"; printf '\\1'; } >>\"$log\"\n")
	b.WriteString("args=\"$*\"\n")
	for _, r := range rs {
		conds := make([]string, len(r.match))
		for i, m := range r.match {
			conds[i] = fmt.Sprintf(`[[ "$args" == *%s* ]]`, shellQuote(m))
		}
		cond := "true"
		if len(conds) > 0 {
			cond = strings.Join(conds, " && ")
		}
		data, err := filepath.Abs(filepath.Join("testdata", r.body))
		if err != nil {
			s.t.Fatal(err)
		}
		if _, err := os.Stat(data); err != nil {
			s.t.Fatal(err)
		}
		fmt.Fprintf(&b, "if %s; then cat %s; ", cond, shellQuote(data))
		if stderr != "" {
			fmt.Fprintf(&b, "printf '%%s\\n' %s >&2; ", shellQuote(stderr))
		}
		fmt.Fprintf(&b, "exit %d; fi\n", r.exit)
	}
	fmt.Fprintf(&b, "echo \"fake %s: no rule for: $args\" >&2\nexit 99\n", name)
	if err := os.WriteFile(filepath.Join(s.dir, "bin", name), []byte(b.String()), 0o755); err != nil {
		s.t.Fatal(err)
	}
}

type rules []rule

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// calls returns the recorded invocations of a fake as argument lists.
func (s *sandbox) calls(name string) [][]string {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, name+".log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	var out [][]string
	for _, call := range strings.Split(string(raw), "\x01") {
		if call == "" {
			continue
		}
		out = append(out, strings.Split(strings.TrimSuffix(call, "\x00"), "\x00"))
	}
	return out
}

type result struct {
	items  []map[string]any
	stdout string
	stderr string
	exit   int
}

// run executes the sensor's script with the sandbox bin first on PATH and only
// the given environment (plus the system PATH tail for jq and coreutils).
func (s *sandbox) run(def sensorDef, env map[string]string) result {
	s.t.Helper()
	script := filepath.Join(s.dir, "sensor.sh")
	if err := os.WriteFile(script, []byte(def.Script), 0o600); err != nil {
		s.t.Fatal(err)
	}
	jq, err := exec.LookPath("jq")
	if err != nil {
		s.t.Skip("jq not installed")
	}
	// Real curl, gh and gog must never be reachable: only jq's directory and
	// the system dirs follow the fakes.
	path := filepath.Join(s.dir, "bin") + ":" + filepath.Dir(jq) + ":/usr/bin:/bin"
	cmd := exec.Command("bash", script)
	cmd.Dir = s.dir
	cmd.Env = []string{"PATH=" + path, "HOME=" + s.dir}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	exit := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			s.t.Fatal(err)
		}
		exit = ee.ExitCode()
	}
	res := result{stdout: stdout.String(), stderr: stderr.String(), exit: exit}
	dec := json.NewDecoder(strings.NewReader(res.stdout))
	for dec.More() {
		var item map[string]any
		if err := dec.Decode(&item); err != nil {
			s.t.Fatalf("stdout is not JSONL: %v\nstdout:\n%s\nstderr:\n%s", err, res.stdout, res.stderr)
		}
		res.items = append(res.items, item)
	}
	return res
}

func (r result) mustSucceed(t *testing.T) {
	t.Helper()
	if r.exit != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.exit, r.stdout, r.stderr)
	}
}

func (r result) mustFail(t *testing.T, wantStderr string) {
	t.Helper()
	if r.exit == 0 {
		t.Fatalf("expected non-zero exit, got 0\nstdout:\n%s", r.stdout)
	}
	if !strings.Contains(r.stderr, wantStderr) {
		t.Fatalf("stderr %q does not contain %q", r.stderr, wantStderr)
	}
	if strings.TrimSpace(r.stdout) != "" {
		t.Fatalf("failed run must not emit items, stdout:\n%s", r.stdout)
	}
}

func byID(t *testing.T, items []map[string]any) map[string]map[string]any {
	t.Helper()
	m := map[string]map[string]any{}
	for _, it := range items {
		id, _ := it["id"].(string)
		if id == "" {
			t.Fatalf("item without id: %v", it)
		}
		if _, dup := m[id]; dup {
			t.Fatalf("duplicate item id %q", id)
		}
		m[id] = it
	}
	return m
}

func wantFields(t *testing.T, item map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if got := item[k]; got != v {
			t.Errorf("item %v: %s = %#v, want %#v", item["id"], k, got, v)
		}
	}
}

// ---- slack-mentions-dms ----

const slackSince = "2026-11-05T10:00:00.123456789Z" // 1793872800

func slackRules() rules {
	return rules{
		{match: []string{"auth.test"}, body: "slack/auth.test.json"},
		{match: []string{"search.messages"}, body: "slack/search.messages.json"},
		{match: []string{"conversations.list"}, body: "slack/conversations.list.json"},
		{match: []string{"conversations.history", "channel=D1"}, body: "slack/history-D1.json"},
		{match: []string{"conversations.history", "channel=D2"}, body: "slack/history-D2.json"},
		{match: []string{"users.info", "user=U2"}, body: "slack/users.info.json"},
	}
}

func TestSlackMentionsAndDMs(t *testing.T) {
	sb := newSandbox(t)
	sb.fake("curl", "", slackRules()...)
	res := sb.run(loadSensor(t, "slack-mentions-dms"), map[string]string{
		"SLACK_USER_TOKEN":    "xoxp-test",
		"HAL_LAST_SUCCESS_AT": slackSince,
	})
	res.mustSucceed(t)

	items := byID(t, res.items)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (new mention + new DM; stale, self, bot and join messages dropped): %s", len(items), res.stdout)
	}
	wantFields(t, items["C1:1793876400.000100"], map[string]any{
		"source":       "slack",
		"kind":         "mention",
		"channel":      "C1",
		"ts":           "1793876400.000100",
		"author":       "Bob B",
		"author_id":    "U2",
		"text":         "<@UME> can you look at the deploy?",
		"url":          "https://acme.slack.com/archives/C1/p1793876400000100",
		"title":        "Mention in #eng from Bob B",
		"updated_at":   "2026-11-05T11:00:00Z",
		"channel_name": "eng",
	})
	wantFields(t, items["D1:1793880000.000200"], map[string]any{
		"kind":       "dm",
		"channel":    "D1",
		"ts":         "1793880000.000200",
		"author":     "Bob B",
		"text":       "got a minute?",
		"title":      "DM from Bob B",
		"updated_at": "2026-11-05T12:00:00Z",
		"url":        "https://slack.com/app_redirect?channel=D1&message_ts=1793880000.000200&team=T1",
	})

	// Incremental fetch: both the search window and the history window follow HAL_LAST_SUCCESS_AT.
	var sawSearch, sawHistory bool
	for _, c := range sb.calls("curl") {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "search.messages") {
			sawSearch = true
			if !strings.Contains(joined, "after:2026-11-04") {
				t.Errorf("search window should start a day before the last success: %s", joined)
			}
		}
		if strings.Contains(joined, "conversations.history") {
			sawHistory = true
			if !strings.Contains(joined, "oldest=1793872800") {
				t.Errorf("history must use oldest=<last success epoch>: %s", joined)
			}
		}
		if !strings.Contains(joined, "Authorization: Bearer xoxp-test") {
			t.Errorf("request lacks the bearer token: %s", joined)
		}
	}
	if !sawSearch || !sawHistory {
		t.Fatalf("expected search and history calls, got %v", sb.calls("curl"))
	}
}

func TestSlackDedupesMentionInsideDM(t *testing.T) {
	sb := newSandbox(t)
	rs := slackRules()
	// Make the search hit the same message the DM history returns.
	rs[1].body = "slack/search-dm-overlap.json"
	sb.fake("curl", "", rs...)
	res := sb.run(loadSensor(t, "slack-mentions-dms"), map[string]string{
		"SLACK_USER_TOKEN": "xoxp-test", "HAL_LAST_SUCCESS_AT": slackSince,
	})
	res.mustSucceed(t)
	if len(res.items) != 1 {
		t.Fatalf("same channel:ts must be emitted once, got %d: %s", len(res.items), res.stdout)
	}
}

func TestSlackErrors(t *testing.T) {
	env := map[string]string{"SLACK_USER_TOKEN": "xoxp-test", "HAL_LAST_SUCCESS_AT": slackSince}
	cases := []struct {
		name   string
		env    map[string]string
		rules  rules
		stderr string
	}{
		{"missing token", map[string]string{}, slackRules(), "SLACK_USER_TOKEN is not set"},
		{"ok false on auth.test", env, rules{{match: []string{"auth.test"}, body: "slack/invalid_auth.json"}}, "slack auth.test: error: invalid_auth"},
		{"ok false on search", env, append(rules{{match: []string{"search.messages"}, body: "slack/ratelimited.json"}}, slackRules()...), "slack search.messages: error: ratelimited"},
		{"transport failure", env, rules{{match: []string{"auth.test"}, body: "slack/invalid_auth.json", exit: 22}}, "slack auth.test: request failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb := newSandbox(t)
			sb.fake("curl", "", tc.rules...)
			sb.run(loadSensor(t, "slack-mentions-dms"), tc.env).mustFail(t, tc.stderr)
		})
	}
}

func TestSlackUserLookupFailureKeepsID(t *testing.T) {
	sb := newSandbox(t)
	rs := slackRules()
	rs[5] = rule{match: []string{"users.info"}, body: "slack/invalid_auth.json"}
	sb.fake("curl", "", rs...)
	res := sb.run(loadSensor(t, "slack-mentions-dms"), map[string]string{
		"SLACK_USER_TOKEN": "xoxp-test", "HAL_LAST_SUCCESS_AT": slackSince,
	})
	res.mustSucceed(t)
	items := byID(t, res.items)
	wantFields(t, items["D1:1793880000.000200"], map[string]any{"author": "U2", "title": "DM from U2"})
}

// ---- github-notifications ----

func TestGitHubNotificationsNormalized(t *testing.T) {
	sb := newSandbox(t)
	sb.fake("gh", "", rule{match: []string{"api", "notifications"}, body: "github/notifications.jsonl"})
	res := sb.run(loadSensor(t, "github-notifications"), nil)
	res.mustSucceed(t)

	items := byID(t, res.items)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	wantFields(t, items["1001"], map[string]any{
		"source": "github", "reason": "review_requested", "type": "PullRequest",
		"title": "Add retry to uploader", "repo": "acme/widgets",
		"url":        "https://github.com/acme/widgets/pull/42",
		"updated_at": "2026-11-05T11:00:00Z",
	})
	wantFields(t, items["1002"], map[string]any{"url": "https://github.com/acme/widgets/issues/7", "type": "Issue"})
	// No subject URL (check suites): link to the repository instead of emitting null.
	wantFields(t, items["1003"], map[string]any{"url": "https://github.com/acme/gadgets", "type": "CheckSuite"})
	// The original notification fields survive for flows (e.g. .repository.name).
	if repo, _ := items["1001"]["repository"].(map[string]any); repo["name"] != "widgets" {
		t.Errorf("repository object not preserved: %v", items["1001"]["repository"])
	}
	if sb.calls("gh")[0][0] != "api" {
		t.Errorf("unexpected gh call: %v", sb.calls("gh"))
	}
}

func TestGitHubNotificationsAPIErrorFails(t *testing.T) {
	sb := newSandbox(t)
	// Partial output then failure: pipefail must still surface the gh exit code.
	sb.fake("gh", "gh: HTTP 401: Bad credentials (https://api.github.com/notifications)",
		rule{match: []string{"notifications"}, body: "github/notifications.jsonl", exit: 1})
	res := sb.run(loadSensor(t, "github-notifications"), nil)
	if res.exit == 0 {
		t.Fatalf("expected failure when gh exits non-zero, stdout:\n%s", res.stdout)
	}
	if !strings.Contains(res.stderr, "Bad credentials") {
		t.Errorf("gh's error must reach stderr, got %q", res.stderr)
	}
}

func TestGitHubNotificationsEmptyIsEmpty(t *testing.T) {
	sb := newSandbox(t)
	sb.fake("gh", "", rule{match: []string{"notifications"}, body: "github/empty.jsonl"})
	res := sb.run(loadSensor(t, "github-notifications"), nil)
	res.mustSucceed(t)
	if strings.TrimSpace(res.stdout) != "" {
		t.Fatalf("no notifications must print nothing, got %q", res.stdout)
	}
}

// ---- linear-assigned ----

const linearSince = "2026-11-05T10:00:00Z"

func TestLinearAssigned(t *testing.T) {
	sb := newSandbox(t)
	sb.fake("curl", "", rule{match: []string{"api.linear.app/graphql"}, body: "linear/ok.json"})
	res := sb.run(loadSensor(t, "linear-assigned"), map[string]string{
		"LINEAR_API_KEY": "lin_api_test", "HAL_LAST_SUCCESS_AT": linearSince,
	})
	res.mustSucceed(t)

	items := byID(t, res.items)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 2 issues + 1 foreign comment: %s", len(items), res.stdout)
	}
	wantFields(t, items["iss-1"], map[string]any{
		"source": "linear", "kind": "issue", "identifier": "ENG-12",
		"title": "ENG-12: Fix upload retries", "author": "Dana",
		"text": "Uploads fail on 503.", "url": "https://linear.app/acme/issue/ENG-12",
		"updatedAt": "2026-11-05T11:00:00.000Z", "state": "In Progress", "priority": "High",
	})
	wantFields(t, items["iss-2"], map[string]any{"text": "", "author": "unknown"})
	wantFields(t, items["cm-1"], map[string]any{
		"kind": "comment", "author": "Dana", "text": "Can you take this today?",
		"title": "Comment on ENG-12: Fix upload retries", "issue_id": "iss-1",
		"updatedAt": "2026-11-05T11:20:00.000Z",
	})
	if _, ok := items["cm-2"]; ok {
		t.Error("own comments must not be emitted")
	}

	// Request: auth header without Bearer, and the query window equals HAL_LAST_SUCCESS_AT.
	calls := sb.calls("curl")
	if len(calls) != 1 {
		t.Fatalf("want one GraphQL request, got %d", len(calls))
	}
	args := strings.Join(calls[0], "\n")
	if !strings.Contains(args, "Authorization: lin_api_test") {
		t.Errorf("Linear personal keys are sent without Bearer: %q", args)
	}
	var payload struct {
		Variables struct {
			Since string `json:"since"`
		} `json:"variables"`
	}
	for _, a := range calls[0] {
		if strings.HasPrefix(a, "{") {
			if err := json.Unmarshal([]byte(a), &payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if payload.Variables.Since != linearSince {
		t.Errorf("since = %q, want %q", payload.Variables.Since, linearSince)
	}
}

func TestLinearErrors(t *testing.T) {
	env := map[string]string{"LINEAR_API_KEY": "k", "HAL_LAST_SUCCESS_AT": linearSince}
	cases := []struct {
		name   string
		env    map[string]string
		rule   rule
		stderr string
	}{
		{"missing key", map[string]string{}, rule{body: "linear/ok.json"}, "LINEAR_API_KEY is not set"},
		{"graphql errors with HTTP 200", env, rule{body: "linear/graphql-error.json"}, "linear: graphql error: Authentication required"},
		{"HTTP failure", env, rule{body: "linear/http-401.json", exit: 22}, "linear: request failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb := newSandbox(t)
			sb.fake("curl", "", tc.rule)
			sb.run(loadSensor(t, "linear-assigned"), tc.env).mustFail(t, tc.stderr)
		})
	}
}

// ---- gmail-unread ----

func TestGmailUnreadNormalized(t *testing.T) {
	sb := newSandbox(t)
	sb.fake("gog", "", rule{match: []string{"gmail", "search"}, body: "gmail/messages.json"})
	res := sb.run(loadSensor(t, "gmail-unread"), map[string]string{"GOG_ACCOUNT": "me@example.com"})
	res.mustSucceed(t)

	items := byID(t, res.items)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	wantFields(t, items["m1"], map[string]any{
		"source": "gmail", "thread_id": "t1", "from": "Alice <alice@example.com>",
		"author": "Alice <alice@example.com>", "subject": "Contract review by Friday",
		"title": "Contract review by Friday",
		"text":  "Hi, can you review the attached contract by Friday?",
		"url":   "https://mail.google.com/mail/u/0/#inbox/t1", "updated_at": "2026-11-05T11:00:03-08:00",
	})
	wantFields(t, items["m2"], map[string]any{"title": "(no subject)", "updated_at": "2026-11-05 11:30", "text": ""})

	call := sb.calls("gog")[0]
	joined := strings.Join(call, " ")
	for _, want := range []string{"--account me@example.com", "--readonly", "--gmail-no-send", "is:unread in:inbox", "--json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gog call lacks %q: %s", want, joined)
		}
	}
}

func TestGmailUnreadAcceptsBareArrayAndOmitsAccount(t *testing.T) {
	sb := newSandbox(t)
	sb.fake("gog", "", rule{body: "gmail/messages-array.json"})
	res := sb.run(loadSensor(t, "gmail-unread"), nil)
	res.mustSucceed(t)
	if len(res.items) != 1 || res.items[0]["id"] != "m9" {
		t.Fatalf("unexpected items: %s", res.stdout)
	}
	if strings.Contains(strings.Join(sb.calls("gog")[0], " "), "--account") {
		t.Error("--account must be omitted when GOG_ACCOUNT is unset")
	}
}

func TestGmailUnreadEmptyAndFailure(t *testing.T) {
	sb := newSandbox(t)
	sb.fake("gog", "", rule{body: "gmail/empty.json"})
	res := sb.run(loadSensor(t, "gmail-unread"), nil)
	res.mustSucceed(t)
	if strings.TrimSpace(res.stdout) != "" {
		t.Fatalf("empty inbox must print nothing, got %q", res.stdout)
	}

	sb = newSandbox(t)
	sb.fake("gog", "error: no stored credentials for me@example.com", rule{body: "gmail/empty.json", exit: 1})
	sb.run(loadSensor(t, "gmail-unread"), nil).mustFail(t, "gog: gmail search failed")
}

// ---- demo-ticker ----

func TestDemoTickerIsDeterministic(t *testing.T) {
	def := loadSensor(t, "demo-ticker")
	var first string
	for range 2 {
		res := newSandbox(t).run(def, nil)
		res.mustSucceed(t)
		if first == "" {
			first = res.stdout
		} else if res.stdout != first {
			t.Fatal("demo-ticker output changed between polls")
		}
		items := byID(t, res.items)
		if len(items) != 3 {
			t.Fatalf("got %d items, want 3", len(items))
		}
		for id, it := range items {
			for _, f := range []string{"title", "text", "author", "url", "severity", "updated_at", "source"} {
				if s, _ := it[f].(string); s == "" {
					t.Errorf("%s: missing %s", id, f)
				}
			}
		}
	}
}

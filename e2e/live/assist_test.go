package live

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildCLI compiles cmd/hal9001 into a temp dir (before startStack changes directory) and returns the dir.
func buildCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.CommandContext(context.Background(), "go", "build", "-buildvcs=false", "-o", filepath.Join(dir, "hal9001"), "hal9001/cmd/hal9001") //nolint:gosec // fixed arguments
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build hal9001: %v\n%s", err, out)
	}
	return dir
}

type liveAssistSession struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	Error          *string `json:"error"`
	LastAgentRunID *string `json:"last_agent_run_id"`
	Draft          struct {
		YAML        string           `json:"yaml"`
		SensorID    *string          `json:"sensor_id"`
		Diagnostics []map[string]any `json:"diagnostics"`
		UpdatedBy   string           `json:"updated_by"`
	} `json:"draft"`
	Messages []struct {
		Role     string  `json:"role"`
		Text     *string `json:"text"`
		ToolCall *struct {
			Title  string `json:"title"`
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"tool_call"`
	} `json:"messages"`
}

// One real claude turn on the cheapest model: asked to create a sensor that emits two static items, the agent edits
// the draft (allowed without asking), runs `hal9001 assist test` (a new script, so the user is asked: the test allows
// it, and checks the approval shows the script) and ends with a summary. Passes when the draft validates, the
// daemon's own test of it returns 2 items, and nothing but that one test asked for a person.
func TestLiveAssistClaudeAuthorsASensor(t *testing.T) {
	requireLive(t)
	rt := runtimesUnderTest[0]
	if !rt.selected() {
		t.Skip("claude not selected by HAL9001_LIVE_RUNTIMES")
	}
	bin := buildCLI(t)
	s := startStack(t, stackOpts{binDir: bin})

	var created liveAssistSession
	s.must(http.StatusCreated, http.MethodPost, "/api/assist/sessions", map[string]any{
		"target": map[string]any{"kind": "sensor"}, "runtime": rt.name, "model": rt.modelName(),
	}, &created)

	const ask = `Create a bash sensor with id "live-two" that emits exactly 2 static items, ` +
		`{"id": 1, "name": "alpha"} and {"id": 2, "name": "beta"}, as a JSON array. ` +
		`Its dedup key is the id, it polls every 10 minutes, and its first run fires. ` +
		`Then test it and tell me in one sentence what the test showed.`
	s.must(http.StatusAccepted, http.MethodPost, "/api/assist/sessions/"+created.ID+"/messages", map[string]any{"text": ask}, nil)

	var got liveAssistSession
	asked := 0
	deadline := time.Now().Add(8 * time.Minute)
	for {
		s.must(http.StatusOK, http.MethodGet, "/api/assist/sessions/"+created.ID, nil, &got)
		if got.Status == "idle" || got.Status == "failed" {
			break
		}
		if got.Status == "awaiting_approval" {
			for _, ap := range s.approvals("status=pending") {
				pr := ap.PermissionRequest
				if pr == nil || pr.ToolKind != "execute" || !strings.Contains(pr.Title, "assist test") {
					t.Fatalf("the agent asked for something other than the test of its script: %+v (%+v)", ap, pr)
				}
				asked++
				t.Logf("approving the test of the new script: %s", pr.Title)
				s.must(http.StatusOK, http.MethodPost, "/api/approvals/"+ap.ID+"/approve", map[string]any{}, nil)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("session still %q after 8m", got.Status)
		}
		time.Sleep(2 * time.Second)
	}
	evidence(t, "assist-session", got)
	if got.Status != "idle" {
		t.Fatalf("turn ended %q: %v", got.Status, got.Error)
	}

	if len(got.Draft.Diagnostics) != 0 || got.Draft.SensorID == nil || got.Draft.UpdatedBy != "agent" {
		t.Errorf("draft id %v by %q, diagnostics %v:\n%s", got.Draft.SensorID, got.Draft.UpdatedBy, got.Draft.Diagnostics, got.Draft.YAML)
	}
	var res struct {
		ExitCode  int `json:"exit_code"`
		ItemCount int `json:"item_count"`
		Items     []struct {
			DedupKey *string `json:"dedup_key"`
		} `json:"items"`
	}
	s.must(http.StatusOK, http.MethodPost, "/api/assist/sessions/"+created.ID+"/test", map[string]any{}, &res)
	evidence(t, "assist-test", res)
	if res.ExitCode != 0 || res.ItemCount != 2 || len(res.Items) != 2 || res.Items[0].DedupKey == nil || res.Items[1].DedupKey == nil {
		t.Errorf("test of the agent's draft = %+v", res)
	}

	// Policy decisions of the turn: the draft edit was allowed by policy, the test of the new script by the user.
	if got.LastAgentRunID == nil {
		t.Fatal("no agent run linked to the session")
	}
	var ranTest bool
	for _, p := range s.permissionRequests(*got.LastAgentRunID) {
		t.Logf("permission: kind=%s title=%q decision=%v by=%v rule=%v", p.ToolKind, p.Title, deref(p.Decision), deref(p.DecidedBy), deref(p.MatchedRule))
		isTest := p.ToolKind == "execute" && strings.Contains(p.Title, "assist test")
		if isTest && deref(p.Decision) == "allow" && deref(p.DecidedBy) == "user" {
			ranTest = true
		}
		if !isTest && p.DecidedBy != nil && *p.DecidedBy == "user" {
			t.Errorf("a tool call other than the test needed a person: %+v", p)
		}
	}
	if !ranTest {
		t.Error("the agent never ran `hal9001 assist test` with the user's approval")
	}
	if asked != 1 {
		t.Errorf("the user was asked %d times, want once (the new script)", asked)
	}
	var summary string
	for _, m := range got.Messages {
		if m.Role == "assistant" && m.Text != nil {
			summary = *m.Text
		}
	}
	if strings.TrimSpace(summary) == "" {
		t.Error("the turn ended without a summary")
	}
	t.Logf("agent summary: %s", summary)
}

package e2e

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"

	"hal9001/internal/hashline"
)

// Drafts are changed line by line: a file is read as anchored lines with its own hash, and an edit names the hash it was
// made against and the anchors of the lines it changes. The agent does it through draft_read and draft_write, everybody
// else through GET and PATCH /api/assist/sessions/{id}/draft; both end in the same storage, so the editor's PUT, the
// validator and the live stream see every change.

type draftLineJSON struct {
	Number int    `json:"number"`
	Anchor string `json:"anchor"`
	Text   string `json:"text"`
}

// draftViewJSON is a draft file read as anchored lines: GET .../draft and the result of draft_read.
type draftViewJSON struct {
	Path            string           `json:"path"`
	Exists          bool             `json:"exists"`
	Saved           bool             `json:"saved"`
	Hash            string           `json:"hash"`
	Lines           []draftLineJSON  `json:"lines"`
	TotalLines      int              `json:"total_lines"`
	HasFinalNewline bool             `json:"has_final_newline"`
	Diagnostics     []map[string]any `json:"diagnostics"`
}

// text rebuilds the file from its lines (LF line endings).
func (v draftViewJSON) text() string {
	texts := make([]string, len(v.Lines))
	for i, l := range v.Lines {
		texts[i] = l.Text
	}
	out := strings.Join(texts, "\n")
	if v.HasFinalNewline && len(v.Lines) > 0 {
		out += "\n"
	}
	return out
}

// anchor is the anchor of line number n.
func (v draftViewJSON) anchor(t *testing.T, n int) string {
	t.Helper()
	for _, l := range v.Lines {
		if l.Number == n {
			return l.Anchor
		}
	}
	t.Fatalf("the view has no line %d: %+v", n, v.Lines)
	return ""
}

// draftEditJSON is the compact result of PATCH .../draft and of draft_write.
type draftEditJSON struct {
	Path        string           `json:"path"`
	Hash        string           `json:"hash"`
	TotalLines  int              `json:"total_lines"`
	Changed     bool             `json:"changed"`
	Affected    []draftLineJSON  `json:"affected"`
	Truncated   bool             `json:"truncated"`
	Diagnostics []map[string]any `json:"diagnostics"`
}

func (e draftEditJSON) affectedNumbers() []int {
	out := []int{}
	for _, l := range e.Affected {
		out = append(out, l.Number)
	}
	return out
}

// anchorsOf returns the anchors a read of text shows, by 1-based line number (index 0 is unused).
func anchorsOf(t *testing.T, text string) []string {
	t.Helper()
	v, err := hashline.Snapshot(text, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(v.Lines)+1)
	for _, l := range v.Lines {
		out[l.Number] = l.Anchor
	}
	return out
}

// wrongAnchor is the anchor of the same line number with another hash, as a line that was changed since the read has.
func wrongAnchor(anchor string) string {
	if strings.HasSuffix(anchor, "0") {
		return anchor[:len(anchor)-1] + "1"
	}
	return anchor[:len(anchor)-1] + "0"
}

func replaceLine(anchor, text string) map[string]any {
	return map[string]any{"op": hashline.OpReplace, "start": anchor, "content": text}
}

// assistCreateFrom starts a session of the kind (sensor, flow or tool) whose draft starts as yaml.
func (d *Daemon) assistCreateFrom(kind, yaml string) assistFlowDetail {
	d.t.Helper()
	var s assistFlowDetail
	d.Post("/api/assist/sessions", map[string]any{"target": map[string]any{"kind": kind}, "runtime": "fake", "yaml": yaml}).Want(201).JSON(&s)
	return s
}

// draftView reads a draft file over the API; query is the query string (path, start, end).
func (d *Daemon) draftView(sessionID, query string) draftViewJSON {
	d.t.Helper()
	path := "/api/assist/sessions/" + sessionID + "/draft"
	if query != "" {
		path += "?" + query
	}
	var v draftViewJSON
	d.Get(path).Want(200).JSON(&v)
	return v
}

func (d *Daemon) draftPatch(sessionID string, body map[string]any) Resp {
	d.t.Helper()
	return d.Patch("/api/assist/sessions/"+sessionID+"/draft", body)
}

// draftEdit applies a PATCH that must succeed and returns its result.
func (d *Daemon) draftEdit(sessionID string, body map[string]any) draftEditJSON {
	d.t.Helper()
	var e draftEditJSON
	d.draftPatch(sessionID, body).Want(200).JSON(&e)
	return e
}

// hasUpdate reports whether the stream carried an assist.updated whose draft has this hash and author.
func (s *assistUIStream) hasUpdate(hash, by string) bool {
	for _, e := range s.of("assist.updated") {
		if dr, _ := e.Data["draft"].(map[string]any); dr["hash"] == hash && dr["updated_by"] == by {
			return true
		}
	}
	return false
}

const hashlineSensor = "# watches the build\nid: build\ninterval: 5m\nlang: bash\nscript: |\n  echo '[{\"id\":1,\"state\":\"red\"}]'\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n"

// The agent changes two lines of a sensor draft instead of rewriting it: every other byte stays, the result lists just
// the lines that changed, and the draft's hash, author and diagnostics are current, on the live stream too. The user
// goes on from those anchors without reading again: a bad value shows its diagnostics at once and the next edit fixes
// them. The CLI does the same through the same endpoints.
func TestAssistHashlineSurgicalEditOfASensorDraft(t *testing.T) {
	a := anchorsOf(t, hashlineSensor)
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false, turn{When: when("make it faster$"), Steps: []step{
			mcpStep("r", "draft_read", map[string]any{}, "read.json"),
			mcpStep("w", "draft_write", map[string]any{
				"expected_hash": "{{sha256:sensor.yaml}}",
				"edits": []map[string]any{
					replaceLine(a[3], "interval: 2m"),
					replaceLine(a[6], "  echo '[{\"id\":1,\"state\":\"green\"}]'"),
				},
			}, "write.json"),
			say("Faster, and green."),
		}})
	})
	s := d.assistCreateFrom("sensor", hashlineSensor)
	stream := d.openAssistStream(s.ID, "assist.updated")

	if got := d.assistTurn(s.ID, "make it faster"); got.Status != "idle" {
		t.Fatalf("turn status %q error %v", got.Status, got.Error)
	}
	faster := strings.Replace(strings.Replace(hashlineSensor, "interval: 5m", "interval: 2m", 1), `"state":"red"`, `"state":"green"`, 1)
	got := d.assistFlowGet(s.ID)
	if got.Draft.YAML != faster || got.Draft.UpdatedBy != "agent" || got.Draft.Hash != hashline.Hash(faster) || got.Draft.Hash == s.Draft.Hash || hasErrorDiag(got.Draft.Diagnostics) {
		t.Fatalf("draft after the agent's edit = %+v, want the seed with two lines changed", got.Draft)
	}
	if got.Draft.Sensor == nil || *got.Draft.Sensor != "build" {
		t.Errorf("draft sensor id = %v, want build", got.Draft.Sensor)
	}
	if file := d.Read("data/assist/" + s.ID + "/sensor.yaml"); file != faster {
		t.Errorf("draft file = %q", file)
	}

	// What the agent saw and what it got back.
	var read draftViewJSON
	if err := json.Unmarshal([]byte(d.assistFile(s.ID, "read.json")), &read); err != nil || read.text() != hashlineSensor || read.Hash != hashline.Hash(hashlineSensor) ||
		read.Path != "sensor.yaml" || read.TotalLines != 9 || read.anchor(t, 3) != a[3] {
		t.Errorf("draft_read = %+v (%v)", read, err)
	}
	var wrote draftEditJSON
	if err := json.Unmarshal([]byte(d.assistFile(s.ID, "write.json")), &wrote); err != nil || !wrote.Changed || wrote.Truncated || wrote.Path != "sensor.yaml" ||
		wrote.Hash != got.Draft.Hash || wrote.TotalLines != 9 || !slices.Equal(wrote.affectedNumbers(), []int{3, 6}) || wrote.Affected[0].Text != "interval: 2m" || len(wrote.Diagnostics) != 0 {
		t.Errorf("draft_write result = %+v (%v), want only the two changed lines", wrote, err)
	}
	run := d.assistRuns(s.ID)[0]
	if ps := d.halPerms(run.ID, "draft_write"); len(ps) != 1 || decisionOf(ps[0]) != "allow" || ps[0].ApprovalID != nil {
		t.Errorf("draft_write records = %+v, want one call allowed without asking", ps)
	}
	d.Eventually("the stream to carry the agent's edit", func() bool { return stream.hasUpdate(got.Draft.Hash, "agent") })

	// The user continues from the anchors of the result: a value that does not validate...
	bad := d.draftEdit(s.ID, map[string]any{"expected_hash": wrote.Hash, "edits": []any{replaceLine(wrote.Affected[0].Anchor, "interval: soon")}})
	if !bad.Changed || !slices.Equal(bad.affectedNumbers(), []int{3}) || !hasErrorDiag(bad.Diagnostics) {
		t.Fatalf("an invalid interval = %+v, want the line and an error diagnostic", bad)
	}
	broken := d.assistFlowGet(s.ID)
	if broken.Draft.YAML != strings.Replace(faster, "interval: 2m", "interval: soon", 1) || broken.Draft.UpdatedBy != "user" || len(broken.Draft.Diagnostics) != len(bad.Diagnostics) || broken.Draft.Hash != bad.Hash {
		t.Errorf("draft with the invalid interval = %+v", broken.Draft)
	}
	d.Eventually("the stream to carry the diagnostics", func() bool { return stream.hasUpdate(bad.Hash, "user") })
	// ...and the next edit puts it right: the same bytes, so the same hash.
	fixed := d.draftEdit(s.ID, map[string]any{"expected_hash": bad.Hash, "edits": []any{replaceLine(bad.Affected[0].Anchor, "interval: 2m")}})
	if fixed.Hash != got.Draft.Hash || hasErrorDiag(fixed.Diagnostics) || d.assistFlowGet(s.ID).Draft.YAML != faster {
		t.Errorf("after the fix = %+v, want the agent's version again", fixed)
	}

	// The CLI reads one line and changes it, and a stale hash is an error that changes nothing.
	out, errOut, code := d.CLI("", "assist", "draft-read", "--start", "4", "--end", "4", s.ID)
	var one draftViewJSON
	if code != 0 || json.Unmarshal([]byte(out), &one) != nil || len(one.Lines) != 1 || one.Lines[0].Text != "lang: bash" || one.TotalLines != 9 || one.Hash != fixed.Hash {
		t.Fatalf("assist draft-read: exit %d, out %s, stderr %s", code, out, errOut)
	}
	edit := `[{"op":"replace","start":"` + one.Lines[0].Anchor + `","content":"lang: python"}]`
	if _, errOut, code = d.CLI(edit, "assist", "draft-edit", "--expected-hash", strings.Repeat("0", 64), "--edits", "-", s.ID); code == 0 || !strings.Contains(errOut, "conflict") {
		t.Errorf("assist draft-edit with a stale hash: exit %d, stderr %s", code, errOut)
	}
	if d.assistFlowGet(s.ID).Draft.YAML != faster {
		t.Error("a refused edit changed the draft")
	}
	out, errOut, code = d.CLI(edit, "assist", "draft-edit", "--expected-hash", fixed.Hash, "--edits", "-", s.ID)
	var cliEdit draftEditJSON
	if code != 0 || json.Unmarshal([]byte(out), &cliEdit) != nil || !cliEdit.Changed || !slices.Equal(cliEdit.affectedNumbers(), []int{4}) {
		t.Fatalf("assist draft-edit: exit %d, out %s, stderr %s", code, out, errOut)
	}
	if y := d.assistFlowGet(s.ID).Draft.YAML; y != strings.Replace(faster, "lang: bash", "lang: python", 1) {
		t.Errorf("draft after the CLI edit = %q", y)
	}
}

// A change made against an old version of a file is refused and changes nothing, whoever sends it: the editor's version
// moved the hash, so a PATCH with the hash it read, a create over a file that exists, an anchor that no longer matches
// its line, a request that is no edit and the agent's draft_write with a stale hash all leave the draft as it is. The
// same edit against the file as it is now goes through.
func TestAssistHashlineStaleExpectedHashIsRejectedAndTheDraftIsUnchanged(t *testing.T) {
	a := anchorsOf(t, hashlineSensor)
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false, turn{When: when("tweak it$"), Steps: []step{
			mcpStep("w", "draft_write", map[string]any{
				"expected_hash": strings.Repeat("0", 64),
				"edits":         []map[string]any{replaceLine(a[3], "interval: 1m")},
			}, ""),
			say("Tweaked."),
		}})
	})
	s := d.assistCreateFrom("sensor", hashlineSensor)
	draftPath := "/api/assist/sessions/" + s.ID + "/draft"

	read := d.draftView(s.ID, "")
	if !read.Exists || read.Path != "sensor.yaml" || read.Hash != s.Draft.Hash || read.text() != hashlineSensor || read.TotalLines != 9 || !read.HasFinalNewline {
		t.Fatalf("GET draft = %+v, want the draft with the hash the session shows", read)
	}

	// The editor saves its own version: the hash of the draft moves.
	mine := strings.Replace(hashlineSensor, "timeout: 10s", "timeout: 30s", 1)
	var put assistDraftJSON
	d.Put(draftPath, map[string]any{"yaml": mine, "expected_hash": read.Hash}).Want(200).JSON(&put)
	unchanged := func(what string) {
		t.Helper()
		if got := d.assistFlowGet(s.ID); got.Draft.YAML != mine || got.Draft.Hash != put.Hash || got.Draft.UpdatedBy != "user" || d.Read("data/assist/"+s.ID+"/sensor.yaml") != mine {
			t.Errorf("%s changed the draft: %+v", what, got.Draft)
		}
	}

	var conflict struct {
		Expected string  `json:"expected_hash"`
		Current  *string `json:"current_hash"`
	}
	r := d.draftPatch(s.ID, map[string]any{"expected_hash": read.Hash, "edits": []any{replaceLine(read.anchor(t, 3), "interval: 1m")}}).Want(409)
	r.JSON(&conflict)
	if r.Problem() != "conflict" || conflict.Expected != read.Hash || conflict.Current == nil || *conflict.Current != put.Hash {
		t.Errorf("a stale hash = %s", r.Body)
	}
	unchanged("a stale hash")

	// An empty hash creates a file that is not there; it never overwrites the draft.
	r = d.draftPatch(s.ID, map[string]any{"expected_hash": "", "content": "id: other\n"}).Want(409)
	r.JSON(&conflict)
	if r.Problem() != "conflict" || conflict.Expected != "" || conflict.Current == nil || *conflict.Current != put.Hash {
		t.Errorf("create over an existing file = %s", r.Body)
	}
	unchanged("a create over the draft")

	// Requests that are no edit, and an anchor that does not match its line, are validation errors.
	now := d.draftView(s.ID, "")
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"no hash", map[string]any{"content": "id: other\n"}},
		{"a malformed hash", map[string]any{"expected_hash": "abc", "content": "id: other\n"}},
		{"neither edits nor content", map[string]any{"expected_hash": now.Hash}},
		{"both edits and content", map[string]any{"expected_hash": now.Hash, "content": "id: other\n", "edits": []any{replaceLine(now.anchor(t, 3), "interval: 1m")}}},
		{"a changed line", map[string]any{"expected_hash": now.Hash, "edits": []any{replaceLine(wrongAnchor(now.anchor(t, 3)), "interval: 1m")}}},
		{"a file the session does not have", map[string]any{"path": "prompts/x.md", "expected_hash": "", "content": "x\n"}},
	} {
		if r := d.draftPatch(s.ID, tc.body).Want(422); r.Problem() != "validation-failed" {
			t.Errorf("%s: problem %q: %s", tc.name, r.Problem(), r.Body)
		}
	}
	unchanged("a request that is no edit")

	// The agent's stale hash is a tool error that says what to do, and changes nothing either.
	if got := d.assistTurn(s.ID, "tweak it"); got.Status != "idle" {
		t.Fatalf("turn status %q error %v", got.Status, got.Error)
	}
	tr := d.Transcript(d.assistRuns(s.ID)[0].ID)
	if !strings.Contains(tr, "mcp[w] is_error=true") || !strings.Contains(tr, "changed since it was read") || !strings.Contains(tr, "draft_read") {
		t.Errorf("transcript lacks the conflict the agent should get:\n%s", tr)
	}
	unchanged("the agent's stale hash")

	// With the hash and the anchors of what the draft is now, the edit goes through.
	done := d.draftEdit(s.ID, map[string]any{"expected_hash": now.Hash, "edits": []any{replaceLine(now.anchor(t, 3), "interval: 1m")}})
	if want := strings.Replace(mine, "interval: 5m", "interval: 1m", 1); !done.Changed || d.assistFlowGet(s.ID).Draft.YAML != want || done.Hash != hashline.Hash(want) {
		t.Errorf("the edit against the current hash = %+v", done)
	}

	// A general session has no draft to read or change.
	general := d.assistCreateGeneral()
	if r := d.Get("/api/assist/sessions/" + general.ID + "/draft").Want(409); r.Problem() != "invalid-state" {
		t.Errorf("GET draft of a general session: %s", r.Body)
	}
	if r := d.draftPatch(general.ID, map[string]any{"expected_hash": "", "content": "x"}).Want(409); r.Problem() != "invalid-state" {
		t.Errorf("PATCH draft of a general session: %s", r.Body)
	}
	d.Get("/api/assist/sessions/01JC7Z3K9Q8V5T2M4N6P8R0S1T/draft").Want(404)
}

const hashlinePrompt = "---\nname: triage-note\ndescription: triage the item\noutput: note\n---\nRead {{.input.title}} and say whether it needs attention.\nBe brief.\n"

// A flow session's prompt is a file of its own: it is created with an empty hash and then changed by anchors against
// the hash of that file, by the user and by the agent. The draft's own hash does not move, the session lists the prompt
// with its new text, and creating the same name twice is refused.
func TestAssistHashlineFlowPromptCreateThenEdit(t *testing.T) {
	shorter := strings.Replace(hashlinePrompt, "Be brief.", "Answer in one sentence.", 1)
	rated := strings.Replace(shorter, "and say whether it needs attention.", "and rate it low, normal or urgent.", 1)
	d := newAssistDaemon(t, func(d *Daemon) {
		srcSensor(d)
		d.assistRuntime(false, turn{When: when("rate it$"), Steps: []step{
			mcpStep("w", "draft_write", map[string]any{
				"path":          "prompts/triage-note.md",
				"expected_hash": "{{sha256:prompts/triage-note.md}}",
				"edits":         []map[string]any{replaceLine(anchorsOf(t, shorter)[6], "Read {{.input.title}} and rate it low, normal or urgent.")},
			}, "write.json"),
			say("Rated."),
		}})
	})
	s := d.assistCreateFlow("")
	stream := d.openAssistStream(s.ID, "assist.updated")
	query := "path=" + url.QueryEscape("prompts/triage-note.md")

	// A prompt that does not exist yet reads as an empty file with no hash.
	missing := d.draftView(s.ID, query)
	if missing.Exists || missing.Hash != "" || len(missing.Lines) != 0 || missing.Path != "prompts/triage-note.md" || missing.TotalLines != 0 {
		t.Fatalf("a missing prompt = %+v", missing)
	}

	created := d.draftEdit(s.ID, map[string]any{"path": "prompts/triage-note.md", "expected_hash": "", "content": hashlinePrompt})
	if !created.Changed || created.Path != "prompts/triage-note.md" || created.Hash != hashline.Hash(hashlinePrompt) || created.TotalLines != 7 || len(created.Affected) != 7 {
		t.Fatalf("created prompt = %+v", created)
	}
	f := d.assistFlowGet(s.ID)
	if len(f.Draft.Prompts) != 1 || f.Draft.Prompts[0].Name != "triage-note" || f.Draft.Prompts[0].YAML != hashlinePrompt || f.Draft.Prompts[0].Exists {
		t.Fatalf("draft prompts = %+v", f.Draft.Prompts)
	}
	if f.Draft.Hash != s.Draft.Hash || f.Draft.YAML != s.Draft.YAML || f.Draft.UpdatedBy != "user" {
		t.Errorf("a new prompt changed the flow draft: %+v", f.Draft)
	}
	d.Eventually("the stream to carry the new prompt", func() bool {
		for _, e := range stream.of("assist.updated") {
			if dr, _ := e.Data["draft"].(map[string]any); dr["prompts"] != nil && len(dr["prompts"].([]any)) == 1 {
				return true
			}
		}
		return false
	})
	r := d.draftPatch(s.ID, map[string]any{"path": "prompts/triage-note.md", "expected_hash": "", "content": "other\n"}).Want(409)
	if r.Problem() != "conflict" {
		t.Errorf("creating the prompt twice = %s", r.Body)
	}

	// The user changes the last line against the hash of the prompt.
	read := d.draftView(s.ID, query)
	if !read.Exists || read.Hash != created.Hash || read.text() != hashlinePrompt {
		t.Fatalf("prompt read = %+v", read)
	}
	edited := d.draftEdit(s.ID, map[string]any{"path": "prompts/triage-note.md", "expected_hash": read.Hash, "edits": []any{replaceLine(read.anchor(t, 7), "Answer in one sentence.")}})
	if edited.Hash != hashline.Hash(shorter) || !slices.Equal(edited.affectedNumbers(), []int{7}) {
		t.Errorf("edited prompt = %+v", edited)
	}
	if f = d.assistFlowGet(s.ID); f.Draft.Prompts[0].YAML != shorter || f.Draft.Hash != s.Draft.Hash {
		t.Errorf("after the user's edit: prompts %+v, flow draft hash %s (was %s)", f.Draft.Prompts, f.Draft.Hash, s.Draft.Hash)
	}
	// The hash of the file moved, so the old one is stale for the prompt, while the draft's hash is still good for flow.yaml.
	d.draftPatch(s.ID, map[string]any{"path": "prompts/triage-note.md", "expected_hash": read.Hash, "edits": []any{replaceLine(read.anchor(t, 7), "x")}}).Want(409)
	d.Put("/api/assist/sessions/"+s.ID+"/draft", map[string]any{"yaml": s.Draft.YAML, "expected_hash": s.Draft.Hash}).Want(200)

	// The agent changes the line before it, against the hash of the file it reads.
	if got := d.assistTurn(s.ID, "rate it"); got.Status != "idle" {
		t.Fatalf("turn status %q error %v", got.Status, got.Error)
	}
	f = d.assistFlowGet(s.ID)
	if len(f.Draft.Prompts) != 1 || f.Draft.Prompts[0].YAML != rated || f.Draft.Hash != s.Draft.Hash || f.Draft.UpdatedBy != "agent" {
		t.Errorf("after the agent's edit: prompts %+v, draft by %q", f.Draft.Prompts, f.Draft.UpdatedBy)
	}
	var wrote draftEditJSON
	if err := json.Unmarshal([]byte(d.assistFile(s.ID, "write.json")), &wrote); err != nil || wrote.Hash != hashline.Hash(rated) || wrote.Path != "prompts/triage-note.md" || !slices.Equal(wrote.affectedNumbers(), []int{6}) {
		t.Errorf("draft_write result = %+v (%v)", wrote, err)
	}
	for _, dg := range f.Draft.Diagnostics {
		if dg["severity"] == "error" {
			t.Errorf("the prompt does not validate: %v", dg)
		}
	}
}

// The first edit of a saved sensor starts from the saved file and its hash, copies it into the draft, and records the hash
// of the saved file as the base Save compares with. Later edits use the draft's own hash and leave the base alone, so
// Save still reports the change that was made to the saved file in the meantime.
func TestAssistHashlineSavedSensorEditKeepsTheBaseHashForSave(t *testing.T) {
	var savedText string
	d := newAssistDaemon(t, func(d *Daemon) {
		srcSensor(d)
		savedText = d.Read("sensors/src.yaml")
		d.assistRuntime(false, turn{When: when("slower$"), Steps: []step{
			mcpStep("w", "draft_write", map[string]any{
				"path":          "sensors/src.yaml",
				"expected_hash": "{{sha256:" + d.Path("sensors/src.yaml") + "}}",
				"edits":         []map[string]any{replaceLine(anchorsOf(t, savedText)[2], "interval: 6h")},
			}, "write.json"),
			say("Slower."),
		}})
	})
	s := d.assistCreateFlow("")
	query := "path=" + url.QueryEscape("sensors/src.yaml")
	var saved struct {
		Hash string `json:"hash"`
	}
	d.Get("/api/sensors/src").Want(200).JSON(&saved)

	// Before the edit the saved sensor reads as itself, flagged as saved: it is not in the draft yet.
	before := d.draftView(s.ID, query)
	if !before.Exists || !before.Saved || before.Hash != saved.Hash || before.text() != savedText {
		t.Fatalf("an undrafted saved sensor = %+v, want the saved file and its hash", before)
	}
	if f := d.assistFlowGet(s.ID); len(f.Draft.Sensors) != 0 {
		t.Fatalf("reading copied the sensor into the draft: %+v", f.Draft.Sensors)
	}
	// An empty hash cannot create a sensor that is saved, and a stale hash cannot edit it.
	d.draftPatch(s.ID, map[string]any{"path": "sensors/src.yaml", "expected_hash": "", "content": "interval: 1m\n"}).Want(409)
	d.draftPatch(s.ID, map[string]any{"path": "sensors/src.yaml", "expected_hash": strings.Repeat("0", 64), "edits": []any{replaceLine(before.anchor(t, 2), "interval: 1m")}}).Want(409)
	if f := d.assistFlowGet(s.ID); len(f.Draft.Sensors) != 0 {
		t.Fatalf("a refused edit copied the sensor into the draft: %+v", f.Draft.Sensors)
	}

	if got := d.assistTurn(s.ID, "slower"); got.Status != "idle" {
		t.Fatalf("turn status %q error %v", got.Status, got.Error)
	}
	slower := strings.Replace(savedText, "interval: 1h", "interval: 6h", 1)
	f := d.assistFlowGet(s.ID)
	if len(f.Draft.Sensors) != 1 || f.Draft.Sensors[0].ID != "src" || f.Draft.Sensors[0].YAML != slower || !f.Draft.Sensors[0].Exists ||
		f.Draft.Sensors[0].BaseHash == nil || *f.Draft.Sensors[0].BaseHash != saved.Hash {
		t.Fatalf("draft sensors = %+v, want the saved file with one line changed and its hash as the base (%s)", f.Draft.Sensors, saved.Hash)
	}
	if f.Draft.Hash != s.Draft.Hash || d.Read("sensors/src.yaml") != savedText {
		t.Error("the flow draft's hash moved, or the saved sensor changed before Save")
	}
	var wrote draftEditJSON
	if err := json.Unmarshal([]byte(d.assistFile(s.ID, "write.json")), &wrote); err != nil || wrote.Hash != hashline.Hash(slower) || !slices.Equal(wrote.affectedNumbers(), []int{2}) {
		t.Errorf("draft_write result = %+v (%v)", wrote, err)
	}

	// The copy is a draft now, with a hash of its own.
	copied := d.draftView(s.ID, query)
	if !copied.Exists || copied.Saved || copied.Hash != hashline.Hash(slower) || copied.text() != slower {
		t.Fatalf("the drafted sensor = %+v", copied)
	}

	// The saved file changes meanwhile; the next edit of the draft is made against the draft's hash and must not
	// move the base, or Save would overwrite that change without a word.
	d.Write("sensors/src.yaml", "interval: 2h\nlang: bash\nscript: \"true\"\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n")
	d.draftEdit(s.ID, map[string]any{"path": "sensors/src.yaml", "expected_hash": copied.Hash, "edits": []any{replaceLine(copied.anchor(t, 2), "interval: 12h")}})
	f = d.assistFlowGet(s.ID)
	if len(f.Draft.Sensors) != 1 || !strings.Contains(f.Draft.Sensors[0].YAML, "interval: 12h") || f.Draft.Sensors[0].BaseHash == nil || *f.Draft.Sensors[0].BaseHash != saved.Hash {
		t.Fatalf("draft sensors after the second edit = %+v, want the base to stay %s", f.Draft.Sensors, saved.Hash)
	}
	warned := false
	for _, dg := range f.Draft.Diagnostics {
		warned = warned || (dg["severity"] == "warning" && dg["file"] == "sensors/src.yaml")
	}
	if !warned {
		t.Errorf("no warning that the saved sensor changed: %+v", f.Draft.Diagnostics)
	}
	d.Put("/api/sensors/src", map[string]any{"expected_hash": *f.Draft.Sensors[0].BaseHash, "yaml": f.Draft.Sensors[0].YAML}).Want(409)
}

// A tool session's draft is edited line by line too: the agent changes the script of the draft tool, and the user's
// test of the draft runs the new script.
func TestAssistHashlineToolDraftEdit(t *testing.T) {
	const seed = "id: shout\ndescription: Upper-case a word\nparams:\n  word: {type: string, required: true}\nlang: bash\nscript: |\n  echo \"$ARG_WORD\" | tr a-z A-Z\napprove: never\n"
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false, turn{When: when("scramble it$"), Steps: []step{
			mcpStep("w", "draft_write", map[string]any{
				"expected_hash": "{{sha256:tool.yaml}}",
				"edits":         []map[string]any{replaceLine(anchorsOf(t, seed)[7], "  echo \"$ARG_WORD\" | tr a-z n-za-m")},
			}, "write.json"),
			say("Scrambled."),
		}})
	})
	s := d.assistCreateFrom("tool", seed)
	test := "/api/assist/sessions/" + s.ID + "/test"
	var res toolTestJSON
	d.Post(test, map[string]any{"args": map[string]any{"word": "hal"}}).Want(200).JSON(&res)
	if !res.OK || strings.TrimSpace(res.Stdout) != "HAL" {
		t.Fatalf("test of the seed = %+v", res)
	}

	if got := d.assistTurn(s.ID, "scramble it"); got.Status != "idle" {
		t.Fatalf("turn status %q error %v", got.Status, got.Error)
	}
	scrambled := strings.Replace(seed, "tr a-z A-Z", "tr a-z n-za-m", 1)
	f := d.assistFlowGet(s.ID)
	if f.Draft.YAML != scrambled || f.Draft.UpdatedBy != "agent" || hasErrorDiag(f.Draft.Diagnostics) || f.Draft.Hash != hashline.Hash(scrambled) {
		t.Fatalf("tool draft = %+v, want the seed with the script line changed", f.Draft)
	}
	view := d.draftView(s.ID, "")
	if view.Path != "tool.yaml" || view.text() != scrambled || view.TotalLines != 8 || view.Hash != f.Draft.Hash {
		t.Errorf("tool draft read = %+v", view)
	}
	var wrote draftEditJSON
	if err := json.Unmarshal([]byte(d.assistFile(s.ID, "write.json")), &wrote); err != nil || wrote.Path != "tool.yaml" || !slices.Equal(wrote.affectedNumbers(), []int{7}) || wrote.Hash != f.Draft.Hash {
		t.Errorf("draft_write result = %+v (%v)", wrote, err)
	}

	d.Post(test, map[string]any{"args": map[string]any{"word": "hal"}}).Want(200).JSON(&res)
	if !res.OK || strings.TrimSpace(res.Stdout) != "uny" {
		t.Errorf("test of the edited draft = %+v, want the new script to run", res)
	}
	// A tool session has no prompts or sensors to draft.
	d.draftPatch(s.ID, map[string]any{"path": "prompts/x.md", "expected_hash": "", "content": "x\n"}).Want(422)
	d.Get("/api/assist/sessions/" + s.ID + "/draft?path=" + url.QueryEscape("sensors/x.yaml")).Want(422)
}

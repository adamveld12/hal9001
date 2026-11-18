package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"hal9001/internal/hashline"
	"hal9001/internal/sandbox"
	"hal9001/internal/spec"
)

// fileToolbox is the file tools of a local sandbox, called the way the hub calls them.
type fileToolbox struct {
	tools map[string]Tool
	dir   string // the workdir
}

// newFileToolbox builds the file tools of a local sandbox with the given policy. secrets are values the sandbox hides.
func newFileToolbox(t *testing.T, cfg spec.Sandbox, secrets ...string) *fileToolbox {
	t.Helper()
	env := sandbox.Env{Vars: map[string]string{}, Secrets: secrets}
	for i, s := range secrets {
		env.Vars[fmt.Sprintf("SECRET_%d", i)] = s
	}
	sess := localSessionWith(t, env)
	policy, err := sandbox.NewPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &sandboxTools{name: "local", sess: sess, policy: policy}
	box := &fileToolbox{tools: map[string]Tool{}, dir: sess.Workdir()}
	for _, tl := range st.tools() {
		box.tools[tl.Name] = tl
	}
	return box
}

// seed puts a file in the workdir behind the tools' back, as another process would.
func (b *fileToolbox) seed(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(b.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// content is what the file holds on disk.
func (b *fileToolbox) content(t *testing.T, rel string) string {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(b.dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func (b *fileToolbox) args(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// run calls a tool as the hub does for a call that is allowed without asking: Run with the arguments.
func (b *fileToolbox) run(t *testing.T, name string, v map[string]any) (string, error) {
	t.Helper()
	res, err := b.tools[name].Run(context.Background(), Call{Name: name, Args: b.args(t, v), Approval: "auto: test"})
	return res.Text, err
}

func replaceLine(anchor, content string) map[string]any {
	return map[string]any{"op": hashline.OpReplace, "start": anchor, "content": content}
}

func approvalField(at ApprovalText, key string) string {
	for _, f := range at.Fields {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

func snapshotOf(t *testing.T, content string) hashline.View {
	t.Helper()
	v, err := hashline.Snapshot(content, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The agent reads a file under anchors and its revision, changes some lines by anchor and gets the new revision and the
// anchors of the changed lines back; the other lines are not touched and not repeated.
func TestSandboxHashlineEditChangesOnlyTheRequestedLines(t *testing.T) {
	box := newFileToolbox(t, spec.Sandbox{Approval: spec.ApprovalAuto})
	const original = "alpha\nbeta\ngamma\ndelta\n"
	out, err := box.run(t, "fs_write", map[string]any{"path": "notes/a.txt", "expected_hash": "", "content": original})
	if err != nil || !strings.Contains(out, "created notes/a.txt") {
		t.Fatalf("create: %q, %v", out, err)
	}

	view := snapshotOf(t, original)
	read, err := box.run(t, "fs_read", map[string]any{"path": "notes/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read, "revision "+view.Hash) {
		t.Errorf("fs_read does not give the revision:\n%s", read)
	}
	for _, l := range view.Lines {
		if !strings.Contains(read, l.Anchor+"|"+l.Text+"\n") {
			t.Errorf("fs_read lacks the line %s:\n%s", l.Anchor, read)
		}
	}

	out, err = box.run(t, "fs_write", map[string]any{
		"path": "notes/a.txt", "expected_hash": view.Hash,
		"edits": []any{
			replaceLine(view.Lines[1].Anchor, "BETA"),
			map[string]any{"op": hashline.OpInsertAfter, "start": view.Lines[3].Anchor, "content": "epsilon"},
		},
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	const edited = "alpha\nBETA\ngamma\ndelta\nepsilon\n"
	if got := box.content(t, "notes/a.txt"); got != edited {
		t.Fatalf("file = %q, want %q", got, edited)
	}
	after := snapshotOf(t, edited)
	for _, want := range []string{"revision: " + after.Hash, after.Lines[1].Anchor + "|BETA", after.Lines[4].Anchor + "|epsilon"} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
	for _, repeated := range []string{"|alpha", "|gamma", "|delta"} {
		if strings.Contains(out, repeated) {
			t.Errorf("result repeats the unchanged line %q:\n%s", repeated, out)
		}
	}

	// The new revision and anchors serve the next edit without a read.
	_, err = box.run(t, "fs_write", map[string]any{
		"path": "notes/a.txt", "expected_hash": after.Hash,
		"edits": []any{map[string]any{"op": hashline.OpDelete, "start": after.Lines[2].Anchor}},
	})
	if err != nil {
		t.Fatalf("second edit: %v", err)
	}
	if got := box.content(t, "notes/a.txt"); got != "alpha\nBETA\ndelta\nepsilon\n" {
		t.Errorf("file after the second edit = %q", got)
	}
}

// A call made against a revision the file no longer has, whether it sends content or edits, writes nothing and tells the
// agent both revisions; so does an anchor whose line changed.
func TestSandboxHashlineStaleRevisionLeavesTheFileUntouched(t *testing.T) {
	box := newFileToolbox(t, spec.Sandbox{Approval: spec.ApprovalAuto})
	const read = "one\ntwo\nthree\n"
	box.seed(t, "f.txt", read)
	view := snapshotOf(t, read)
	const changed = "one\ntwo changed\nthree\n"
	box.seed(t, "f.txt", changed)

	for name, req := range map[string]map[string]any{
		"edits":   {"edits": []any{replaceLine(view.Lines[0].Anchor, "ONE")}},
		"content": {"content": "rewritten\n"},
	} {
		req["path"], req["expected_hash"] = "f.txt", view.Hash
		_, err := box.run(t, "fs_write", req)
		var ce *hashline.ConflictError
		if !errors.As(err, &ce) || ce.Expected != view.Hash || ce.Current != hashline.Hash(changed) {
			t.Errorf("%s: error = %v, want a conflict between the revision sent and the current one", name, err)
		}
		if got := box.content(t, "f.txt"); got != changed {
			t.Errorf("%s: file = %q, want it untouched", name, got)
		}
	}

	_, err := box.run(t, "fs_write", map[string]any{
		"path": "f.txt", "expected_hash": hashline.Hash(changed),
		"edits": []any{replaceLine(view.Lines[1].Anchor, "TWO")},
	})
	if err == nil {
		t.Error("an anchor of a line that changed was accepted")
	}
	if got := box.content(t, "f.txt"); got != changed {
		t.Errorf("file = %q after the stale anchor, want it untouched", got)
	}
}

// An empty expected_hash creates and never overwrites, not even an empty file; a call that does not say what it
// expects (the shape of the former fs_write) is refused.
func TestSandboxHashlineCreateOnlyNeverOverwrites(t *testing.T) {
	box := newFileToolbox(t, spec.Sandbox{Approval: spec.ApprovalAuto})
	box.seed(t, "full.txt", "keep me\n")
	box.seed(t, "empty.txt", "")
	for _, p := range []string{"full.txt", "empty.txt"} {
		_, err := box.run(t, "fs_write", map[string]any{"path": p, "expected_hash": "", "content": "new\n"})
		var ce *hashline.ConflictError
		if !errors.As(err, &ce) || ce.Expected != "" || ce.Current == "" {
			t.Errorf("%s: error = %v, want a conflict because the file exists", p, err)
		}
	}
	if got := box.content(t, "full.txt"); got != "keep me\n" {
		t.Errorf("full.txt = %q", got)
	}
	if got := box.content(t, "empty.txt"); got != "" {
		t.Errorf("empty.txt = %q", got)
	}

	_, err := box.run(t, "fs_write", map[string]any{"path": "new.txt", "content": "x"})
	if err == nil || !strings.Contains(err.Error(), "expected_hash") {
		t.Errorf("a call without expected_hash: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(box.dir, "new.txt")); statErr == nil {
		t.Error("a call without expected_hash created the file")
	}
}

// With approval, the user is shown the diff of what the call really does to the file as it is, secret values aside. A
// call that cannot be applied fails before the user is asked. The write checks the revision again: a file that changed
// while the approval was pending is not written.
func TestSandboxHashlineApprovalShowsTheRealDiffAndRechecksTheRevision(t *testing.T) {
	box := newFileToolbox(t, spec.Sandbox{Approval: spec.ApprovalAlways})
	tl := box.tools["fs_write"]
	const original = "one\ntwo\nthree\n"
	box.seed(t, "f.txt", original)
	view := snapshotOf(t, original)
	args := box.args(t, map[string]any{"path": "f.txt", "expected_hash": view.Hash, "edits": []any{replaceLine(view.Lines[1].Anchor, "TWO")}})

	verdict, why := approval(tl, args)
	if verdict != VerdictApprove {
		t.Fatalf("verdict = %d (%s)", verdict, why)
	}
	for _, want := range []string{"edit f.txt (1 edit)", "\n one\n", "\n-two\n", "\n+TWO\n", "\n three"} {
		if !strings.Contains(why, want) {
			t.Errorf("the approval lacks %q:\n%s", want, why)
		}
	}
	if got := box.content(t, "f.txt"); got != original {
		t.Fatalf("asking for approval changed the file: %q", got)
	}
	at := DescribeCall(tl, args, why)
	if at.Title != "Edit f.txt" || approvalField(at, "mode") != "edit" || approvalField(at, "edits") != "1" || approvalField(at, "path") != "f.txt" {
		t.Errorf("approval text = %+v", at)
	}

	// A change that leaves the file as it is needs no approval.
	same := box.args(t, map[string]any{"path": "f.txt", "expected_hash": view.Hash, "edits": []any{replaceLine(view.Lines[0].Anchor, "one")}})
	if v, _ := approval(tl, same); v != VerdictAllow {
		t.Errorf("a change that changes nothing needs approval (verdict %d)", v)
	}

	// Creating a file is previewed as the whole new file.
	create := box.args(t, map[string]any{"path": "new/n.txt", "expected_hash": "", "content": "a\nb\n"})
	if v, why := approval(tl, create); v != VerdictApprove || !strings.Contains(why, "create new/n.txt") || !strings.Contains(why, "\n+a\n+b") {
		t.Errorf("create preview: verdict %d\n%s", v, why)
	}

	// The file changes while the approval is pending: the approved call writes nothing.
	const changed = "one\ntwo (edited elsewhere)\nthree\n"
	box.seed(t, "f.txt", changed)
	_, err := tl.Run(context.Background(), Call{Name: "fs_write", Args: args, Approval: "approved by user"})
	var ce *hashline.ConflictError
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "while the approval was pending") {
		t.Errorf("approved call on a changed file: %v", err)
	}
	if got := box.content(t, "f.txt"); got != changed {
		t.Errorf("file = %q, want the change made elsewhere and nothing of the approved call", got)
	}

	// A call that was made against the old revision is not put to the user at all.
	if v, why := approval(tl, args); v != VerdictDeny || !strings.Contains(why, "not asked") {
		t.Errorf("stale call: verdict %d (%s)", v, why)
	}
}

// The approval of an edit to a large file still shows the diff.
func TestSandboxHashlineApprovalDiffOfALargeFile(t *testing.T) {
	box := newFileToolbox(t, spec.Sandbox{Approval: spec.ApprovalAlways})
	var b strings.Builder
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	box.seed(t, "big.txt", b.String())
	view := snapshotOf(t, b.String())
	args := box.args(t, map[string]any{"path": "big.txt", "expected_hash": view.Hash, "edits": []any{replaceLine(view.Lines[1499].Anchor, "LINE 1500")}})
	verdict, why := approval(box.tools["fs_write"], args)
	if verdict != VerdictApprove || !strings.Contains(why, "\n-line 1500\n") || !strings.Contains(why, "\n+LINE 1500\n") || strings.Contains(why, "too large") {
		t.Errorf("verdict %d, approval:\n%.600s", verdict, why)
	}
}

// The approval rules by path decide the same for whole-file and for edit calls.
func TestSandboxHashlinePolicyRulesApplyToEditsAndWholeFiles(t *testing.T) {
	cfg := spec.Sandbox{Approval: spec.ApprovalRules, Rules: spec.SandboxRules{FSWrite: []spec.SandboxPathRule{
		{Path: "notes/**", Action: spec.RuleAuto},
		{Path: "locked/**", Action: spec.RuleDeny},
	}}}
	box := newFileToolbox(t, cfg)
	const original = "x\ny\n"
	view := snapshotOf(t, original)
	for _, tc := range []struct {
		path string
		want Verdict
	}{{"notes/a.txt", VerdictAllow}, {"locked/a.txt", VerdictDeny}, {"other/a.txt", VerdictApprove}} {
		box.seed(t, tc.path, original)
		for mode, req := range map[string]map[string]any{
			"edits":   {"edits": []any{replaceLine(view.Lines[0].Anchor, "X")}},
			"content": {"content": "X\ny\n"},
		} {
			req["path"], req["expected_hash"] = tc.path, view.Hash
			if got, why := approval(box.tools["fs_write"], box.args(t, req)); got != tc.want {
				t.Errorf("%s %s: verdict %d (%s), want %d", mode, tc.path, got, why, tc.want)
			}
		}
	}
}

// A line that holds a secret value of the sandbox is read without the value and marked, cannot be replaced or deleted
// by any call, and the secret bytes survive an edit of other lines. The value reaches no read, search hit, approval or error.
func TestSandboxHashlineSecretLinesAreHiddenAndSurviveEdits(t *testing.T) {
	const secret = "s3cr3t-token-value"
	box := newFileToolbox(t, spec.Sandbox{Approval: spec.ApprovalAuto}, secret)
	original := "name = demo\ntoken = " + secret + "\nmode = a\n"
	box.seed(t, "app.ini", original)
	view := snapshotOf(t, original)
	noLeak := func(what, text string) {
		t.Helper()
		if strings.Contains(text, secret) {
			t.Errorf("%s shows the secret value:\n%s", what, text)
		}
	}

	read, err := box.run(t, "fs_read", map[string]any{"path": "app.ini"})
	if err != nil {
		t.Fatal(err)
	}
	noLeak("fs_read", read)
	// The anchor is the one of the stored line, so an edit made against it applies.
	if want := view.Lines[1].Anchor + "|token = " + sandbox.Redacted + hiddenLineMark + "\n"; !strings.Contains(read, want) {
		t.Errorf("fs_read lacks the marked line %q:\n%s", want, read)
	}
	if !strings.Contains(read, "revision "+view.Hash) {
		t.Errorf("fs_read does not give the revision of the stored file:\n%s", read)
	}

	// An edit of another line keeps the secret bytes in the file.
	out, err := box.run(t, "fs_write", map[string]any{"path": "app.ini", "expected_hash": view.Hash, "edits": []any{replaceLine(view.Lines[2].Anchor, "mode = b")}})
	if err != nil {
		t.Fatal(err)
	}
	noLeak("the result of fs_write", out)
	edited := "name = demo\ntoken = " + secret + "\nmode = b\n"
	if got := box.content(t, "app.ini"); got != edited {
		t.Fatalf("file = %q, want the secret kept: %q", got, edited)
	}

	// Replacing or deleting the line, alone or inside a range, is refused.
	after := snapshotOf(t, edited)
	for name, edit := range map[string]map[string]any{
		"replace": replaceLine(after.Lines[1].Anchor, "token = guessed"),
		"delete":  {"op": hashline.OpDelete, "start": after.Lines[1].Anchor},
		"range":   {"op": hashline.OpReplace, "start": after.Lines[0].Anchor, "end": after.Lines[1].Anchor, "content": "x"},
	} {
		_, err := box.run(t, "fs_write", map[string]any{"path": "app.ini", "expected_hash": after.Hash, "edits": []any{edit}})
		if err == nil || !strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "ask the user") {
			t.Errorf("%s of the secret line: error = %v", name, err)
		} else {
			noLeak(name+" error", err.Error())
		}
		if got := box.content(t, "app.ini"); got != edited {
			t.Errorf("%s: file = %q, want it untouched", name, got)
		}
	}
	// So is replacing the whole file, which would lose the value.
	_, err = box.run(t, "fs_write", map[string]any{"path": "app.ini", "expected_hash": after.Hash, "content": "token = x\n"})
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Errorf("whole-file replacement of a file with a secret: error = %v", err)
	}
	if got := box.content(t, "app.ini"); got != edited {
		t.Errorf("file = %q after the refused whole-file write", got)
	}
	// An insert next to the line leaves it as it is.
	if _, err := box.run(t, "fs_write", map[string]any{"path": "app.ini", "expected_hash": after.Hash, "edits": []any{map[string]any{"op": hashline.OpInsertAfter, "start": after.Lines[1].Anchor, "content": "extra = 1"}}}); err != nil {
		t.Fatalf("insert after the secret line: %v", err)
	}
	if got := box.content(t, "app.ini"); got != "name = demo\ntoken = "+secret+"\nextra = 1\nmode = b\n" {
		t.Errorf("file after the insert = %q", got)
	}

	hits, err := box.run(t, "fs_search", map[string]any{"pattern": "token"})
	if err != nil {
		t.Fatal(err)
	}
	noLeak("fs_search", hits)
	if !strings.Contains(hits, "token = "+sandbox.Redacted+hiddenLineMark) {
		t.Errorf("fs_search does not mark the secret line:\n%s", hits)
	}

	// The approval of an edit shows the diff with the value hidden, and a call on the secret line fails before asking.
	ask := newFileToolbox(t, spec.Sandbox{Approval: spec.ApprovalAlways}, secret)
	ask.seed(t, "app.ini", original)
	tl := ask.tools["fs_write"]
	verdict, why := approval(tl, ask.args(t, map[string]any{"path": "app.ini", "expected_hash": view.Hash, "edits": []any{replaceLine(view.Lines[2].Anchor, "mode = b")}}))
	noLeak("the approval", why)
	if verdict != VerdictApprove || !strings.Contains(why, "-mode = a") || !strings.Contains(why, "+mode = b") || !strings.Contains(why, sandbox.Redacted) {
		t.Errorf("approval preview: verdict %d\n%s", verdict, why)
	}
	verdict, why = approval(tl, ask.args(t, map[string]any{"path": "app.ini", "expected_hash": view.Hash, "edits": []any{replaceLine(view.Lines[1].Anchor, "token = x")}}))
	noLeak("the refusal", why)
	if verdict != VerdictDeny || !strings.Contains(why, "secret") {
		t.Errorf("edit of the secret line: verdict %d (%s)", verdict, why)
	}
}

// fs_read returns ranges, never more than the cap in one call, and says where to continue; following those hints reads
// every line once. A binary file shows its revision but no content.
func TestSandboxHashlineReadRangesAndTheSizeCap(t *testing.T) {
	box := newFileToolbox(t, spec.Sandbox{Approval: spec.ApprovalAuto})
	const small = "a\nb\nc\nd\ne\n"
	box.seed(t, "r.txt", small)
	part, err := box.run(t, "fs_read", map[string]any{"path": "r.txt", "start_line": 2, "end_line": 3})
	if err != nil {
		t.Fatal(err)
	}
	window, err := hashline.Snapshot(small, true, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lines 2-3 of 5", window.Lines[0].Anchor + "|b\n", window.Lines[1].Anchor + "|c\n"} {
		if !strings.Contains(part, want) {
			t.Errorf("range read lacks %q:\n%s", want, part)
		}
	}
	if strings.Contains(part, "|a\n") || strings.Contains(part, "|d\n") {
		t.Errorf("range read returns lines outside the range:\n%s", part)
	}
	if _, err := box.run(t, "fs_read", map[string]any{"path": "r.txt", "start_line": 9}); err == nil || !strings.Contains(err.Error(), "past") {
		t.Errorf("a range past the end of the file: %v", err)
	}
	if _, err := box.run(t, "fs_read", map[string]any{"path": "r.txt", "offset": 3}); err == nil || !strings.Contains(err.Error(), "offset") {
		t.Errorf("the removed offset argument was accepted: %v", err)
	}

	line := strings.Repeat("0123456789abcdef", 64) + "\n"
	box.seed(t, "big.txt", strings.Repeat(line, 600))
	header := regexp.MustCompile(`^big\.txt: lines (\d+)-(\d+) of (\d+), revision`)
	covered := 0
	for next := 1; ; {
		out, err := box.run(t, "fs_read", map[string]any{"path": "big.txt", "start_line": next})
		if err != nil {
			t.Fatal(err)
		}
		if len(out) > maxReadBytes+1024 {
			t.Fatalf("one read returned %d bytes, the cap is %d", len(out), maxReadBytes)
		}
		m := header.FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no range in the header: %.200q", out)
		}
		from, _ := strconv.Atoi(m[1])
		to, _ := strconv.Atoi(m[2])
		total, _ := strconv.Atoi(m[3])
		if from != next || to < from || total != 600 {
			t.Fatalf("read of line %d returned lines %d-%d of %d", next, from, to, total)
		}
		covered += to - from + 1
		if to == total {
			break
		}
		if want := "start_line=" + strconv.Itoa(to+1); !strings.Contains(out, want) {
			t.Fatalf("the read stopped at line %d without saying %q", to, want)
		}
		next = to + 1
	}
	if covered != 600 {
		t.Errorf("the reads covered %d lines, want 600", covered)
	}

	const binary = "a\x00b\n"
	box.seed(t, "bin.dat", binary)
	out, err := box.run(t, "fs_read", map[string]any{"path": "bin.dat"})
	if err != nil || !strings.Contains(out, "binary file") || !strings.Contains(out, "revision "+hashline.Hash(binary)) || strings.Contains(out, "a\x00b") {
		t.Errorf("binary read: %q, %v", out, err)
	}
	if _, err := box.run(t, "fs_write", map[string]any{"path": "bin.dat", "expected_hash": hashline.Hash(binary), "edits": []any{map[string]any{"op": hashline.OpDelete, "start": "1:0000"}}}); err == nil {
		t.Error("an edit of a binary file was accepted")
	}
}

// The schema an agent gets for fs_write is the hashline request plus the path, and path and expected_hash are required.
func TestSandboxHashlineWriteSchemaTakesAPathAndTheRequest(t *testing.T) {
	box := newFileToolbox(t, spec.Sandbox{Approval: spec.ApprovalAuto})
	schema := box.tools["fs_write"].InputSchema
	props, _ := schema["properties"].(map[string]any)
	for _, k := range []string{"path", "expected_hash", "content", "edits"} {
		if _, ok := props[k]; !ok {
			t.Errorf("fs_write has no property %q: %v", k, props)
		}
	}
	required := map[string]bool{}
	if r, ok := schema["required"].([]any); ok {
		for _, k := range r {
			required[fmt.Sprint(k)] = true
		}
	}
	if !required["path"] || !required["expected_hash"] {
		t.Errorf("required = %v, want path and expected_hash", schema["required"])
	}
	if schema["type"] != "object" {
		t.Errorf("type = %v", schema["type"])
	}
}

// approvalHook is a fake host that does something while a call waits for its decision.
type approvalHook struct {
	*fakeHost
	before func()
}

func (h *approvalHook) Approve(ctx context.Context, r ApprovalRequest) (Decision, error) {
	if h.before != nil {
		h.before()
	}
	return h.fakeHost.Approve(ctx, r)
}

// Through the hub, with approval always: a read-only run is refused and nobody is asked; a denied call writes nothing; an
// approved call is applied and the user was shown the diff; and when the file changes while the approval is pending, the
// approved call does not write.
func TestSandboxHashlineApprovalThroughTheHub(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "sbx")
	cfg := "enabled: true\nroot: " + root + "\nlifecycle: persistent\napproval: always\nkeep_alive: 0s\n"
	if _, err := spec.WriteFile(home, spec.KindSandbox, "local", []byte(cfg), ""); err != nil {
		t.Fatal(err)
	}
	m := sandbox.NewManager(sandbox.ManagerOptions{Home: home})
	t.Cleanup(m.Close)

	notes := filepath.Join(root, "notes.txt")
	write := func(content string) {
		t.Helper()
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(notes, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(notes)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	const original = "one\ntwo\nthree\n"
	write(original)
	view := snapshotOf(t, original)
	edit := func(v hashline.View, line int, text string) json.RawMessage {
		raw, err := json.Marshal(map[string]any{"path": "notes.txt", "expected_hash": v.Hash, "edits": []any{replaceLine(v.Lines[line].Anchor, text)}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	host := &approvalHook{fakeHost: &fakeHost{decision: Decision{Allowed: true}}}
	h, _ := newTestHub(t, host, t.TempDir())
	h.AddProvider(SandboxProvider(m, SandboxOptions{}))
	open := func(readOnly bool) *Session {
		t.Helper()
		s, err := h.Open(context.Background(), RunSpec{
			Kind: KindFlow, RunID: "run1", Sandbox: "local", ReadOnly: readOnly, ApprovalTimeout: time.Minute,
			Info: func() RunInfo { return RunInfo{AgentRunID: "run1", NodeRunID: "nr1"} },
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		return s
	}
	ctx := context.Background()

	if _, err := open(true).Call(ctx, "fs_write", edit(view, 1, "TWO")); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("a read-only run was not refused: %v", err)
	}
	if len(host.approvals) != 0 || read() != original {
		t.Errorf("read-only run: %d approvals, file %q", len(host.approvals), read())
	}

	s := open(false)
	host.decision = Decision{Allowed: false, Comment: "not now"}
	var denied *DeniedError
	if _, err := s.Call(ctx, "fs_write", edit(view, 1, "TWO")); !errors.As(err, &denied) || read() != original {
		t.Errorf("a denied call: %v, file %q", err, read())
	}

	host.decision = Decision{Allowed: true}
	res, err := s.Call(ctx, "fs_write", edit(view, 1, "TWO"))
	const edited = "one\nTWO\nthree\n"
	if err != nil || !strings.Contains(res.Text, "edited notes.txt") || read() != edited {
		t.Fatalf("an approved call: %q, %v, file %q", res.Text, err, read())
	}
	last := host.approvals[len(host.approvals)-1]
	if last.Tool != "fs_write" || last.Title != "Edit notes.txt" || last.Description != "Edit notes.txt (1 edit) in sandbox local" ||
		!strings.Contains(last.Reason, "\n-two\n") || !strings.Contains(last.Reason, "\n+TWO\n") {
		t.Errorf("the approval request = %+v", last)
	}

	const changed = "one\nTWO (changed elsewhere)\nthree\n"
	host.before = func() { write(changed) }
	_, err = s.Call(ctx, "fs_write", edit(snapshotOf(t, edited), 2, "THREE"))
	var ce *hashline.ConflictError
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "while the approval was pending") || read() != changed {
		t.Errorf("a call approved on a file that changed: %v, file %q", err, read())
	}
}

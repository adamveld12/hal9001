package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"hal9001/internal/hashline"
	"hal9001/internal/sandbox"
)

// The file tools of a sandbox work on hashline views: fs_read shows a file as numbered lines with anchors and the
// revision of the file, fs_write changes it by whole content or by edits that name those anchors and that revision.
// Anchors and the revision come from the stored bytes, so an edit made against them applies to the file as it is. What an
// agent sees of a line is the line with the secret values of the sandbox replaced (RawFile.Redact); a line that holds
// one is marked, and no edit may replace or delete it. The stored bytes of the other lines, secrets included, are
// written back unchanged.

const (
	// maxViewBytes is the largest file that gets anchors in search hits and write results; a larger one is read with
	// fs_read.
	maxViewBytes = 4 << 20
	// maxAnchoredFiles bounds how many files of a search are read to anchor their hits: on a remote sandbox each is a
	// round trip.
	maxAnchoredFiles = 20
	// maxChangedLines bounds the changed lines a write result lists.
	maxChangedLines = 40
	// binaryProbe is how much of the start of a file is searched for a NUL byte, which makes it binary.
	binaryProbe = 8192
	// previewTimeout bounds the read that previews a write for an approval.
	previewTimeout = 20 * time.Second
	// hiddenLineMark ends a displayed line that holds a value hidden from the agent.
	hiddenLineMark = "  [secret: read-only]"
)

func isBinary(b []byte) bool { return bytes.IndexByte(b[:min(len(b), binaryProbe)], 0) >= 0 }

func trimCR(s string) string { return strings.TrimSuffix(s, "\r") }

// plural renders a count with its noun: "1 edit", "2 edits".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// renderLine is one line of a view: its anchor, then its text as the agent may see it.
func renderLine(anchor, text string, secret bool) string {
	if secret {
		return anchor + "|" + text + hiddenLineMark
	}
	return anchor + "|" + text
}

// anchorLine is the line number of an anchor such as "12:a3f1", 0 for anything else.
func anchorLine(a string) int {
	num, _, ok := strings.Cut(a, ":")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// decodeStrict is decode that refuses an argument the tool does not know. fs_read no longer takes an offset: an agent
// that still sends one must hear that, not get the start of the file again.
func decodeStrict(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// ---- fs_read ----

// renderRead renders a file for fs_read: its revision and the lines startLine through endLine (0 leaves a side open) as
// "anchor|text", at most maxReadBytes of them.
func renderRead(path string, f sandbox.RawFile, startLine, endLine int) (string, error) {
	if f.Redact == nil {
		return "", errors.New("the sandbox session cannot hide secret values, so files are not shown")
	}
	text := string(f.Data)
	if isBinary(f.Data) {
		return fmt.Sprintf("%s: binary file, %s, revision %s\nThe content is not shown. Replace the file with fs_write content and this revision as expected_hash, or inspect it with shell.\n",
			path, plural(len(f.Data), "byte"), hashline.Hash(text)), nil
	}
	view, err := hashline.Snapshot(text, true, startLine, endLine)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if view.TotalLines == 0 {
		return fmt.Sprintf("%s: empty file, revision %s\n", path, view.Hash), nil
	}
	if len(view.Lines) == 0 {
		return "", fmt.Errorf("%s has %d lines: start_line %d is past its end", path, view.TotalLines, startLine)
	}
	raw, hidden := strings.Split(text, "\n"), strings.Split(f.Redact(text), "\n")
	if len(raw) != len(hidden) {
		return "", fmt.Errorf("%s: the sandbox cannot say which lines hold secret values, so the file is not shown", path)
	}
	var body strings.Builder
	var shown int
	var secrets bool
	for _, l := range view.Lines {
		if l.Number < 1 || l.Number > len(raw) {
			return "", fmt.Errorf("%s: line %d is out of range", path, l.Number)
		}
		secret := raw[l.Number-1] != hidden[l.Number-1]
		line := renderLine(l.Anchor, trimCR(hidden[l.Number-1]), secret)
		if shown > 0 && body.Len()+len(line)+1 > maxReadBytes {
			break
		}
		if len(line) > maxReadBytes {
			line = strings.ToValidUTF8(line[:maxReadBytes], "") + " […line cut]"
		}
		body.WriteString(line)
		body.WriteByte('\n')
		secrets = secrets || secret
		shown++
	}
	first, last := view.Lines[0].Number, view.Lines[shown-1].Number
	var out strings.Builder
	if first == 1 && last == view.TotalLines {
		fmt.Fprintf(&out, "%s: %s, revision %s", path, plural(view.TotalLines, "line"), view.Hash)
	} else {
		fmt.Fprintf(&out, "%s: lines %d-%d of %d, revision %s", path, first, last, view.TotalLines, view.Hash)
	}
	if !view.HasFinalNewline {
		out.WriteString(", no final newline")
	}
	out.WriteByte('\n')
	if secrets {
		out.WriteString("Lines marked [secret: read-only] hold a secret value that is hidden from you; they cannot be replaced or deleted.\n")
	}
	out.WriteString(body.String())
	if shown < len(view.Lines) {
		fmt.Fprintf(&out, "[stopped after %d bytes: call fs_read again with start_line=%d to continue]\n", maxReadBytes, last+1)
	}
	return out.String(), nil
}

// ---- fs_search ----

// clipHit shortens the text of a search hit.
func clipHit(text string) string {
	if len(text) > 300 {
		return strings.ToValidUTF8(text[:300], "") + "…"
	}
	return text
}

// renderHits lists the hits of a search. The files of the first hits (maxAnchoredFiles files, each small enough) are
// read to anchor them: their hits come as "anchor|text" under the file's path and revision, so a hit can be edited
// without reading the file again. The other hits follow as path:line:text.
func (s *sandboxTools) renderHits(ctx context.Context, hits []sandbox.Match) string {
	type group struct {
		path string
		hits []sandbox.Match
	}
	var groups []*group
	byPath := map[string]*group{}
	for _, h := range hits {
		g := byPath[h.Path]
		if g == nil {
			g = &group{path: h.Path}
			byPath[h.Path] = g
			groups = append(groups, g)
		}
		g.hits = append(g.hits, h)
	}
	// Reading a file to anchor its hits is no operation of the agent.
	hiddenCtx := sandbox.WithCall(ctx, sandbox.CallInfo{Hidden: true})
	var anchored, plain strings.Builder
	for i, g := range groups {
		if i < maxAnchoredFiles && s.anchorHits(hiddenCtx, g.path, g.hits, &anchored) {
			continue
		}
		for _, h := range g.hits {
			fmt.Fprintf(&plain, "%s:%d:%s\n", h.Path, h.Line, clipHit(h.Text))
		}
	}
	if plain.Len() == 0 {
		return anchored.String()
	}
	if anchored.Len() == 0 {
		return plain.String()
	}
	return anchored.String() + "\nNo anchors for these hits (read the file with fs_read to change it):\n" + plain.String()
}

// anchorHits appends the hits of one file to out, anchored, and reports whether it did. It does nothing when the file
// cannot be read, is binary or large, or has changed since the search.
func (s *sandboxTools) anchorHits(ctx context.Context, path string, hits []sandbox.Match, out *strings.Builder) bool {
	f, err := s.sess.ReadFileRaw(ctx, path)
	if err != nil || f.Redact == nil || len(f.Data) > maxViewBytes || isBinary(f.Data) {
		return false
	}
	text := string(f.Data)
	view, err := hashline.Snapshot(text, true, 0, 0)
	if err != nil {
		return false
	}
	raw, hidden := strings.Split(text, "\n"), strings.Split(f.Redact(text), "\n")
	if len(raw) != len(hidden) {
		return false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (revision %s)\n", path, view.Hash)
	for _, h := range hits {
		if h.Line < 1 || h.Line > len(view.Lines) || h.Line > len(raw) || view.Lines[h.Line-1].Number != h.Line {
			return false
		}
		b.WriteString(renderLine(view.Lines[h.Line-1].Anchor, clipHit(trimCR(hidden[h.Line-1])), raw[h.Line-1] != hidden[h.Line-1]))
		b.WriteByte('\n')
	}
	if out.Len() > 0 {
		out.WriteByte('\n')
	}
	out.WriteString(b.String())
	return true
}

// ---- fs_write ----

// writeSchema is the input schema of fs_write: the hashline request plus the path of the file.
func writeSchema() map[string]any {
	schema := map[string]any{}
	if b, err := json.Marshal(hashline.RequestSchema()); err == nil {
		_ = json.Unmarshal(b, &schema)
	}
	if _, ok := schema["type"]; !ok {
		schema["type"] = "object"
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	props["path"] = str("File path relative to the workdir.")
	schema["properties"] = props
	required := []any{"path"}
	if r, ok := schema["required"].([]any); ok {
		required = append(required, r...)
	}
	schema["required"] = required
	return schema
}

// writeRequest is a decoded fs_write call: a hashline request for one file.
type writeRequest struct {
	path string
	req  hashline.Request
}

// parseWrite decodes the arguments of fs_write. A call without expected_hash, which is what the former fs_write took
// ({path, content}), is refused: a call must say whether it creates the file or changes the revision that fs_read showed.
func parseWrite(raw json.RawMessage) (writeRequest, error) {
	var fields map[string]json.RawMessage
	if err := decode(raw, &fields); err != nil {
		return writeRequest{}, err
	}
	var w writeRequest
	if p, ok := fields["path"]; ok {
		if err := json.Unmarshal(p, &w.path); err != nil {
			return writeRequest{}, fmt.Errorf("invalid arguments: path: %w", err)
		}
	}
	if strings.TrimSpace(w.path) == "" {
		return writeRequest{}, errors.New("path is required")
	}
	if _, ok := fields["expected_hash"]; !ok {
		return writeRequest{}, errors.New(`expected_hash is required: send "" to create a file that does not exist yet, or the revision fs_read showed for the file you change`)
	}
	if err := decode(raw, &w.req); err != nil {
		return writeRequest{}, err
	}
	return w, nil
}

// detail says in a few words what the call does to the file, for the sandbox log.
func (w writeRequest) detail() string {
	switch {
	case w.req.Content == nil:
		return plural(len(w.req.Edits), "edit")
	case w.req.ExpectedHash == "":
		return "new file"
	}
	return "whole file"
}

// label names what the call does to a file that exists or not: "create a.txt", "replace a.txt", "edit a.txt (2 edits)".
func (w writeRequest) label(exists bool) string {
	switch {
	case !exists:
		return "create " + w.path
	case w.req.Content != nil:
		return "replace " + w.path
	}
	return fmt.Sprintf("edit %s (%s)", w.path, plural(len(w.req.Edits), "edit"))
}

// writePlan is what an fs_write call makes of a file whose stored content is known.
type writePlan struct {
	exists           bool
	oldText, newText string
	// redact hides the secret values of the sandbox in a text and keeps its lines.
	redact func(string) string
}

// same reports whether the call leaves the file as it is.
func (p writePlan) same() bool { return p.exists && p.oldText == p.newText }

// planWrite applies the request to the stored content. hashline checks the revision and the anchors, and the call is
// refused when it would change a line that holds a secret value: the agent saw only a replacement for the value, so it
// cannot have meant to change that line, and writing its version would destroy the value.
func planWrite(w writeRequest, old sandbox.RawFile) (writePlan, error) {
	p := writePlan{exists: old.Exists, oldText: string(old.Data), redact: old.Redact}
	if old.Redact == nil {
		return p, errors.New("the sandbox session cannot hide secret values, so files are not changed")
	}
	if old.Exists && w.req.Content == nil && isBinary(old.Data) {
		return p, fmt.Errorf("%s is a binary file, which edits cannot change: send the whole new file as content, or use shell. Nothing was written", w.path)
	}
	updated, err := hashline.Apply(p.oldText, old.Exists, w.req)
	if err != nil {
		return p, fmt.Errorf("%s: %w. Nothing was written", w.path, err)
	}
	if old.Exists {
		if err := refuseSecrets(w, p); err != nil {
			return p, err
		}
	}
	p.newText = updated
	return p, nil
}

// refuseSecrets fails when the request would replace or delete a line of the old text that holds a secret value, or
// replace the whole file while any line does. Inserts next to such a line leave it as it is.
func refuseSecrets(w writeRequest, p writePlan) error {
	hiddenText := p.redact(p.oldText)
	if hiddenText == p.oldText {
		return nil
	}
	raw, hidden := strings.Split(p.oldText, "\n"), strings.Split(hiddenText, "\n")
	if len(raw) != len(hidden) {
		return fmt.Errorf("%s holds secret values and the sandbox cannot say on which lines, so it is not changed: ask the user. Nothing was written", w.path)
	}
	secret := func(n int) bool { return n >= 1 && n <= len(raw) && raw[n-1] != hidden[n-1] }
	if w.req.Content != nil {
		first := 1
		for first <= len(raw) && !secret(first) {
			first++
		}
		return fmt.Errorf("line %d of %s holds a secret value that is hidden from you, so the file cannot be replaced as a whole: change other lines with edits, or ask the user. Nothing was written", first, w.path)
	}
	for _, e := range w.req.Edits {
		if e.Op != hashline.OpReplace && e.Op != hashline.OpDelete {
			continue
		}
		from := anchorLine(e.Start)
		to := from
		if e.End != "" {
			to = anchorLine(e.End)
		}
		if from < 1 || to < from {
			return fmt.Errorf("%s holds secret values and the sandbox cannot tell which lines an edit covers, so it is not changed: ask the user. Nothing was written", w.path)
		}
		for n := from; n <= to; n++ {
			if secret(n) {
				return fmt.Errorf("line %d of %s holds a secret value that is hidden from you, so it cannot be replaced or deleted: change other lines, or ask the user to change this one. Nothing was written", n, w.path)
			}
		}
	}
	return nil
}

// previewWrite computes what the call would do, without writing. It goes through EditFile, so the path checks and the
// limits are those of the write.
func (s *sandboxTools) previewWrite(w writeRequest) (writePlan, error) {
	// Reading the current content for the preview is not an operation of the agent.
	ctx, cancel := context.WithTimeout(sandbox.WithCall(context.Background(), sandbox.CallInfo{Hidden: true}), previewTimeout)
	defer cancel()
	var plan writePlan
	err := s.sess.EditFile(ctx, w.path, func(old sandbox.RawFile) ([]byte, error) {
		var perr error
		if plan, perr = planWrite(w, old); perr != nil {
			return nil, perr
		}
		return nil, sandbox.ErrNoChange
	})
	return plan, err
}

// writePolicy decides fs_write by the sandbox policy and, when the user must approve, previews the call: it reads the
// stored content, applies the request and puts the diff of the real result in the approval. A call that cannot be applied
// (a stale revision, a bad anchor, a path that is not writable) fails here instead of being put to the user. The write
// repeats the checks against the file as it is then, so a file that changed while the approval was pending is not written.
func (s *sandboxTools) writePolicy(raw json.RawMessage) (Verdict, string) {
	w, err := parseWrite(raw)
	if err != nil {
		return VerdictAllow, "" // the call is refused when it runs
	}
	d := s.policy.Decide(sandbox.OpFSWrite, map[string]any{"path": w.path})
	switch d.Action {
	case "deny":
		return VerdictDeny, fmt.Sprintf("fs_write to %s is denied by the %s sandbox policy: %s", w.path, s.name, d.Reason)
	case "approve":
	default:
		return VerdictAllow, d.Reason
	}
	plan, err := s.previewWrite(w)
	if err != nil {
		return VerdictDeny, fmt.Sprintf("the user was not asked, because the change cannot be made: %s", err)
	}
	if plan.same() {
		return VerdictAllow, "the change leaves the file as it is"
	}
	diff := UnifiedDiff(w.path, plan.redact(plan.oldText), plan.redact(plan.newText))
	if len(diff) > diffSnippetLimit {
		diff = strings.ToValidUTF8(diff[:diffSnippetLimit], "") + "\n[diff truncated]"
	}
	return VerdictApprove, fmt.Sprintf("sandbox %s: %s (%s)\n\n%s", s.name, w.label(plan.exists), d.Reason, diff)
}

// write applies the call. The revision and the anchors are checked against the file as it is when it is replaced.
func (s *sandboxTools) write(ctx context.Context, c Call) (Result, error) {
	w, err := parseWrite(c.Args)
	if err != nil {
		return Result{}, err
	}
	ctx = sandbox.WithCall(ctx, sandbox.CallInfo{Tool: c.Name, Approval: c.Approval, Detail: w.detail()})
	var plan writePlan
	var wrote, refused bool
	err = s.sess.EditFile(ctx, w.path, func(old sandbox.RawFile) ([]byte, error) {
		wrote, refused = false, false
		p, perr := planWrite(w, old)
		if perr != nil {
			refused = true
			return nil, perr
		}
		plan = p
		if p.same() {
			return nil, sandbox.ErrNoChange
		}
		wrote = true
		return []byte(p.newText), nil
	})
	if err != nil {
		if refused && strings.HasPrefix(c.Approval, "approved by user") {
			err = fmt.Errorf("%w (the user approved the change, but the file may have changed while the approval was pending)", err)
		}
		return Result{}, err
	}
	return Result{Text: writeResult(w, plan, wrote)}, nil
}

// writeResult is what fs_write returns: what was done, the new revision, and the changed lines with their anchors, so
// the agent can go on editing without reading the file again.
func writeResult(w writeRequest, p writePlan, wrote bool) string {
	lines := splitLines(p.newText)
	var b strings.Builder
	if !wrote {
		fmt.Fprintf(&b, "unchanged %s: the change leaves the file as it is (%s)\nrevision: %s\n", w.path, plural(len(lines), "line"), hashline.Hash(p.newText))
		return b.String()
	}
	verb, edits := "edited", plural(len(w.req.Edits), "edit")+", "
	switch {
	case !p.exists:
		verb, edits = "created", ""
	case w.req.Content != nil:
		verb, edits = "replaced", ""
	}
	fmt.Fprintf(&b, "%s %s: %s%s, %s\nrevision: %s\n", verb, w.path, edits, plural(len(p.newText), "byte"), plural(len(lines), "line"), hashline.Hash(p.newText))
	ops, ok := diffLines(splitLines(p.oldText), lines)
	if !ok {
		b.WriteString("Too many lines changed to list them: read the file with fs_read for its anchors.\n")
		return b.String()
	}
	added, removed := changedLines(ops)
	fmt.Fprintf(&b, "changed: +%d -%d lines\n", len(added), removed)
	if len(added) == 0 {
		return b.String()
	}
	if len(p.newText) > maxViewBytes {
		b.WriteString("The file is too large to list the anchors of the changed lines: read the part you need with fs_read.\n")
		return b.String()
	}
	view, err := hashline.Snapshot(p.newText, true, 0, 0)
	raw, hidden := strings.Split(p.newText, "\n"), strings.Split(p.redact(p.newText), "\n")
	if err != nil || len(raw) != len(hidden) {
		return b.String()
	}
	for i, n := range added {
		if i == maxChangedLines {
			fmt.Fprintf(&b, "… %s not listed\n", plural(len(added)-i, "more changed line"))
			break
		}
		if n > len(view.Lines) || n > len(raw) {
			break
		}
		b.WriteString(renderLine(view.Lines[n-1].Anchor, trimCR(hidden[n-1]), raw[n-1] != hidden[n-1]))
		b.WriteByte('\n')
	}
	return b.String()
}

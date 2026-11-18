package assist

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"hal9001/internal/core"
	"hal9001/internal/hashline"
	"hal9001/internal/spec"
)

// The files of a session's draft that the agent (draft_read, draft_write) and the API (GET and PATCH
// /api/assist/sessions/{id}/draft) address by path. One adapter maps a draft path to its storage, so a read and an
// edit see the same bytes and the same revision whichever file it is:
//
//   - the draft itself (sensor.yaml, flow.yaml or tool.yaml): the row's DraftYAML and the draft file;
//   - prompts/<name>.md of a flow session: a file of the draft directory;
//   - sensors/<id>.yaml of a flow session: a file of the draft directory. A saved sensor without a draft reads as the
//     saved file (its raw bytes and hash); the first edit copies it into the draft and records that hash as the base
//     Save compares the saved file with.
//
// Every file has its own revision, the hash of its bytes: an edit of a prompt or a sensor never moves the draft's hash.

// Bounds of an edit result: the lines it lists (see affectedLines), and the size of the comparison that finds them.
const (
	maxAffectedLines = 40
	maxAffectedBytes = 8 << 10
	maxDiffCells     = 4 << 20
)

type draftTargetKind int

const (
	draftIsPrimary draftTargetKind = iota
	draftIsPrompt
	draftIsSensor
)

// draftTarget is one file of a session's draft.
type draftTarget struct {
	kind draftTargetKind
	// path is the draft path: the draft file's name, prompts/<name>.md or sensors/<id>.yaml.
	path string
	// name is the prompt name or the sensor id.
	name string
}

// byteLimit is the size a file of this kind may have, and what to call it in the error.
func (t draftTarget) byteLimit() (int, string) {
	switch t.kind {
	case draftIsPrompt:
		return maxPromptBytes, "the prompt"
	case draftIsSensor:
		return maxDraftSensorBytes, "the sensor"
	}
	return maxDraftBytes, "the draft"
}

// draftTargetOf maps a draft path to its file. ok is false when p names no file of this session's draft (the caller
// says what it accepts); err explains a name that is not valid.
func (s *session) draftTargetOf(p string) (t draftTarget, ok bool, err error) {
	switch {
	case p == "" || p == s.draftFile():
		return draftTarget{kind: draftIsPrimary, path: s.draftFile()}, true, nil
	case s.isFlow() && strings.HasPrefix(p, PromptsDir+"/") && strings.HasSuffix(p, ".md"):
		name := strings.TrimSuffix(strings.TrimPrefix(p, PromptsDir+"/"), ".md")
		if !spec.ValidID(name) {
			return t, true, invalid("path", fmt.Sprintf("prompt name %q is invalid (letters, digits, - and _, at most 64 characters)", name))
		}
		return draftTarget{kind: draftIsPrompt, path: PromptsDir + "/" + name + ".md", name: name}, true, nil
	case s.isFlow() && strings.HasPrefix(p, SensorsDir+"/") && strings.HasSuffix(p, draftSensorSuffix):
		id := strings.TrimSuffix(strings.TrimPrefix(p, SensorsDir+"/"), draftSensorSuffix)
		if !spec.ValidID(id) {
			return t, true, invalid("path", fmt.Sprintf("sensor name %q is invalid (letters, digits, - and _, at most 64 characters)", id))
		}
		return draftTarget{kind: draftIsSensor, path: SensorsDir + "/" + id + draftSensorSuffix, name: id}, true, nil
	}
	return t, false, nil
}

// draftPathsHelp says which draft paths this session accepts.
func (s *session) draftPathsHelp() string {
	if s.isFlow() {
		return "no path (" + s.draftFile() + "), " + PromptsDir + "/<name>.md or " + SensorsDir + "/<id>.yaml"
	}
	return "no path (" + s.draftFile() + ")"
}

// resolveDraft maps a path of a request to its file, or says which paths the session has.
func (s *session) resolveDraft(path string) (draftTarget, error) {
	t, ok, err := s.draftTargetOf(strings.TrimSpace(path))
	switch {
	case err != nil:
		return t, err
	case !ok:
		return t, invalid("path", fmt.Sprintf("the session has no draft file %q: use %s", path, s.draftPathsHelp()))
	}
	return t, nil
}

// draftUsableLocked is usableLocked for what needs a draft: a general session has none. s.mu must be held.
func (s *session) draftUsableLocked() error {
	if err := s.usableLocked(); err != nil {
		return err
	}
	if s.isGeneral() {
		return errNoDraft
	}
	return nil
}

// draftSource is the stored state of one draft file.
type draftSource struct {
	content string
	exists  bool
	// saved marks a sensor that has no draft yet: content is the saved file, which the first edit copies into the draft.
	saved bool
	// base is the hash of that saved file.
	base string
}

// draftSourceLocked reads the raw bytes of a draft file. s.mu must be held.
func (s *session) draftSourceLocked(t draftTarget) (draftSource, error) {
	switch t.kind {
	case draftIsPrimary:
		return draftSource{content: s.row.DraftYAML, exists: true}, nil
	case draftIsPrompt:
		b, err := os.ReadFile(filepath.Join(s.dir, PromptsDir, t.name+".md")) //nolint:gosec // the name is a valid id inside the session's draft directory
		return draftFileSource(b, err)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, SensorsDir, t.name+draftSensorSuffix)) //nolint:gosec // the name is a valid id inside the session's draft directory
	if errors.Is(err, fs.ErrNotExist) {
		if saved := spec.Load(s.m.home).Sensor(t.name); saved != nil {
			return draftSource{content: string(saved.Raw), exists: true, saved: true, base: saved.Hash}, nil
		}
		return draftSource{}, nil
	}
	return draftFileSource(b, err)
}

func draftFileSource(b []byte, err error) (draftSource, error) {
	switch {
	case err == nil:
		return draftSource{content: string(b), exists: true}, nil
	case errors.Is(err, fs.ErrNotExist):
		return draftSource{}, nil
	}
	return draftSource{}, fmt.Errorf("assist: read draft file: %w", err)
}

// diagnosticsJSON returns raw as a JSON array, "[]" when it is empty.
func diagnosticsJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return raw
}

// readDraft is ReadDraft of the manager: the anchored lines of one file of the draft.
func (s *session) readDraft(path string, start, end int) (core.AssistDraftFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.draftUsableLocked(); err != nil {
		return core.AssistDraftFile{}, err
	}
	t, err := s.resolveDraft(path)
	if err != nil {
		return core.AssistDraftFile{}, err
	}
	return s.readDraftLocked(t, start, end)
}

// readDraftLocked returns the lines of file t from start to end (both 0: every line) with their anchors. s.mu must be held.
func (s *session) readDraftLocked(t draftTarget, start, end int) (core.AssistDraftFile, error) {
	var zero core.AssistDraftFile
	src, err := s.draftSourceLocked(t)
	if err != nil {
		return zero, err
	}
	if !src.exists {
		start, end = 0, 0 // a file that is not there has no lines to select
	}
	view, err := hashline.Snapshot(src.content, src.exists, start, end)
	if err != nil {
		return zero, invalid("start", err.Error())
	}
	lines := view.Lines
	if lines == nil {
		lines = []hashline.Line{}
	}
	return core.AssistDraftFile{
		Path: t.path, Exists: view.Exists, Saved: src.saved, Hash: view.Hash, Lines: lines,
		TotalLines: view.TotalLines, HasFinalNewline: view.HasFinalNewline, Diagnostics: diagnosticsJSON(s.row.DraftDiagnostics),
	}, nil
}

// editDraft is EditDraft of the manager: it applies req to one file of the draft as by (store.DraftByUser or DraftByAgent).
func (s *session) editDraft(path string, req hashline.Request, by string) (core.AssistDraftEdit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.draftUsableLocked(); err != nil {
		return core.AssistDraftEdit{}, err
	}
	t, err := s.resolveDraft(path)
	if err != nil {
		return core.AssistDraftEdit{}, err
	}
	return s.editDraftLocked(t, req, by)
}

// editDraftLocked applies req to the raw bytes of file t and, when they change, stores the result the way every draft
// change is stored: the file, the row (hash, diagnostics, updated_by) and with it the assist.updated event. s.mu
// must be held.
func (s *session) editDraftLocked(t draftTarget, req hashline.Request, by string) (core.AssistDraftEdit, error) {
	var zero core.AssistDraftEdit
	src, err := s.draftSourceLocked(t)
	if err != nil {
		return zero, err
	}
	next, err := hashline.Apply(src.content, src.exists, req)
	if err != nil {
		return zero, draftEditError(t, req, err)
	}
	if limit, what := t.byteLimit(); len(next) > limit {
		return zero, invalid(requestField(req), what+" is too large")
	}
	changed := !src.exists || next != src.content
	if changed {
		if err := s.storeDraftLocked(t, next, src, by); err != nil {
			return zero, err
		}
	}
	prev, err := hashline.Snapshot(src.content, src.exists, 0, 0)
	if err != nil {
		return zero, fmt.Errorf("assist: snapshot of %s: %w", t.path, err)
	}
	cur, err := hashline.Snapshot(next, true, 0, 0)
	if err != nil {
		return zero, fmt.Errorf("assist: snapshot of %s: %w", t.path, err)
	}
	out := core.AssistDraftEdit{
		Path: t.path, Hash: cur.Hash, TotalLines: cur.TotalLines, Changed: changed, Affected: []hashline.Line{},
		Diagnostics: diagnosticsJSON(s.row.DraftDiagnostics),
	}
	if changed {
		out.Affected, out.Truncated = affectedLines(prev, cur)
	}
	return out, nil
}

// storeDraftLocked writes content as the new bytes of file t. src is what editDraftLocked applied the edit to. s.mu must be held.
func (s *session) storeDraftLocked(t draftTarget, content string, src draftSource, by string) error {
	switch t.kind {
	case draftIsPrimary:
		return s.setDraftLocked(content, by)
	case draftIsPrompt:
		dir := filepath.Join(s.dir, PromptsDir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("assist: create prompts directory: %w", err)
		}
		if err := writeAtomic(filepath.Join(dir, t.name+".md"), []byte(content)); err != nil {
			return err
		}
	default:
		if err := s.writeSensorFileLocked(t.name, content, src); err != nil {
			return err
		}
	}
	s.applyDraft(s.row.DraftYAML, by)
	return s.saveLocked()
}

// writeSensorFileLocked stores the draft of sensor id. The first write of a saved sensor copies it in, and the hash of
// the saved file the edit started from becomes the base of the copy. s.mu must be held.
func (s *session) writeSensorFileLocked(id, content string, src draftSource) error {
	dir := filepath.Join(s.dir, SensorsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("assist: create sensors directory: %w", err)
	}
	if src.saved || !src.exists {
		if len(readDraftSensors(s.dir)) >= maxDraftSensors {
			return invalid("path", fmt.Sprintf("a flow session drafts at most %d sensors", maxDraftSensors))
		}
	}
	if src.saved {
		bases := sensorBases(s.dir)
		bases[id] = src.base
		if err := writeSensorBases(s.dir, bases); err != nil {
			return err
		}
	}
	return writeAtomic(filepath.Join(dir, id+draftSensorSuffix), []byte(content))
}

// requestField names the part of an edit request that carries the new text, for the diagnostics of a 422.
func requestField(req hashline.Request) string {
	if req.Content != nil {
		return "content"
	}
	return "edits"
}

// draftEditError maps what hashline.Apply reports to the errors of core.Assist: a hash that is not the file's is a
// conflict, anything else is an edit that does not apply.
func draftEditError(t draftTarget, req hashline.Request, err error) error {
	var conflict *hashline.ConflictError
	if errors.As(err, &conflict) {
		return &core.DraftConflictError{Path: t.path, Expected: conflict.Expected, Current: conflict.Current}
	}
	return invalid(requestField(req), err.Error())
}

// affectedLines lists the lines of cur that differ from prev, with their new anchors: the lines that are new or changed
// and, where lines were only removed (no new line stands at the gap), the line before the gap and the line after it. It
// lists at most maxAffectedLines lines and maxAffectedBytes bytes of text; truncated reports that more lines changed.
func affectedLines(prev, cur hashline.View) (lines []hashline.Line, truncated bool) {
	lines = []hashline.Line{}
	a, b := prev.Lines, cur.Lines
	head := 0
	for head < len(a) && head < len(b) && a[head].Text == b[head].Text {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail].Text == b[len(b)-1-tail].Text {
		tail++
	}
	midA, midB := a[head:len(a)-tail], b[head:len(b)-tail]
	eof := prev.HasFinalNewline != cur.HasFinalNewline && len(b) > 0 // the last line gained or lost its newline
	if len(midA) == 0 && len(midB) == 0 && !eof {
		return lines, false // nothing changed
	}
	want := make([]bool, len(b))
	added, removed := diffRuns(midA, midB)
	for j, isAdded := range added {
		want[head+j] = isAdded
	}
	for j, gap := range removed {
		if !gap || (j < len(added) && added[j]) || (j > 0 && added[j-1]) {
			continue // no gap here, or a new line that is listed anyway stands next to it
		}
		if i := head + j - 1; i >= 0 && i < len(b) {
			want[i] = true
		}
		if i := head + j; i < len(b) {
			want[i] = true
		}
	}
	if eof {
		want[len(b)-1] = true
	}
	size := 0
	for i, l := range b {
		if !want[i] {
			continue
		}
		if len(lines) >= maxAffectedLines || (len(lines) > 0 && size+len(l.Text) > maxAffectedBytes) {
			return lines, true
		}
		lines = append(lines, l)
		size += len(l.Text)
	}
	return lines, false
}

// diffRuns compares two runs of lines that have no first and no last line in common. added[j] is true for a line of b
// that is not part of a longest common subsequence of a and b: it is new or changed. removed[j] is true when lines of
// a that are not part of it lie just before b[j] (j == len(b): after the last line). Runs too long to compare count as
// replaced as a whole.
func diffRuns(a, b []hashline.Line) (added, removed []bool) {
	n, m := len(a), len(b)
	added, removed = make([]bool, m), make([]bool, m+1)
	if n > 0 && m > 0 && n*m > maxDiffCells {
		removed[0] = true
		for j := range added {
			added[j] = true
		}
		return added, removed
	}
	// common[i*(m+1)+j] is the length of the longest common subsequence of a[i:] and b[j:]; it never exceeds the
	// shorter run, which maxDiffCells keeps far below 1<<16.
	w := m + 1
	common := make([]uint16, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i].Text == b[j].Text {
				common[i*w+j] = common[(i+1)*w+j+1] + 1
			} else {
				common[i*w+j] = max(common[(i+1)*w+j], common[i*w+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i].Text == b[j].Text:
			i++
			j++
		case common[(i+1)*w+j] >= common[i*w+j+1]:
			removed[j] = true
			i++
		default:
			added[j] = true
			j++
		}
	}
	for ; j < m; j++ {
		added[j] = true
	}
	if i < n {
		removed[m] = true
	}
	return added, removed
}

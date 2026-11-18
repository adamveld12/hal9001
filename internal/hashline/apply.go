package hashline

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Apply returns the content a file gets from req, or an error and no content. content is the file's current source and
// exists tells whether the file exists; content is ignored for a missing file.
//
// Everything is checked before anything is produced. First what does not depend on the file: exactly one of Content and
// a non-empty Edits is set, and every edit has a known op, content where it needs some, anchors of the form line:hash
// and an End that is not before its Start.
//
// Then the version: ExpectedHash must equal the Hash of the source, exactly. An empty ExpectedHash only creates, so it
// fails for an existing file, an empty one included, and any other ExpectedHash fails for a missing file. A missing file
// can only be created with Content; edits need something to anchor on. A failed version check is a [*ConflictError],
// whatever the ExpectedHash looks like.
//
// Content is then returned exactly as given. Edits are checked against the source as it is: each anchor names a line
// that exists and whose hash is the anchor's, with no relocation; the source's line endings are all LF or all CRLF; and
// no two edits overlap or insert at the same place. Replace and delete cover the lines Start through End, an insert goes
// before or after the line Start. Edits may touch neighboring lines, and an insert may sit right before or after a
// replaced or deleted range, but not inside it. The result is assembled in one pass.
//
// Any error other than a ConflictError is a defect of the request or of the source that repeating the request cannot
// fix. The messages are meant for the author of the request: they name the edit and the problem and never quote the
// source.
func Apply(content string, exists bool, req Request) (string, error) {
	edits, err := parse(req)
	if err != nil {
		return "", err
	}
	if !exists {
		switch {
		case req.ExpectedHash != "":
			return "", &ConflictError{Expected: req.ExpectedHash}
		case req.Content == nil:
			return "", errors.New("the file does not exist, so there is nothing to edit: create it by sending content")
		}
		return *req.Content, nil
	}
	if cur := Hash(content); req.ExpectedHash != cur {
		return "", &ConflictError{Expected: req.ExpectedHash, Current: cur}
	}
	if req.Content != nil {
		return *req.Content, nil
	}
	return applyEdits(content, edits)
}

// anchor is a parsed "line:hash".
type anchor struct {
	line int
	hash uint32
}

// loc is where a line sits in the source: its text is src[start:end] and its terminator src[end:next].
type loc struct {
	start, end, next int
}

// edit is an Edit after parsing. It removes the source lines from+1 through to; an insertion has from == to, the number
// of the line it comes after. first and last are where the Start and End lines sit in the source, set once the anchors
// are checked.
type edit struct {
	index   int
	op      string
	start   anchor
	end     anchor
	content *string
	from    int
	to      int
	first   loc
	last    loc
}

// parse checks what does not depend on the file and parses the edits (none in content mode).
func parse(req Request) ([]edit, error) {
	switch {
	case req.Content != nil && len(req.Edits) > 0:
		return nil, errors.New("send either content or edits, not both")
	case req.Content == nil && len(req.Edits) == 0:
		return nil, errors.New("send content (the whole file) or a non-empty list of edits")
	case req.Content != nil:
		return nil, nil
	}
	edits := make([]edit, len(req.Edits))
	for i, e := range req.Edits {
		pe, err := parseEdit(i, e)
		if err != nil {
			return nil, err
		}
		edits[i] = pe
	}
	return edits, nil
}

func parseEdit(i int, e Edit) (edit, error) {
	insert := e.Op == OpInsertBefore || e.Op == OpInsertAfter
	switch {
	case !insert && e.Op != OpReplace && e.Op != OpDelete:
		return edit{}, fmt.Errorf("edits[%d]: unknown op %q: use replace, delete, insert_before or insert_after", i, e.Op)
	case e.Op == OpDelete && e.Content != nil:
		return edit{}, fmt.Errorf("edits[%d]: delete takes no content", i)
	case e.Op != OpDelete && e.Content == nil:
		return edit{}, fmt.Errorf("edits[%d]: %s needs content (an empty string is one blank line)", i, e.Op)
	case insert && e.End != "":
		return edit{}, fmt.Errorf("edits[%d]: %s takes a start anchor only, no end", i, e.Op)
	case e.Content != nil && loneCR(*e.Content):
		return edit{}, fmt.Errorf("edits[%d]: content has a carriage return that is not part of a CRLF line break", i)
	}
	start, err := parseAnchor(e.Start)
	if err != nil {
		return edit{}, fmt.Errorf("edits[%d].start: %w", i, err)
	}
	end := start
	if e.End != "" {
		if end, err = parseAnchor(e.End); err != nil {
			return edit{}, fmt.Errorf("edits[%d].end: %w", i, err)
		}
		if end.line < start.line {
			return edit{}, fmt.Errorf("edits[%d]: end %s is before start %s", i, e.End, e.Start)
		}
	}
	pe := edit{index: i, op: e.Op, start: start, end: end, content: e.Content}
	switch e.Op {
	case OpInsertBefore:
		pe.from, pe.to = start.line-1, start.line-1
	case OpInsertAfter:
		pe.from, pe.to = start.line, start.line
	default:
		pe.from, pe.to = start.line-1, end.line
	}
	return pe, nil
}

// parseAnchor reads "line:hash": a line number from 1 without sign or leading zeros, a colon and exactly four
// lowercase hex digits.
func parseAnchor(s string) (anchor, error) {
	i := strings.IndexByte(s, ':')
	if i < 1 || len(s) != i+5 || s[0] == '0' {
		return anchor{}, badAnchor(s)
	}
	for j := range i {
		if s[j] < '0' || s[j] > '9' {
			return anchor{}, badAnchor(s)
		}
	}
	line, err := strconv.Atoi(s[:i])
	if err != nil {
		return anchor{}, badAnchor(s)
	}
	var hash uint32
	for j := i + 1; j < len(s); j++ {
		d, ok := hexVal(s[j])
		if !ok {
			return anchor{}, badAnchor(s)
		}
		hash = hash<<4 | d
	}
	return anchor{line: line, hash: hash}, nil
}

func badAnchor(s string) error {
	return fmt.Errorf("%q is not an anchor: use line:hash as the read shows it (a line number from 1, a colon, four lowercase hex digits, like 12:a3f1)", s)
}

// hexVal is the value of a lowercase hex digit.
func hexVal(c byte) (uint32, bool) {
	switch {
	case c >= '0' && c <= '9':
		return uint32(c - '0'), true
	case c >= 'a' && c <= 'f':
		return uint32(c-'a') + 10, true
	}
	return 0, false
}

// loneCR reports whether s has a carriage return that does not start a CRLF.
func loneCR(s string) bool {
	for i := 0; ; {
		j := strings.IndexByte(s[i:], '\r')
		if j < 0 {
			return false
		}
		i += j + 1
		if i >= len(s) || s[i] != '\n' {
			return true
		}
	}
}

// layout is what one pass over the source finds: the number of lines, the line terminator all of them share ("\n" when
// none has one), whether the last line has a terminator, and where the lines named by anchors sit. named lists their
// numbers, ascending and without repeats, and locs[i] is where line named[i] sits.
type layout struct {
	lines int
	nl    string
	final bool
	named []int
	locs  []loc
}

// scan reads src once. It fails when the line endings are not uniform or the source ends in a lone CR, because edits
// could not keep either as it is.
func scan(src string, named []int) (layout, error) {
	lay := layout{nl: "\n", final: strings.HasSuffix(src, "\n"), named: named, locs: make([]loc, len(named))}
	var lf, crlf int
	for pos, w := 0, 0; pos < len(src); {
		end, next := lineAt(src, pos)
		lay.lines++
		switch next - end {
		case 1:
			lf++
		case 2:
			crlf++
		}
		if w < len(named) && named[w] == lay.lines {
			lay.locs[w] = loc{start: pos, end: end, next: next}
			w++
		}
		pos = next
	}
	switch {
	case lf > 0 && crlf > 0:
		return layout{}, fmt.Errorf("the file mixes LF and CRLF line endings (%d lines end with LF, %d with CRLF), which edits cannot keep; send the whole file as content", lf, crlf)
	case strings.HasSuffix(src, "\r"):
		return layout{}, errors.New("the file ends with a carriage return that is not part of a line break, which edits cannot keep; send the whole file as content")
	case crlf > 0:
		lay.nl = "\r\n"
	}
	return lay, nil
}

// locate finds the line an anchor names and checks that it is still the line the caller saw. field is "start" or "end".
func (l *layout) locate(src string, index int, field string, a anchor) (loc, error) {
	if a.line > l.lines {
		if l.lines == 0 {
			return loc{}, fmt.Errorf("edits[%d].%s: the file is empty and has no line %d; send content to fill it", index, field, a.line)
		}
		return loc{}, fmt.Errorf("edits[%d].%s: line %d is out of range, the file has %d lines", index, field, a.line, l.lines)
	}
	i, _ := slices.BinarySearch(l.named, a.line)
	at := l.locs[i]
	if got := lineHash(src[at.start:at.end]); got != a.hash {
		return loc{}, fmt.Errorf("edits[%d].%s: anchor %d:%04x does not match line %d, whose anchor is %d:%04x; read the file again", index, field, a.line, a.hash, a.line, a.line, got)
	}
	return at, nil
}

// clash is the error for two edits that cannot both be applied. a comes before b in the file.
func clash(a, b edit) error {
	x, y := min(a.index, b.index), max(a.index, b.index)
	if a.from == a.to && b.from == b.to {
		return fmt.Errorf("edits[%d] and edits[%d] insert at the same place, so their order would be ambiguous; send one insert", x, y)
	}
	return fmt.Errorf("edits[%d] and edits[%d] overlap; every edit refers to the lines as they were read, so two edits cannot change the same lines", x, y)
}

// assembler builds the result line by line: lines are separated by nl, and one more nl ends the text when the source
// ended with one. Separating rather than terminating keeps the end of the file as the source had it, however the last
// lines were edited.
type assembler struct {
	b     strings.Builder
	nl    string
	wrote bool
}

// run writes one line, or several lines taken as they are from the source, on a new line.
func (a *assembler) run(text string) {
	if a.wrote {
		a.b.WriteString(a.nl)
	}
	a.wrote = true
	a.b.WriteString(text)
}

// lines writes the lines of an edit's content, each on a new line. The content ends a line at LF or CRLF; a terminator
// at its very end does not start another line, and "" is one blank line.
func (a *assembler) lines(content string) {
	for {
		i := strings.IndexByte(content, '\n')
		if i < 0 {
			a.run(content)
			return
		}
		a.run(strings.TrimSuffix(content[:i], "\r"))
		content = content[i+1:]
		if content == "" {
			return
		}
	}
}

// applyEdits checks the anchors of edits against src and builds the result.
func applyEdits(src string, edits []edit) (string, error) {
	named := make([]int, 0, 2*len(edits))
	for _, e := range edits {
		named = append(named, e.start.line, e.end.line)
	}
	slices.Sort(named)
	lay, err := scan(src, slices.Compact(named))
	if err != nil {
		return "", err
	}
	for i := range edits {
		e := &edits[i]
		if e.first, err = lay.locate(src, e.index, "start", e.start); err != nil {
			return "", err
		}
		e.last = e.first
		if e.end != e.start {
			if e.last, err = lay.locate(src, e.index, "end", e.end); err != nil {
				return "", err
			}
		}
	}

	// In file order an edit starts where the previous one ended or later; an insert goes before a range that starts at
	// the same place, after a range that ends there, and two inserts at one place cannot be ordered.
	slices.SortFunc(edits, func(a, b edit) int {
		return cmp.Or(cmp.Compare(a.from, b.from), cmp.Compare(a.to, b.to), cmp.Compare(a.index, b.index))
	})
	for i := 1; i < len(edits); i++ {
		a, b := edits[i-1], edits[i]
		if b.from < a.to || (a.from == a.to && b.from == a.from && b.to == b.from) {
			return "", clash(a, b)
		}
	}

	// The size is a capacity hint: the source, the added text with room for a line break per line, minus what goes.
	size := len(src) + len(lay.nl)
	for i := range edits {
		e := &edits[i]
		if e.content != nil {
			size += len(*e.content) + len(lay.nl)*(strings.Count(*e.content, "\n")+2)
		}
		if e.from < e.to {
			size -= e.last.next - e.first.start
		}
	}
	out := assembler{nl: lay.nl}
	out.b.Grow(size)

	// done source lines are behind us and at is where line done+1 starts. Before each edit the lines up to the one it
	// comes after are copied unchanged, as a single slice of the source.
	done, at := 0, 0
	for i := range edits {
		e := &edits[i]
		if e.from > done {
			end := e.first.start - len(lay.nl)
			if e.op == OpInsertAfter {
				end = e.first.end
			}
			out.run(src[at:end])
		}
		if e.content != nil {
			out.lines(*e.content)
		}
		done = e.to
		if e.op == OpInsertBefore {
			at = e.first.start
		} else {
			at = e.last.next
		}
	}
	if done < lay.lines {
		end := len(src)
		if lay.final {
			end -= len(lay.nl)
		}
		out.run(src[at:end])
	}
	if lay.final && out.wrote {
		out.b.WriteString(lay.nl)
	}
	return out.b.String(), nil
}

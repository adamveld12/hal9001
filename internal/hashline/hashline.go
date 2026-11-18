// Package hashline edits text files through line anchors, for callers (agent tools) that read a file and then send back
// changes without resending the whole file.
//
// A read, [Snapshot], numbers the lines and gives each an anchor "<line>:<hash>", for example "12:a3f1". The hash is
// four lowercase hex digits computed from the bytes of the line alone, without its line terminator and without
// normalizing case or whitespace: the 32-bit FNV-1a hash of the line with its two 16-bit halves xored. It catches a
// miscounted or invented line number; it is not a version. The version of a file is [Hash], the SHA-256 of all its
// bytes.
//
// A write, [Apply], names the version it was made against ([Request].ExpectedHash) and carries either the complete new
// Content or a list of Edits. Every anchor refers to the file as it was read, so an edit never moves the lines another
// edit names. Edits that touch the same lines, or insert at the same place, are rejected instead of being ordered. A
// stale version, a bad anchor or a line out of range fails the whole request; nothing is relocated or repaired and no
// partial result exists.
//
// A line ends at LF or CRLF; a CR that is not followed by LF is ordinary text. The last line may lack a terminator. An
// empty file has no lines, so only Content can fill it. Edits copy every byte they do not touch, keep the file's line
// ending (all LF or all CRLF; a file that mixes them is rejected) and keep whether the file ends with a newline.
//
// The content of an edit is lines separated by LF or CRLF. One line terminator at its very end is ignored, so "a" and
// "a\n" are one line, "a\n\n" is "a" and a blank line, and "" is one blank line. The lines are written with the file's
// own line ending (LF when the file has no line break yet); a CR that does not start a CRLF is rejected. What edits
// cannot change, such as the final newline or the line ending, is changed by sending the whole file as Content, which is
// written exactly as given.
//
// The package works on strings and knows no storage, approval or redaction. Callers pass the raw source. A caller that
// shows the lines to someone who must not see everything redacts Line.Text itself and keeps the anchors, which come
// from the raw text. Error messages never quote the file's text.
package hashline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// The operations of an [Edit].
const (
	// OpReplace replaces the lines Start through End (End defaults to Start) with Content.
	OpReplace = "replace"
	// OpDelete removes the lines Start through End (End defaults to Start); it takes no Content.
	OpDelete = "delete"
	// OpInsertBefore adds Content before the line Start; it takes no End.
	OpInsertBefore = "insert_before"
	// OpInsertAfter adds Content after the line Start; it takes no End.
	OpInsertAfter = "insert_after"
)

// Request is a write to one file: the version it was made against and either the whole new content or edits.
//
// ExpectedHash is the Hash of the file as the caller read it. Empty means create: it is only accepted when the file does
// not exist, even if an existing file is empty. Exactly one of Content (a pointer, so that "" is a real, empty file)
// and a non-empty Edits must be set.
type Request struct {
	ExpectedHash string  `json:"expected_hash"`
	Content      *string `json:"content,omitempty"`
	Edits        []Edit  `json:"edits,omitempty"`
}

// Edit is one change to the lines of the file as it was read.
//
// Op is one of the Op constants. Start and End are anchors as listed by Snapshot ("12:a3f1"). Replace and delete cover
// the lines Start through End; End is optional and defaults to Start. The insert operations take Start only. Content is
// the new text of replace and of the insert operations, which require it ("" is one blank line), and is not allowed for
// delete.
type Edit struct {
	Op      string  `json:"op"`
	Start   string  `json:"start"`
	End     string  `json:"end,omitempty"`
	Content *string `json:"content,omitempty"`
}

// Line is one numbered, anchored line of a [View]. Text is the line without its terminator (LF or CRLF), exactly as
// stored: the engine neither normalizes nor redacts it.
type Line struct {
	Number int    `json:"number"`
	Anchor string `json:"anchor"`
	Text   string `json:"text"`
}

// View is what a reader gets of a file: its version and a window of its numbered lines.
//
// A missing file has Exists false, no Hash and no lines; no anchors are made up for it. TotalLines and HasFinalNewline
// describe the whole file, whatever window Lines covers. Lines is never nil, so it encodes as an empty array.
type View struct {
	Exists          bool   `json:"exists"`
	Hash            string `json:"hash"`
	Lines           []Line `json:"lines"`
	TotalLines      int    `json:"total_lines"`
	HasFinalNewline bool   `json:"has_final_newline"`
}

// ConflictError is the error of [Apply] when the file is not in the version the request names. Expected is the
// request's ExpectedHash, whatever it is: empty when the caller meant to create the file. Current is the hash of the
// file, empty when it does not exist. So the file exists although the caller wanted to create it (Expected empty), or
// it is gone (Current empty), or it differs (both set): it changed since the caller read it, or Expected is not the
// hash of a read.
type ConflictError struct {
	Expected, Current string
}

func (e *ConflictError) Error() string {
	switch {
	case e.Current == "":
		return "the file does not exist, but expected_hash names version " + short(e.Expected) +
			"; to create a file send an empty expected_hash with content"
	case e.Expected == "":
		return "the file already exists, so it cannot be created; read it and send its hash as expected_hash to change it"
	}
	return "the file does not match expected_hash (expected " + short(e.Expected) + ", now " + short(e.Current) +
		"): it changed since you read it, or the hash is not the one the read returned; read it again and redo the change"
}

// short abbreviates a hash for a message.
func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// Hash returns the version of a file's content: the lowercase hex SHA-256 of its bytes, the value spec.Hash gives for the
// same bytes.
func Hash(content string) string {
	h := sha256.New()
	// The digest takes bytes; feeding it through a buffer avoids converting the whole string to a slice.
	var buf [8192]byte
	for len(content) > 0 {
		n := copy(buf[:], content)
		_, _ = h.Write(buf[:n])
		content = content[n:]
	}
	var sum [sha256.Size]byte
	return hex.EncodeToString(h.Sum(sum[:0]))
}

// Snapshot lists the lines of content for a reader. exists false stands for a missing file; content is then ignored.
//
// startLine and endLine select lines by absolute, one-based number, both inclusive. 0 leaves that side open, so 0, 0 is
// the whole file. A window that extends past the last line is cut there, and one that starts after it has no lines;
// TotalLines tells the extent of the file either way. Negative numbers, and an end before the start, are errors.
func Snapshot(content string, exists bool, startLine, endLine int) (View, error) {
	if startLine < 0 || endLine < 0 || (endLine != 0 && startLine > endLine) {
		return View{}, fmt.Errorf("invalid line range %d-%d: lines count from 1 and the end cannot be before the start", startLine, endLine)
	}
	if !exists {
		return View{Lines: []Line{}}, nil
	}
	v := View{Exists: true, Hash: Hash(content), Lines: []Line{}, TotalLines: countLines(content), HasFinalNewline: strings.HasSuffix(content, "\n")}
	first, last := max(startLine, 1), v.TotalLines
	if endLine != 0 {
		last = min(endLine, last)
	}
	if first > last {
		return v, nil
	}
	pos := 0
	for range first - 1 {
		pos += strings.IndexByte(content[pos:], '\n') + 1
	}
	n := last - first + 1
	v.Lines = make([]Line, 0, n)
	// The anchors of all lines are built in one buffer and then cut out of one string: one allocation, not one per line.
	anchors := make([]byte, 0, n*(digits(last)+5))
	for number := first; number <= last; number++ {
		end, next := lineAt(content, pos)
		text := content[pos:end]
		anchors = appendAnchor(anchors, number, lineHash(text))
		v.Lines = append(v.Lines, Line{Number: number, Text: text})
		pos = next
	}
	all := string(anchors)
	for i := range v.Lines {
		l := &v.Lines[i]
		size := digits(l.Number) + 5
		l.Anchor, all = all[:size], all[size:]
	}
	return v, nil
}

// lineAt finds the line that starts at pos: end is where its text ends and next where the following line starts. A
// CRLF terminator is not part of the text. The last line of a source without a final newline has no terminator, so
// next == end == len(s) for it.
func lineAt(s string, pos int) (end, next int) {
	i := strings.IndexByte(s[pos:], '\n')
	if i < 0 {
		return len(s), len(s)
	}
	end = pos + i
	next = end + 1
	if end > pos && s[end-1] == '\r' {
		end--
	}
	return end, next
}

// countLines is the number of lines of s: its line breaks, plus one for a last line without one. "" has none.
func countLines(s string) int {
	n := strings.Count(s, "\n")
	if s != "" && s[len(s)-1] != '\n' {
		n++
	}
	return n
}

// lineHash is the 16 bit hash of an anchor: the 32 bit FNV-1a hash of the line's bytes, high and low half xored.
func lineHash(text string) uint32 {
	h := uint32(2166136261)
	for i := range len(text) {
		h ^= uint32(text[i])
		h *= 16777619
	}
	return (h ^ h>>16) & 0xffff
}

const hexDigits = "0123456789abcdef"

// appendAnchor appends "<line>:<hash>" with the hash as four lowercase hex digits.
func appendAnchor(buf []byte, line int, hash uint32) []byte {
	buf = strconv.AppendInt(buf, int64(line), 10)
	return append(buf, ':', hexDigits[hash>>12&0xf], hexDigits[hash>>8&0xf], hexDigits[hash>>4&0xf], hexDigits[hash&0xf])
}

// digits is the number of decimal digits of n, which is at least 1.
func digits(n int) int {
	d := 1
	for ; n >= 10; n /= 10 {
		d++
	}
	return d
}

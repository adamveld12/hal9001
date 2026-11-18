package hashline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"testing"
)

// anc is the anchor of line n whose text is text, as the package documents it: the number, a colon and the 32 bit
// FNV-1a hash of the text with its two halves xored, as four hex digits. It uses the standard library's FNV.
func anc(n int, text string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	sum := h.Sum32()
	return fmt.Sprintf("%d:%04x", n, (sum^sum>>16)&0xffff)
}

// wrong is an anchor with the line number of a and another hash.
func wrong(a string) string {
	if strings.HasSuffix(a, "0") {
		return a[:len(a)-1] + "1"
	}
	return a[:len(a)-1] + "0"
}

func rep(start, content string) Edit {
	return Edit{Op: OpReplace, Start: start, Content: new(content)}
}

func repTo(start, end, content string) Edit {
	return Edit{Op: OpReplace, Start: start, End: end, Content: new(content)}
}

func del(start string) Edit { return Edit{Op: OpDelete, Start: start} }

func delTo(start, end string) Edit { return Edit{Op: OpDelete, Start: start, End: end} }

func before(start, content string) Edit {
	return Edit{Op: OpInsertBefore, Start: start, Content: new(content)}
}

func after(start, content string) Edit {
	return Edit{Op: OpInsertAfter, Start: start, Content: new(content)}
}

func TestHash(t *testing.T) {
	for in, want := range map[string]string{
		"":    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"abc": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
	} {
		if got := Hash(in); got != want {
			t.Errorf("Hash(%q) = %s, want %s", in, got, want)
		}
	}
	// Longer than the buffer the digest is fed through.
	big := strings.Repeat("line of text\n", 3000)
	sum := sha256.Sum256([]byte(big))
	if got := Hash(big); got != hex.EncodeToString(sum[:]) {
		t.Errorf("Hash of %d bytes = %s, want %x", len(big), got, sum)
	}
}

func TestSnapshotLines(t *testing.T) {
	for _, tc := range []struct {
		name  string
		src   string
		texts []string
		final bool
	}{
		{"no final newline", "alpha\nbeta\n\ngamma", []string{"alpha", "beta", "", "gamma"}, false},
		{"final newline", "a\nb\n", []string{"a", "b"}, true},
		{"CRLF", "a\r\nb\r\n", []string{"a", "b"}, true},
		{"CRLF without final newline", "a\r\nb", []string{"a", "b"}, false},
		{"one blank line", "\n", []string{""}, true},
		{"two blank lines", "\n\n", []string{"", ""}, true},
		{"a bare CR stays in the text", "a\rb\nc\r", []string{"a\rb", "c\r"}, false},
		{"one unterminated line", "no newline", []string{"no newline"}, false},
		{"unicode", "héllo\n日本語\n😀\n", []string{"héllo", "日本語", "😀"}, true},
		{"unicode separators are not line breaks", "a\u2028b\nc\u0085d", []string{"a\u2028b", "c\u0085d"}, false},
		{"whitespace is kept", "  a\t\n\t\n", []string{"  a\t", "\t"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := Snapshot(tc.src, true, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !v.Exists || v.Hash != Hash(tc.src) || v.TotalLines != len(tc.texts) || v.HasFinalNewline != tc.final {
				t.Fatalf("view = %+v, want an existing file of %d lines, final newline %v", v, len(tc.texts), tc.final)
			}
			if len(v.Lines) != len(tc.texts) {
				t.Fatalf("%d lines, want %d: %+v", len(v.Lines), len(tc.texts), v.Lines)
			}
			for i, text := range tc.texts {
				want := Line{Number: i + 1, Anchor: anc(i+1, text), Text: text}
				if v.Lines[i] != want {
					t.Errorf("line %d = %+v, want %+v", i+1, v.Lines[i], want)
				}
			}
		})
	}
}

// The anchor hash is part of the contract (agents copy anchors, other code may recompute them), so a few values are
// pinned: the FNV-1a hashes of "", "a" and "abc" are 0x811c9dc5, 0xe40c292c and 0x1a47e90b.
func TestAnchorHash(t *testing.T) {
	v, err := Snapshot("\na\nabc\n", true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"1:1cd9", "2:cd20", "3:f34c"} {
		if got := v.Lines[i].Anchor; got != want {
			t.Errorf("anchor of line %d = %s, want %s", i+1, got, want)
		}
	}

	// Line numbers of several digits.
	long, err := Snapshot(strings.Repeat("x\n", 120), true, 98, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(long.Lines) != 4 {
		t.Fatalf("%d lines, want 4", len(long.Lines))
	}
	for i, l := range long.Lines {
		if want := anc(98+i, "x"); l.Anchor != want || l.Number != 98+i {
			t.Errorf("line %d = %+v, want anchor %s", 98+i, l, want)
		}
	}
}

func TestSnapshotMissingAndEmpty(t *testing.T) {
	missing, err := Snapshot("ignored", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Exists || missing.Hash != "" || missing.Lines == nil || len(missing.Lines) != 0 || missing.TotalLines != 0 || missing.HasFinalNewline {
		t.Errorf("missing file: %+v", missing)
	}
	b, err := json.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"exists":false,"hash":"","lines":[],"total_lines":0,"has_final_newline":false}`; string(b) != want {
		t.Errorf("missing file as JSON = %s, want %s", b, want)
	}

	empty, err := Snapshot("", true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Exists || empty.Hash != Hash("") || empty.Lines == nil || len(empty.Lines) != 0 || empty.TotalLines != 0 || empty.HasFinalNewline {
		t.Errorf("empty file: %+v", empty)
	}

	one, err := Snapshot("a\n", true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err = json.Marshal(one)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"exists":true,"hash":%q,"lines":[{"number":1,"anchor":%q,"text":"a"}],"total_lines":1,"has_final_newline":true}`, Hash("a\n"), anc(1, "a"))
	if string(b) != want {
		t.Errorf("view as JSON = %s, want %s", b, want)
	}
}

func TestSnapshotWindow(t *testing.T) {
	const src = "l1\nl2\nl3\nl4\nl5\n"
	for _, tc := range []struct {
		name       string
		start, end int
		first, n   int
	}{
		{"whole file", 0, 0, 1, 5},
		{"middle", 2, 4, 2, 3},
		{"one line", 3, 3, 3, 1},
		{"open end", 4, 0, 4, 2},
		{"open start", 0, 2, 1, 2},
		{"end past the file is cut", 4, 99, 4, 2},
		{"start past the file", 9, 12, 0, 0},
		{"start right after the last line", 6, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := Snapshot(src, true, tc.start, tc.end)
			if err != nil {
				t.Fatal(err)
			}
			if v.Hash != Hash(src) || v.TotalLines != 5 || !v.HasFinalNewline || !v.Exists {
				t.Errorf("the view describes the whole file whatever the window: %+v", v)
			}
			if v.Lines == nil || len(v.Lines) != tc.n {
				t.Fatalf("%d lines (nil: %v), want %d", len(v.Lines), v.Lines == nil, tc.n)
			}
			for i, l := range v.Lines {
				number := tc.first + i
				text := fmt.Sprintf("l%d", number)
				if want := (Line{Number: number, Anchor: anc(number, text), Text: text}); l != want {
					t.Errorf("line = %+v, want %+v", l, want)
				}
			}
		})
	}

	for _, r := range [][2]int{{-1, 0}, {0, -1}, {3, 2}, {5, 4}, {-2, -1}} {
		for _, exists := range []bool{true, false} {
			if _, err := Snapshot(src, exists, r[0], r[1]); err == nil {
				t.Errorf("Snapshot(exists %v, %d, %d) accepted an invalid range", exists, r[0], r[1])
			}
		}
	}
}

// The anchors of a window are absolute, so they can be used in a write without reading the rest of the file.
func TestWindowAnchorsDriveApply(t *testing.T) {
	const src = "one\ntwo\nthree\nfour\n"
	v, err := Snapshot(src, true, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	if v.Lines[0].Number != 3 || v.Lines[1].Number != 4 {
		t.Fatalf("window lines = %+v", v.Lines)
	}
	got, err := Apply(src, true, Request{ExpectedHash: v.Hash, Edits: []Edit{repTo(v.Lines[0].Anchor, v.Lines[1].Anchor, "X")}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "one\ntwo\nX\n"; got != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}

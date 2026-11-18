package hashline

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// edited is the request of an agent that read src and wants these edits: it carries the hash of src.
func edited(src string, edits ...Edit) Request {
	return Request{ExpectedHash: Hash(src), Edits: edits}
}

// applyTo applies edits to src the way an agent that read src would.
func applyTo(src string, edits ...Edit) (string, error) {
	return Apply(src, true, edited(src, edits...))
}

func TestApplyEdits(t *testing.T) {
	const (
		abc  = "a\nb\nc\n"
		nums = "1\n2\n3\n4\n5\n"
		dup  = "x\n\nx\n\nx\n"
		crlf = "a\r\nb\r\nc\r\n"
		conf = "# header  \n\tkey: value \t\n\n  # comment\nlast: 1\n"
	)
	a1, a2, a3 := anc(1, "a"), anc(2, "b"), anc(3, "c")
	n := func(i int) string { return anc(i, strconv.Itoa(i)) }

	for _, tc := range []struct {
		name  string
		src   string
		edits []Edit
		want  string
	}{
		{"replace the first line", abc, []Edit{rep(a1, "X")}, "X\nb\nc\n"},
		{"replace a middle line", abc, []Edit{rep(a2, "X")}, "a\nX\nc\n"},
		{"replace the last line", abc, []Edit{rep(a3, "X")}, "a\nb\nX\n"},
		{"replace one line with several", abc, []Edit{rep(a2, "X\nY\nZ")}, "a\nX\nY\nZ\nc\n"},
		{"replace a range with one line", "a\nb\nc\nd\n", []Edit{repTo(a2, a3, "X")}, "a\nX\nd\n"},
		{"an explicit one-line range", abc, []Edit{repTo(a2, a2, "X")}, "a\nX\nc\n"},
		{"replace every line", "a\nb\n", []Edit{repTo(a1, a2, "x")}, "x\n"},
		{"content ending in a newline is the same lines", abc, []Edit{rep(a2, "X\n")}, "a\nX\nc\n"},
		{"content ending in two newlines ends in a blank line", abc, []Edit{rep(a2, "X\n\n")}, "a\nX\n\nc\n"},
		{"empty content is one blank line", abc, []Edit{rep(a2, "")}, "a\n\nc\n"},
		{"a newline alone is one blank line", abc, []Edit{rep(a2, "\n")}, "a\n\nc\n"},

		{"delete a line", abc, []Edit{del(a2)}, "a\nc\n"},
		{"delete the first line", abc, []Edit{del(a1)}, "b\nc\n"},
		{"delete the last line", abc, []Edit{del(a3)}, "a\nb\n"},
		{"delete a range", "a\nb\nc\nd\n", []Edit{delTo(a2, a3)}, "a\nd\n"},
		{"delete every line", "a\nb\n", []Edit{delTo(a1, a2)}, ""},

		{"insert before the first line", abc, []Edit{before(a1, "X")}, "X\na\nb\nc\n"},
		{"insert before a line", abc, []Edit{before(a2, "X")}, "a\nX\nb\nc\n"},
		{"insert after a line", abc, []Edit{after(a2, "X")}, "a\nb\nX\nc\n"},
		{"insert after the last line", abc, []Edit{after(a3, "X")}, "a\nb\nc\nX\n"},
		{"insert several lines", abc, []Edit{after(a1, "X\nY")}, "a\nX\nY\nb\nc\n"},
		{"insert a blank line", abc, []Edit{after(a1, "")}, "a\n\nb\nc\n"},

		{"edits refer to the file as it was read", nums, []Edit{before(n(1), "0"), del(n(3)), rep(n(5), "five")}, "0\n1\n2\n4\nfive\n"},
		{"edits in any order", nums, []Edit{rep(n(5), "five"), del(n(3)), before(n(1), "0")}, "0\n1\n2\n4\nfive\n"},
		{"inserts next to a replaced range", nums, []Edit{repTo(n(3), n(4), "X"), before(n(3), "B"), after(n(4), "A")}, "1\n2\nB\nX\nA\n5\n"},
		{"an insert right before a replaced range", nums, []Edit{repTo(n(2), n(4), "X"), before(n(2), "B")}, "1\nB\nX\n5\n"},
		{"an insert right after a replaced range", nums, []Edit{repTo(n(2), n(4), "X"), after(n(4), "A")}, "1\nX\nA\n5\n"},
		{"adjacent ranges", "1\n2\n3\n4\n", []Edit{repTo(n(1), n(2), "X"), repTo(n(3), n(4), "Y")}, "X\nY\n"},
		{"adjacent deletions", nums, []Edit{del(n(2)), del(n(3))}, "1\n4\n5\n"},
		{"an insert between adjacent deletions", nums, []Edit{del(n(2)), after(n(2), "X"), del(n(3))}, "1\nX\n4\n5\n"},
		{"inserts at neighboring places", nums, []Edit{after(n(2), "x"), before(n(4), "y")}, "1\n2\nx\n3\ny\n4\n5\n"},
		{"a replaced last line and an insert after it", nums, []Edit{rep(n(5), "X"), after(n(5), "Y")}, "1\n2\n3\n4\nX\nY\n"},

		{"other lines are copied byte for byte", conf, []Edit{rep(anc(2, "\tkey: value \t"), "\tkey: other")}, "# header  \n\tkey: other\n\n  # comment\nlast: 1\n"},
		{"the last line of a file with blank lines", conf, []Edit{rep(anc(5, "last: 1"), "last: 2")}, "# header  \n\tkey: value \t\n\n  # comment\nlast: 2\n"},

		{"no final newline: replace the last line", "a\nb", []Edit{rep(a2, "X")}, "a\nX"},
		{"no final newline: content ending in a newline", "a\nb", []Edit{rep(a2, "X\n")}, "a\nX"},
		{"no final newline: delete the last line", "a\nb", []Edit{del(a2)}, "a"},
		{"no final newline: insert after the last line", "a\nb", []Edit{after(a2, "X")}, "a\nb\nX"},
		{"no final newline: insert before the last line", "a\nb", []Edit{before(a2, "X")}, "a\nX\nb"},
		{"one unterminated line", "a", []Edit{rep(a1, "b\nc")}, "b\nc"},
		{"delete the only line, unterminated", "a", []Edit{del(a1)}, ""},

		{"duplicate lines are told apart by number", dup, []Edit{rep(anc(3, "x"), "Y")}, "x\n\nY\n\nx\n"},
		{"delete a blank line", dup, []Edit{del(anc(4, ""))}, "x\n\nx\nx\n"},
		{"replace across blank lines", dup, []Edit{repTo(anc(2, ""), anc(4, ""), "-")}, "x\n-\nx\n"},

		{"unicode", "héllo\n日本語\n😀\n", []Edit{rep(anc(2, "日本語"), "世界")}, "héllo\n世界\n😀\n"},
		{"unicode separators are ordinary text", "a\u2028b\nc\u0085d\n", []Edit{rep(anc(2, "c\u0085d"), "X")}, "a\u2028b\nX\n"},
		{"unicode separators in content are not line breaks", abc, []Edit{rep(a2, "x\u2028y")}, "a\nx\u2028y\nc\n"},

		{"CRLF: several lines", crlf, []Edit{rep(a2, "X\nY")}, "a\r\nX\r\nY\r\nc\r\n"},
		{"CRLF: content with CRLF", crlf, []Edit{rep(a2, "X\r\nY\r\n")}, "a\r\nX\r\nY\r\nc\r\n"},
		{"CRLF: insert after the last line", crlf, []Edit{after(a3, "X")}, "a\r\nb\r\nc\r\nX\r\n"},
		{"CRLF: delete the first line", crlf, []Edit{del(a1)}, "b\r\nc\r\n"},
		{"CRLF without final newline: insert after the last line", "a\r\nb", []Edit{after(a2, "c")}, "a\r\nb\r\nc"},
		{"CRLF without final newline: delete the last line", "a\r\nb", []Edit{del(a2)}, "a"},
		{"LF file, CRLF content", "a\nb\n", []Edit{rep(a1, "x\r\ny")}, "x\ny\nb\n"},

		{"a bare CR in an untouched line", "a\rb\nc\n", []Edit{rep(anc(2, "c"), "X")}, "a\rb\nX\n"},
		{"replace the line with a bare CR", "a\rb\nc\n", []Edit{rep(anc(1, "a\rb"), "Z")}, "Z\nc\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyTo(tc.src, tc.edits...)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("result = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApplyRejectsEdits(t *testing.T) {
	const abc = "a\nb\nc\n"
	a1, a2, a3 := anc(1, "a"), anc(2, "b"), anc(3, "c")

	for _, tc := range []struct {
		name  string
		src   string
		edits []Edit
		want  string
	}{
		{"unknown op", abc, []Edit{{Op: "swap", Start: a1, Content: new("x")}}, "unknown op"},
		{"missing op", abc, []Edit{{Start: a1, Content: new("x")}}, "unknown op"},
		{"the op is case sensitive", abc, []Edit{{Op: "Replace", Start: a1, Content: new("x")}}, "unknown op"},
		{"replace needs content", abc, []Edit{{Op: OpReplace, Start: a1}}, "needs content"},
		{"insert needs content", abc, []Edit{{Op: OpInsertAfter, Start: a1}}, "needs content"},
		{"delete takes no content", abc, []Edit{{Op: OpDelete, Start: a1, Content: new("")}}, "no content"},
		{"an insert takes no end", abc, []Edit{{Op: OpInsertBefore, Start: a1, End: a2, Content: new("x")}}, "start anchor only"},
		{"an insert takes no end, even the same line", abc, []Edit{{Op: OpInsertAfter, Start: a1, End: a1, Content: new("x")}}, "start anchor only"},
		{"content with a lone CR", abc, []Edit{rep(a1, "x\ry")}, "carriage return"},
		{"content ending in a lone CR", abc, []Edit{rep(a1, "x\r")}, "carriage return"},
		{"missing start", abc, []Edit{{Op: OpDelete}}, "edits[0].start"},
		{"end before start", abc, []Edit{repTo(a3, a1, "x")}, "before start"},
		{"line out of range", abc, []Edit{del(anc(4, "d"))}, "out of range"},
		{"end out of range", abc, []Edit{delTo(a2, anc(9, "z"))}, "out of range"},
		{"wrong hash", abc, []Edit{rep(wrong(a2), "x")}, "does not match"},
		{"wrong hash on the end", abc, []Edit{repTo(a1, wrong(a2), "x")}, "edits[0].end"},
		{"the hash of another line is not relocated", abc, []Edit{rep(anc(1, "b"), "x")}, "does not match"},
		{"the second edit is the bad one", abc, []Edit{rep(a1, "x"), rep(wrong(a2), "y")}, "edits[1]"},
		{"an empty file has no lines", "", []Edit{del(anc(1, ""))}, "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyTo(tc.src, tc.edits...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
			var ce *ConflictError
			if errors.As(err, &ce) {
				t.Errorf("a bad edit is not a conflict: %v", err)
			}
			if got != "" {
				t.Errorf("a rejected request produced content %q", got)
			}
		})
	}
}

func TestApplyRejectsMalformedAnchors(t *testing.T) {
	const abc = "a\nb\nc\n"
	for _, bad := range []string{
		"2", "2:", ":abcd", "2:abc", "2:abcde", "2:ABCD", "02:abcd", "0:abcd", "-1:abcd", "+2:abcd", "2:abcg", " 2:abcd",
		"2:abcd ", "2: abcd", "2-abcd", "two:abcd", "2:abcd:ef", "99999999999999999999:abcd", "1:12345",
	} {
		for field, ed := range map[string]Edit{
			"start": {Op: OpDelete, Start: bad},
			"end":   {Op: OpDelete, Start: anc(1, "a"), End: bad},
		} {
			_, err := applyTo(abc, ed)
			if err == nil || !strings.Contains(err.Error(), "not an anchor") || !strings.Contains(err.Error(), field) {
				t.Errorf("%s %q: error = %v, want one about a malformed %s anchor", field, bad, err, field)
			}
		}
	}
}

func TestApplyRejectsOverlaps(t *testing.T) {
	n := func(i int) string { return anc(i, strconv.Itoa(i)) }
	const nums = "1\n2\n3\n4\n5\n"

	for _, tc := range []struct {
		name  string
		edits []Edit
		want  string
	}{
		{"the same line twice", []Edit{rep(n(2), "x"), rep(n(2), "y")}, "edits[0] and edits[1] overlap"},
		{"overlapping ranges", []Edit{repTo(n(2), n(4), "x"), delTo(n(3), n(5))}, "overlap"},
		{"a range inside a range", []Edit{repTo(n(1), n(5), "x"), del(n(3))}, "overlap"},
		{"the same range twice", []Edit{delTo(n(2), n(3)), delTo(n(2), n(3))}, "overlap"},
		{"an insert inside a replaced range, after its first line", []Edit{repTo(n(2), n(4), "x"), after(n(2), "y")}, "overlap"},
		{"an insert inside a replaced range, before its last line", []Edit{repTo(n(2), n(4), "x"), before(n(4), "y")}, "overlap"},
		{"an insert inside a deleted range", []Edit{delTo(n(2), n(4)), before(n(3), "y")}, "overlap"},
		{"two inserts after the same line", []Edit{after(n(2), "x"), after(n(2), "y")}, "same place"},
		{"two inserts before the same line", []Edit{before(n(2), "x"), before(n(2), "y")}, "same place"},
		{"an insert after a line and one before the next", []Edit{after(n(2), "x"), before(n(3), "y")}, "same place"},
		{"the message names the edits as the request numbers them", []Edit{rep(n(5), "x"), rep(n(1), "y"), rep(n(5), "z")}, "edits[0] and edits[2] overlap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyTo(nums, tc.edits...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
			if got != "" {
				t.Errorf("a rejected request produced content %q", got)
			}
		})
	}
}

func wantConflict(t *testing.T, got string, err error, expected, current string) {
	t.Helper()
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v, want a *ConflictError", err)
	}
	if ce.Expected != expected || ce.Current != current {
		t.Errorf("conflict = {expected %q, current %q}, want {%q, %q}", ce.Expected, ce.Current, expected, current)
	}
	if got != "" {
		t.Errorf("a conflict produced content %q", got)
	}
}

func TestApplyVersionChecks(t *testing.T) {
	const old, cur = "a\nb\nc\n", "A\nb\nc\n"

	t.Run("stale hash although the anchor still matches", func(t *testing.T) {
		// Line 2 is the same in both versions, so its anchor is valid; only the hash of the file says it changed.
		got, err := Apply(cur, true, Request{ExpectedHash: Hash(old), Edits: []Edit{rep(anc(2, "b"), "X")}})
		wantConflict(t, got, err, Hash(old), Hash(cur))
	})
	t.Run("stale hash with content", func(t *testing.T) {
		got, err := Apply(cur, true, Request{ExpectedHash: Hash(old), Content: new("x")})
		wantConflict(t, got, err, Hash(old), Hash(cur))
	})
	t.Run("a hash that is no hash does not match either", func(t *testing.T) {
		for _, h := range []string{"stale", strings.ToUpper(Hash(cur)), Hash(cur)[:12], Hash(cur) + "0"} {
			got, err := Apply(cur, true, Request{ExpectedHash: h, Content: new("x")})
			wantConflict(t, got, err, h, Hash(cur))
		}
	})
	t.Run("the version is checked before the anchors", func(t *testing.T) {
		got, err := Apply(cur, true, Request{ExpectedHash: Hash(old), Edits: []Edit{del(anc(99, "zzz"))}})
		wantConflict(t, got, err, Hash(old), Hash(cur))
	})
	t.Run("the exact hash is accepted", func(t *testing.T) {
		got, err := Apply(cur, true, Request{ExpectedHash: Hash(cur), Content: new("new")})
		if err != nil || got != "new" {
			t.Errorf("got %q, %v", got, err)
		}
	})

	t.Run("an empty hash never overwrites", func(t *testing.T) {
		got, err := Apply(cur, true, Request{Content: new("x")})
		wantConflict(t, got, err, "", Hash(cur))
		got, err = Apply("", true, Request{Content: new("x")})
		wantConflict(t, got, err, "", Hash(""))
		got, err = Apply(cur, true, Request{Edits: []Edit{del(anc(1, "A"))}})
		wantConflict(t, got, err, "", Hash(cur))
	})
	t.Run("a missing file is created with an empty hash", func(t *testing.T) {
		for _, content := range []string{"x", "", "x\r\ny"} {
			// What the caller passes as the source of a missing file is ignored.
			if got, err := Apply("junk", false, Request{Content: new(content)}); err != nil || got != content {
				t.Errorf("create %q: got %q, %v", content, got, err)
			}
		}
	})
	t.Run("a missing file does not match a hash", func(t *testing.T) {
		got, err := Apply("", false, Request{ExpectedHash: Hash("x"), Content: new("y")})
		wantConflict(t, got, err, Hash("x"), "")
		got, err = Apply("", false, Request{ExpectedHash: Hash("x"), Edits: []Edit{del(anc(1, "x"))}})
		wantConflict(t, got, err, Hash("x"), "")
	})
	t.Run("a missing file cannot be edited", func(t *testing.T) {
		got, err := Apply("", false, Request{Edits: []Edit{del(anc(1, "x"))}})
		var ce *ConflictError
		if err == nil || errors.As(err, &ce) || !strings.Contains(err.Error(), "nothing to edit") || got != "" {
			t.Errorf("got %q, %v", got, err)
		}
	})

	t.Run("the messages say what to do", func(t *testing.T) {
		for _, tc := range []struct {
			err  ConflictError
			want string
		}{
			{ConflictError{Expected: Hash(old)}, "does not exist"},
			{ConflictError{Current: Hash(cur)}, "already exists"},
			{ConflictError{Expected: Hash(old), Current: Hash(cur)}, "read it again"},
		} {
			if got := tc.err.Error(); !strings.Contains(got, tc.want) {
				t.Errorf("%+v: message %q lacks %q", tc.err, got, tc.want)
			}
		}
	})
}

func TestRequestModes(t *testing.T) {
	const src = "a\n"
	h := Hash(src)
	one := []Edit{del(anc(1, "a"))}
	for _, tc := range []struct {
		name string
		req  Request
		want string
	}{
		{"both content and edits", Request{ExpectedHash: h, Content: new("x"), Edits: one}, "either content or edits"},
		{"neither", Request{ExpectedHash: h}, "send content"},
		{"an empty list of edits", Request{ExpectedHash: h, Edits: []Edit{}}, "send content"},
	} {
		for _, exists := range []bool{true, false} {
			got, err := Apply(src, exists, tc.req)
			var ce *ConflictError
			if err == nil || errors.As(err, &ce) || !strings.Contains(err.Error(), tc.want) || got != "" {
				t.Errorf("%s (exists %v): got %q, %v; want an error containing %q", tc.name, exists, got, err, tc.want)
			}
		}
	}
}

func TestApplyContentMode(t *testing.T) {
	for _, tc := range []struct{ name, src, content string }{
		{"CRLF is written as given", "a\nb\n", "x\r\ny"},
		{"no final newline", "a\nb\n", "x"},
		{"empty", "a\nb\n", ""},
		{"a source with mixed line endings does not matter", "a\r\nb\nc", "z\n"},
		{"an empty file", "", "x\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Apply(tc.src, true, Request{ExpectedHash: Hash(tc.src), Content: new(tc.content)})
			if err != nil || got != tc.content {
				t.Errorf("got %q, %v, want %q", got, err, tc.content)
			}
		})
	}
}

func TestApplyRefusesToChangeLineEndings(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{"LF and CRLF", "a\r\nb\nc\n", "mixes LF and CRLF"},
		{"CRLF after LF", "a\nb\r\nc\r\n", "mixes LF and CRLF"},
		{"a lone CR at the end", "a\nb\r", "carriage return"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyTo(tc.src, del(anc(1, "a")))
			if err == nil || !strings.Contains(err.Error(), tc.want) || got != "" {
				t.Fatalf("got %q, %v, want an error containing %q", got, err, tc.want)
			}
			// Reading such a file works, and the whole file can still be written.
			if v, err := Snapshot(tc.src, true, 0, 0); err != nil || v.TotalLines == 0 {
				t.Errorf("Snapshot = %+v, %v", v, err)
			}
			if got, err := Apply(tc.src, true, Request{ExpectedHash: Hash(tc.src), Content: new("ok")}); err != nil || got != "ok" {
				t.Errorf("content mode = %q, %v", got, err)
			}
		})
	}
}

// The messages are shown to whoever wrote the request, who may not be allowed to see the file.
func TestErrorsDoNotQuoteTheFile(t *testing.T) {
	const secret = "hunter2-secret"
	line := "TOKEN=" + secret
	src := line + "\nb\n"
	first := anc(1, line)
	mixed := line + "\r\nb\n"
	lone := line + "\nb\r"

	for _, tc := range []struct {
		name string
		src  string
		req  Request
	}{
		{"anchor mismatch", src, edited(src, rep(wrong(first), "x"))},
		{"out of range", src, edited(src, del(anc(9, "x")))},
		{"overlap", src, edited(src, rep(first, "x"), rep(first, "y"))},
		{"same place", src, edited(src, after(first, "x"), before(anc(2, "b"), "y"))},
		{"stale", src, Request{ExpectedHash: Hash("other"), Content: new("x")}},
		{"create", src, Request{Content: new("x")}},
		{"mixed endings", mixed, edited(mixed, del(first))},
		{"lone CR", lone, edited(lone, del(first))},
	} {
		_, err := Apply(tc.src, true, tc.req)
		if err == nil {
			t.Errorf("%s: no error", tc.name)
		} else if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: the message quotes the file: %v", tc.name, err)
		}
	}
}

func TestRequestJSON(t *testing.T) {
	const src = "a\nb\n"
	body := fmt.Sprintf(`{"expected_hash":%q,"edits":[{"op":"replace","start":%q,"content":"X"},{"op":"delete","start":%q}]}`, Hash(src), anc(1, "a"), anc(2, "b"))
	var req Request
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if got, err := Apply(src, true, req); err != nil || got != "X\n" {
		t.Errorf("decoded request: got %q, %v", got, err)
	}

	// An empty content is a real value (an empty file); a missing or null one is no content.
	req = Request{}
	if err := json.Unmarshal([]byte(`{"expected_hash":"","content":""}`), &req); err != nil {
		t.Fatal(err)
	}
	if got, err := Apply("", false, req); err != nil || got != "" {
		t.Errorf("empty content: got %q, %v", got, err)
	}
	req = Request{}
	if err := json.Unmarshal([]byte(`{"expected_hash":"","content":null}`), &req); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("", false, req); err == nil {
		t.Error("null content accepted")
	}

	b, err := json.Marshal(Request{ExpectedHash: "h", Content: new("")})
	if want := `{"expected_hash":"h","content":""}`; err != nil || string(b) != want {
		t.Errorf("content request = %s, %v, want %s", b, err, want)
	}
	b, err = json.Marshal(Request{ExpectedHash: "h", Edits: []Edit{del("1:abcd"), rep("2:abcd", "")}})
	if want := `{"expected_hash":"h","edits":[{"op":"delete","start":"1:abcd"},{"op":"replace","start":"2:abcd","content":""}]}`; err != nil || string(b) != want {
		t.Errorf("edit request = %s, %v, want %s", b, err, want)
	}
}

// refEdit is an edit of the reference model, which works on a list of lines.
type refEdit struct {
	op      string
	s, e    int
	content string
}

func (e refEdit) isInsert() bool { return e.op == OpInsertBefore || e.op == OpInsertAfter }

// gap is where an insertion goes: after the line with this number.
func (e refEdit) gap() int {
	if e.op == OpInsertBefore {
		return e.s - 1
	}
	return e.s
}

// contentLines are the lines of an edit's content: split at LF or CRLF, a break at the very end not counted, and ""
// one blank line.
func contentLines(content string) []string {
	parts := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// reference is a slow model of Apply that works on lines: whether the edits are compatible and, if so, the result.
func reference(lines []string, nl string, final bool, edits []refEdit) (string, bool) {
	for i, a := range edits {
		for _, b := range edits[i+1:] {
			switch {
			case !a.isInsert() && !b.isInsert():
				if a.s <= b.e && b.s <= a.e {
					return "", false
				}
			case a.isInsert() && b.isInsert():
				if a.gap() == b.gap() {
					return "", false
				}
			default:
				ins, rng := a, b
				if b.isInsert() {
					ins, rng = b, a
				}
				if g := ins.gap(); rng.s-1 < g && g < rng.e {
					return "", false
				}
			}
		}
	}
	inserts := map[int][]string{}
	ranges := map[int]refEdit{}
	for _, e := range edits {
		if e.isInsert() {
			inserts[e.gap()] = contentLines(e.content)
		} else {
			ranges[e.s] = e
		}
	}
	out := append([]string(nil), inserts[0]...)
	for i := 1; i <= len(lines); {
		if e, ok := ranges[i]; ok {
			if e.op == OpReplace {
				out = append(out, contentLines(e.content)...)
			}
			out = append(out, inserts[e.e]...)
			i = e.e + 1
			continue
		}
		out = append(out, lines[i-1])
		out = append(out, inserts[i]...)
		i++
	}
	if len(out) == 0 {
		return "", true
	}
	res := strings.Join(out, nl)
	if final {
		res += nl
	}
	return res, true
}

// Random sources and edits, compared with the reference model: the same edits are accepted, with the same result.
func TestApplyMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	texts := []string{"", "a", "b", "x y", "é", "  sp  ", "#c"}
	contents := []string{"", "X", "X\nY", "X\n", "X\r\nY", "\n"}
	ops := []string{OpReplace, OpDelete, OpInsertBefore, OpInsertAfter}

	for i := range 3000 {
		n := 1 + r.IntN(7)
		lines := make([]string, n)
		for j := range lines {
			lines[j] = texts[r.IntN(len(texts))]
		}
		nl := "\n"
		if r.IntN(2) == 0 {
			nl = "\r\n"
		}
		final := r.IntN(2) == 0
		if !final && lines[n-1] == "" {
			// A last empty line without a line break cannot be told from a final newline.
			lines[n-1] = "z"
		}
		src := strings.Join(lines, nl)
		if final {
			src += nl
		}
		if n == 1 && !final {
			// There is no line break in the source, so no line ending to keep: new lines get LF.
			nl = "\n"
		}

		var refs []refEdit
		var edits []Edit
		for range 1 + r.IntN(4) {
			op := ops[r.IntN(len(ops))]
			s := 1 + r.IntN(n)
			e := s
			ed := Edit{Op: op, Start: anc(s, lines[s-1])}
			if op == OpReplace || op == OpDelete {
				e = s + r.IntN(min(3, n-s+1))
				if e != s || r.IntN(2) == 0 {
					ed.End = anc(e, lines[e-1])
				}
			}
			ref := refEdit{op: op, s: s, e: e}
			if op != OpDelete {
				c := contents[r.IntN(len(contents))]
				ed.Content = new(c)
				ref.content = c
			}
			edits = append(edits, ed)
			refs = append(refs, ref)
		}

		want, ok := reference(lines, nl, final, refs)
		got, err := applyTo(src, edits...)
		switch {
		case !ok && (err == nil || got != ""):
			t.Fatalf("iteration %d: source %q, edits %+v: accepted with result %q, the reference rejects them", i, src, refs, got)
		case ok && err != nil:
			t.Fatalf("iteration %d: source %q, edits %+v: rejected (%v), the reference accepts them", i, src, refs, err)
		case ok && got != want:
			t.Fatalf("iteration %d: source %q, edits %+v: result %q, want %q", i, src, refs, got, want)
		}
	}
}

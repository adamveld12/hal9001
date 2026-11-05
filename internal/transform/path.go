package transform

import (
	"fmt"
	"strings"
	"unicode"
)

// seg is one step of a field path: a key, or "every element" (a "[]").
type seg struct {
	key  string
	each bool
}

// parsePath parses a dot path. "" and "." are the value itself. "a.b" is
// key a then key b; "a[].b" is key a, every element, key b; a leading "." is
// optional (".a" is "a"); a path may start with "[]".
func parsePath(s string) ([]seg, error) {
	if s == "" || s == "." {
		return nil, nil
	}
	s = strings.TrimPrefix(s, ".")
	var segs []seg
	i := 0
	for i < len(s) {
		start := i
		for i < len(s) && s[i] != '.' && s[i] != '[' && s[i] != ']' {
			i++
		}
		key := s[start:i]
		if err := checkKey(key); err != nil {
			return nil, err
		}
		keyed := key != ""
		if keyed {
			segs = append(segs, seg{key: key})
		}
		each := false
		for strings.HasPrefix(s[i:], "[]") {
			segs = append(segs, seg{each: true})
			i += 2
			each = true
		}
		switch {
		case i == len(s):
		case s[i] == '.':
			i++
			if i == len(s) {
				return nil, fmt.Errorf("the path %q ends with a dot", s)
			}
		case s[i] == '[' || s[i] == ']':
			return nil, fmt.Errorf("the path %q has a stray bracket: use [] (empty brackets) for any element", s)
		default:
			return nil, fmt.Errorf("the path %q needs a dot after []", s)
		}
		if !keyed && !each {
			return nil, fmt.Errorf("the path %q has an empty field name", s)
		}
	}
	return segs, nil
}

func checkKey(key string) error {
	for _, r := range key {
		if unicode.IsControl(r) {
			return fmt.Errorf("field names cannot contain control characters")
		}
	}
	return nil
}

func hasEach(segs []seg) bool {
	for _, s := range segs {
		if s.each {
			return true
		}
	}
	return false
}

// keysOf returns the keys of a path that has no "[]".
func keysOf(segs []seg) []string {
	keys := make([]string, 0, len(segs))
	for _, s := range segs {
		keys = append(keys, s.key)
	}
	return keys
}

// formatPath renders segs the way users write them.
func formatPath(segs []seg) string {
	if len(segs) == 0 {
		return "."
	}
	var b strings.Builder
	for i, s := range segs {
		if s.each {
			b.WriteString("[]")
			continue
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s.key)
	}
	return b.String()
}

package runner

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// MinSecretLen is the shortest env-file value treated as a secret. Shorter values
// ("1", "true", "debug") would corrupt ordinary output if replaced.
const MinSecretLen = 8

// Redactor replaces secret values with «KEY» placeholders.
type Redactor struct {
	r *strings.Replacer // nil when there is nothing to redact
}

// NewRedactor builds a Redactor from env-file values (key → value). Values shorter
// than MinSecretLen are ignored. Longer values are matched first, so a secret that
// contains another is replaced as a whole. The JSON-escaped form of each value is
// also redacted, since stdout is usually JSON. When several keys share a value the
// alphabetically first key names it.
//
// Several maps may be given (the global env and per-sensor env files): every
// value of every map is redacted, named by its own key.
func NewRedactor(secrets ...map[string]string) *Redactor {
	type pair struct{ val, key string }
	byVal := map[string]string{}
	for _, m := range secrets {
		for k, v := range m {
			if len(v) < MinSecretLen {
				continue
			}
			if cur, ok := byVal[v]; !ok || k < cur {
				byVal[v] = k
			}
			if esc := jsonEscaped(v); esc != v {
				if cur, ok := byVal[esc]; !ok || k < cur {
					byVal[esc] = k
				}
			}
		}
	}
	if len(byVal) == 0 {
		return &Redactor{}
	}
	pairs := make([]pair, 0, len(byVal))
	for v, k := range byVal {
		pairs = append(pairs, pair{v, k})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if len(pairs[i].val) != len(pairs[j].val) {
			return len(pairs[i].val) > len(pairs[j].val)
		}
		return pairs[i].val < pairs[j].val
	})
	args := make([]string, 0, 2*len(pairs))
	for _, p := range pairs {
		args = append(args, p.val, "«"+p.key+"»")
	}
	// strings.Replacer tries pairs in argument order at each position: longest first.
	return &Redactor{r: strings.NewReplacer(args...)}
}

// jsonEscaped returns s as it appears inside a JSON string literal.
func jsonEscaped(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return s
	}
	out := strings.TrimSuffix(buf.String(), "\n")
	return out[1 : len(out)-1]
}

// String redacts s. A nil Redactor returns s unchanged.
func (r *Redactor) String(s string) string {
	if r == nil || r.r == nil {
		return s
	}
	return r.r.Replace(s)
}

// Bytes redacts b, returning b itself when nothing needs replacing.
func (r *Redactor) Bytes(b []byte) []byte {
	if r == nil || r.r == nil || len(b) == 0 {
		return b
	}
	return []byte(r.r.Replace(string(b)))
}

// Argv redacts each argument, returning a new slice.
func (r *Redactor) Argv(argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = r.String(a)
	}
	return out
}

// Result returns a copy of res with stdout, stderr and argv redacted.
func (r *Redactor) Result(res Result) Result {
	res.Stdout = r.Bytes(res.Stdout)
	res.Stderr = r.Bytes(res.Stderr)
	res.Argv = r.Argv(res.Argv)
	return res
}

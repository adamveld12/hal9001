package sandbox

import (
	"bytes"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// limitBuffer keeps the first limit bytes written to it.
type limitBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.limit - b.buf.Len()
	if room < len(p) {
		b.truncated = true
		p2 := p
		if room > 0 {
			b.buf.Write(p2[:room])
		}
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *limitBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *limitBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

func (b *limitBuffer) wasTruncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// relPath turns p, relative to workdir or absolute inside it, into a clean workdir-relative slash path
// ("." for the workdir itself). A path that leaves the workdir lexically fails with ErrEscape.
func relPath(workdir, p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("invalid path %q", p)
	}
	if p == "" {
		p = "."
	}
	if path.IsAbs(p) {
		r, err := filepath.Rel(workdir, p)
		if err != nil {
			return "", ErrEscape
		}
		p = filepath.ToSlash(r)
	}
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", ErrEscape
	}
	return p, nil
}

// shQuote quotes s for a POSIX shell.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// envExports renders `export K='v' ...;` for the env map, sorted for stable output.
func envExports(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("export")
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%s", k, shQuote(env[k]))
	}
	b.WriteString("; ")
	return b.String()
}

// mergeEnv returns the maps overlaid left to right.
func mergeEnv(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

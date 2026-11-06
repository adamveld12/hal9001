package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// pathRules are the path-based parts of AllowRules, resolved once.
type pathRules struct {
	base      string
	editUnder []resolvedPath // allow.edit_under
	prot      []resolvedPath // protected
	editDeny  []resolvedPath // edit_deny
}

// resolvedPath keeps a configured path as written (cleaned, absolute) and with
// symlinks resolved, so both spellings are compared.
type resolvedPath struct {
	lexical string
	real    string
	ok      bool // real is trustworthy (no dangling or looping symlink)
}

func newPathRules(a AllowRules) pathRules {
	r := pathRules{base: a.Base}
	for _, d := range a.EditUnder {
		r.editUnder = append(r.editUnder, r.resolve(d))
	}
	for _, p := range a.Protected {
		r.prot = append(r.prot, r.resolve(p))
	}
	for _, p := range a.EditDeny {
		r.editDeny = append(r.editDeny, r.resolve(p))
	}
	return r
}

// resolve makes p absolute (relative to the base, else the process's working
// directory), cleans it and resolves symlinks of its longest existing prefix.
func (r pathRules) resolve(p string) resolvedPath {
	if !filepath.IsAbs(p) {
		if r.base != "" {
			p = filepath.Join(r.base, p)
		} else if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	p = filepath.Clean(p)
	real, ok := evalExistingPrefix(p)
	return resolvedPath{lexical: p, real: real, ok: ok}
}

// evalExistingPrefix resolves symlinks of the longest existing prefix of p and
// appends the rest unchanged, so a macOS /var -> /private/var link does not
// break the comparison for a file that does not exist yet. ok is false when a
// component exists but cannot be resolved (a dangling or looping symlink): such
// a path may point anywhere.
func evalExistingPrefix(p string) (string, bool) {
	var rest []string
	for cur := p; ; {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			slices.Reverse(rest)
			return filepath.Join(append([]string{real}, rest...)...), true
		}
		if _, lerr := os.Lstat(cur); lerr == nil {
			return p, false
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, true
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// within reports whether p equals dir or lies below it.
func within(dir, p string) bool {
	if p == dir {
		return true
	}
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	return strings.HasPrefix(p, dir)
}

// editAllowed reports whether the call names at least one path and every path
// lies inside one of the allow.edit_under directories. It returns the index
// and directory that cover the first path.
func (r pathRules) editAllowed(paths []string) (idx int, dir string, ok bool) {
	if len(paths) == 0 {
		return 0, "", false
	}
	for n, raw := range paths {
		p := r.resolve(raw)
		if !p.ok {
			return 0, "", false
		}
		found := false
		for i, d := range r.editUnder {
			// The comparison is on resolved paths, so a symlink inside the
			// directory that points out of it does not count as inside.
			if d.ok && within(d.real, p.real) {
				found = true
				if n == 0 {
					idx, dir = i, d.lexical
				}
				break
			}
		}
		if !found {
			return 0, "", false
		}
	}
	return idx, dir, true
}

// denied returns the first edit_deny path (as configured) that contains a path of the call.
func (r pathRules) denied(paths []string) (string, bool) {
	for _, raw := range paths {
		p := r.resolve(raw)
		for _, d := range r.editDeny {
			if within(d.lexical, p.lexical) || within(d.real, p.real) {
				return d.lexical, true
			}
		}
	}
	return "", false
}

// protected returns the first protected path (as configured) that contains a
// path of the call.
func (r pathRules) protected(paths []string) (string, bool) {
	for _, raw := range paths {
		p := r.resolve(raw)
		for _, d := range r.prot {
			if within(d.lexical, p.lexical) || within(d.real, p.real) {
				return d.lexical, true
			}
		}
	}
	return "", false
}

// pathKeys are the raw-input keys holding one target path.
var pathKeys = []string{"file_path", "filePath", "path", "notebook_path", "target_file", "file", "old_path", "new_path", "source", "destination"}

// ToolPaths collects the target paths of a tool call, deduplicated in a stable
// order: ACP locations first, then diff content items, then paths named in the
// raw input (file_path, path, paths, changes, files, old_path/new_path, ...).
func ToolPaths(rawInput any, content json.RawMessage, locations []string) []string {
	var out []string
	add := func(p string) {
		if p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	for _, l := range locations {
		add(l)
	}
	var items []struct {
		Type string `json:"type"`
		Path string `json:"path"`
	}
	if json.Unmarshal(content, &items) == nil {
		for _, it := range items {
			if it.Type == "diff" {
				add(it.Path)
			}
		}
	}
	in, ok := rawInput.(map[string]any)
	if !ok {
		return out
	}
	for _, k := range pathKeys {
		if s, ok := in[k].(string); ok {
			add(s)
		}
	}
	for _, k := range []string{"paths", "changes", "files"} {
		addPathList(in[k], add)
	}
	return out
}

// addPathList adds the paths of a list-like raw-input value: an array of
// strings or of objects with a path, or an object keyed by path.
func addPathList(v any, add func(string)) {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			add(k)
		}
	case []any:
		for _, el := range v {
			switch el := el.(type) {
			case string:
				add(el)
			case map[string]any:
				for _, k := range pathKeys {
					if s, ok := el[k].(string); ok {
						add(s)
					}
				}
			}
		}
	}
}

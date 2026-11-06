package spec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// gitignoreContent keeps runtime data and secrets out of a git-versioned home
// dir (REQUIREMENTS §3).
const gitignoreContent = "# hal9001 runtime data and secrets\ndata/\nenv\nenv.d/\n"

// ignoredLines are the entries .gitignore must contain.
var ignoredLines = []string{"data/", "env", "env.d/"}

// Init creates the home dir layout of REQUIREMENTS §3 (sensors/, flows/,
// prompts/, data/blobs, data/state, data/worktrees) and, only when the home is
// new (config.yaml does not exist yet), copies the starter files from seed into
// it. For an existing home Init never copies starters, so deleted ones stay
// deleted; use SeedStarters to add them explicitly. seed mirrors the home
// layout at its root (config.yaml, sensors/..., prompts/...); it may be nil.
// Files under data/ in the seed are ignored. A .gitignore excluding data/, env
// and env.d/ is added if absent; an existing one gets any of those lines it
// lacks appended.
//
// Init returns the home-relative paths of the files it created.
func Init(home string, seed fs.FS) ([]string, error) {
	_, statErr := os.Stat(filepath.Join(home, FileConfig))
	fresh := errors.Is(statErr, fs.ErrNotExist)
	for _, d := range []string{"", DirSensors, DirTools, DirFlows, DirPrompts, DirBlobs, DirState, DirWorktrees} {
		if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(d)), 0o750); err != nil {
			return nil, fmt.Errorf("init %s: %w", home, err)
		}
	}
	var created []string
	if fresh {
		var err error
		created, err = SeedStarters(home, seed, nil)
		if err != nil {
			return created, err
		}
	}
	return initRest(home, created)
}

// Starters lists the home-relative, slash-separated paths of the starter files
// in seed, sorted. Files under data/ and .DS_Store are not starters.
func Starters(seed fs.FS) ([]string, error) {
	var out []string
	if seed == nil {
		return nil, nil
	}
	err := fs.WalkDir(seed, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == DirData {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == ".DS_Store" || !d.Type().IsRegular() {
			return nil
		}
		out = append(out, p)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// SeedStarters copies starters from seed into home without overwriting
// existing files. names selects starters by home-relative path or by base name
// without extension (e.g. "demo" or "flows/demo.yaml"); empty names means all.
// It returns the paths it created and errors on a name matching no starter.
func SeedStarters(home string, seed fs.FS, names []string) ([]string, error) {
	all, err := Starters(seed)
	if err != nil {
		return nil, fmt.Errorf("seed %s: %w", home, err)
	}
	sel := all
	if len(names) > 0 {
		sel = nil
		for _, n := range names {
			var match []string
			for _, p := range all {
				base := filepath.Base(p)
				if p == n || base == n || strings.TrimSuffix(base, filepath.Ext(base)) == n {
					match = append(match, p)
				}
			}
			if len(match) == 0 {
				return nil, fmt.Errorf("no starter named %q", n)
			}
			sel = append(sel, match...)
		}
	}
	var created []string
	for _, p := range sel {
		data, err := fs.ReadFile(seed, p)
		if err != nil {
			return created, fmt.Errorf("seed %s: %w", home, err)
		}
		made, err := createIfMissing(home, p, data)
		if made {
			created = append(created, p)
		}
		if err != nil {
			return created, fmt.Errorf("seed %s: %w", home, err)
		}
	}
	return created, nil
}

func initRest(home string, created []string) ([]string, error) {
	if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(DirSensorEnv)), 0o700); err != nil {
		return nil, fmt.Errorf("init %s: %w", home, err)
	}
	if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(DirToolEnv)), 0o700); err != nil {
		return nil, fmt.Errorf("init %s: %w", home, err)
	}
	made, err := createIfMissing(home, ".gitignore", []byte(gitignoreContent))
	if made {
		created = append(created, ".gitignore")
	}
	if err == nil && !made {
		err = ensureIgnored(filepath.Join(home, ".gitignore"))
	}
	if err != nil {
		return created, fmt.Errorf("init %s: %w", home, err)
	}
	sort.Strings(created)
	return created, nil
}

// createIfMissing writes data to home/rel (rel is slash-separated) unless it
// already exists. made reports whether the file was created.
func createIfMissing(home, rel string, data []byte) (made bool, err error) {
	dst := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return false, err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // dst is a seed path under home
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(dst)
		return false, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(dst)
		return false, err
	}
	return true, nil
}

// ensureIgnored appends the entries of ignoredLines that the existing
// .gitignore at path lacks, leaving everything else as the user wrote it.
func ensureIgnored(path string) error {
	b, err := os.ReadFile(path) //nolint:gosec // fixed name under home
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		have[strings.TrimPrefix(strings.TrimSpace(l), "/")] = true
	}
	var add strings.Builder
	for _, l := range ignoredLines {
		if !have[l] {
			add.WriteString(l + "\n")
		}
	}
	if add.Len() == 0 {
		return nil
	}
	out := string(b)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out+add.String()), 0o600) //nolint:gosec // fixed name under home
}

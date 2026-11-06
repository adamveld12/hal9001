package spec

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultDebounce is how long Watch waits for further changes before
// reporting.
const DefaultDebounce = 150 * time.Millisecond

// Reload reports that configuration files changed. Paths are relative to the
// home dir, slash-separated, sorted and unique (for example "flows/a.yaml",
// "config.yaml"); a deleted file is reported like a changed one. A path that
// is only a directory name ("flows") means the directory itself appeared or
// disappeared.
type Reload struct {
	Paths []string `json:"paths"`
}

// WatchOption configures Watch.
type WatchOption func(*watchConfig)

type watchConfig struct{ debounce time.Duration }

// WithDebounce sets the quiet period before a Reload is delivered.
func WithDebounce(d time.Duration) WatchOption {
	return func(c *watchConfig) { c.debounce = d }
}

// ClassifyPath maps a home-relative path from Reload.Paths to its kind and id.
// ok is false for paths that are not configuration files.
func ClassifyPath(rel string) (k Kind, id string, ok bool) {
	switch rel {
	case FileConfig:
		return KindConfig, "", true
	case FileEnv:
		return KindEnv, "", true
	}
	if name, ok := strings.CutPrefix(rel, DirSensorEnv+"/"); ok {
		if !strings.Contains(name, "/") && !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".env") {
			return KindSensorEnv, strings.TrimSuffix(name, ".env"), true
		}
		return "", "", false
	}
	if name, ok := strings.CutPrefix(rel, DirSandboxEnv+"/"); ok {
		if !strings.Contains(name, "/") && !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".env") {
			return KindSandboxEnv, strings.TrimSuffix(name, ".env"), true
		}
		return "", "", false
	}
	dir, name, found := strings.Cut(rel, "/")
	if !found || strings.Contains(name, "/") {
		return "", "", false
	}
	for _, c := range []struct {
		dir  string
		kind Kind
		exts []string
	}{
		{DirSensors, KindSensor, []string{".yaml", ".yml"}},
		{DirFlows, KindFlow, []string{".yaml", ".yml"}},
		{DirPrompts, KindPrompt, []string{".md"}},
		{DirSandboxes, KindSandbox, []string{".yaml"}},
	} {
		if dir != c.dir || strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") {
			continue
		}
		ext := filepath.Ext(name)
		for _, e := range c.exts {
			if ext == e {
				return c.kind, strings.TrimSuffix(name, ext), true
			}
		}
	}
	return "", "", false
}

var watchedDirs = []string{DirSensors, DirFlows, DirPrompts, DirSensorEnv, DirSandboxes, DirSandboxEnv}

// Watch watches config.yaml, env, sensors/, flows/ and prompts/ under home and
// delivers debounced Reload notifications until ctx is done, when the
// returned channel is closed. Directories that do not exist yet are picked up
// when they are created. Temporary files (names starting with "." or ending
// in "~") are ignored. home must exist.
func Watch(ctx context.Context, home string, opts ...WatchOption) (<-chan Reload, error) {
	cfg := watchConfig{debounce: DefaultDebounce}
	for _, o := range opts {
		o(&cfg)
	}
	home = filepath.Clean(home)
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch %s: %w", home, err)
	}
	if err := w.Add(home); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("watch %s: %w", home, err)
	}
	for _, d := range watchedDirs {
		if err := w.Add(filepath.Join(home, d)); err != nil && !os.IsNotExist(err) {
			_ = w.Close()
			return nil, fmt.Errorf("watch %s: %w", filepath.Join(home, d), err)
		}
	}
	out := make(chan Reload)
	go watchLoop(ctx, w, home, cfg.debounce, out)
	return out, nil
}

func watchLoop(ctx context.Context, w *fsnotify.Watcher, home string, debounce time.Duration, out chan<- Reload) {
	defer close(out)
	defer func() { _ = w.Close() }()

	pending := map[string]struct{}{}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	ready := false // debounce elapsed; pending can be delivered

	for {
		var send chan<- Reload
		var msg Reload
		if ready && len(pending) > 0 {
			send = out
			msg = Reload{Paths: sortedSet(pending)}
		}
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Op == fsnotify.Chmod {
				continue
			}
			rel, err := filepath.Rel(home, ev.Name)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			changed := false
			if isWatchedDir(rel) {
				// A watched directory appeared or vanished: watch it and
				// report the files already inside.
				pending[rel] = struct{}{}
				changed = true
				if ev.Has(fsnotify.Create) {
					if err := w.Add(ev.Name); err != nil {
						slog.Warn("spec: watch directory", "dir", ev.Name, "err", err)
					}
					addExisting(pending, home, rel)
				}
			} else if _, _, ok := ClassifyPath(rel); ok {
				pending[rel] = struct{}{}
				changed = true
			}
			if changed {
				ready = false
				timer.Reset(debounce)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			slog.Warn("spec: watcher error", "err", err)
		case <-timer.C:
			ready = true
		case send <- msg:
			clear(pending)
			ready = false
		}
	}
}

func isWatchedDir(rel string) bool {
	for _, d := range watchedDirs {
		if rel == d {
			return true
		}
	}
	return false
}

func addExisting(pending map[string]struct{}, home, dir string) {
	entries, err := os.ReadDir(filepath.Join(home, dir))
	if err != nil {
		return
	}
	for _, e := range entries {
		if rel := dir + "/" + e.Name(); !e.IsDir() {
			if _, _, ok := ClassifyPath(rel); ok {
				pending[rel] = struct{}{}
			}
		}
	}
}

func sortedSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

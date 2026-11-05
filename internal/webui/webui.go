// Package webui serves the built single-page app: the files of web/dist, with
// index.html as the fallback for client-side routes (REQUIREMENTS §10.1, §11).
//
// The UI comes from one of, in order:
//  1. Options.Dir, a directory with a built UI (flag/env override, for dev);
//  2. the UI embedded in the binary (build tag `embedui`, see web/embed.go);
//  3. ./web/dist when it exists (running from a checkout).
//
// Without any of them the handler answers 503 with instructions, so the
// daemon, the API and the hooks never need a built UI.
package webui

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const indexFile = "index.html"

// DevDir is the checkout-relative directory used when nothing else is given.
const DevDir = "web/dist"

// Options configures New.
type Options struct {
	// Dir is a directory containing a built UI. When set it wins over the
	// embedded UI, and it must contain index.html.
	Dir string
}

// Handler serves the UI. It implements http.Handler.
type Handler struct {
	fsys   fs.FS
	source string
}

// New resolves the UI source. It fails only when Options.Dir is set but
// unusable.
func New(opts Options) (*Handler, error) {
	if opts.Dir != "" {
		dir, err := filepath.Abs(opts.Dir)
		if err != nil {
			return nil, fmt.Errorf("webui: %w", err)
		}
		if _, err := os.Stat(filepath.Join(dir, indexFile)); err != nil {
			return nil, fmt.Errorf("webui: %s has no %s (run `pnpm --dir web build`): %w", dir, indexFile, err)
		}
		return &Handler{fsys: os.DirFS(dir), source: dir}, nil
	}
	if fsys, ok := embedded(); ok {
		return &Handler{fsys: fsys, source: "embedded"}, nil
	}
	if dir, err := filepath.Abs(DevDir); err == nil {
		if _, err := os.Stat(filepath.Join(dir, indexFile)); err == nil {
			return &Handler{fsys: os.DirFS(dir), source: dir}, nil
		}
	}
	return &Handler{}, nil
}

// Source describes where the UI comes from: "embedded", a directory, or "" when
// no UI is available.
func (h *Handler) Source() string { return h.source }

// Available reports whether a built UI is being served.
func (h *Handler) Available() bool { return h.fsys != nil }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "api" || strings.HasPrefix(name, "api/") {
		http.NotFound(w, r)
		return
	}
	if h.fsys == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "The web UI is not built. Run `pnpm --dir web build` (or `mise run build`), then restart hal9001, or point --web-dir at a built UI.\nThe API under /api/ is available.\n")
		return
	}
	if name == "" {
		name = indexFile
	}
	if isFile(h.fsys, name) {
		h.serve(w, r, name)
		return
	}
	// A missing hashed asset must be a 404: answering with index.html would be
	// parsed as JavaScript or CSS and hide the stale-build problem.
	if strings.HasPrefix(name, "assets/") {
		http.NotFound(w, r)
		return
	}
	h.serve(w, r, indexFile)
}

func isFile(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && st.Mode().IsRegular()
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request, name string) {
	f, err := h.fsys.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(data)
	}
	hdr := w.Header()
	hdr.Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(name, "assets/") {
		// Vite names these by content hash; a changed file has a new name.
		hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		hdr.Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, st.ModTime(), rs)
}

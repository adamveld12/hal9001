package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"hal9001/internal/apispec"
	"hal9001/internal/cli"
)

// Canned IDs that match the contract's patterns.
const (
	id1  = "01JC7Z3K9Q8V5T2M4N6P8R0S1T"
	id2  = "01JC7Z3K9Q8V5T2M4N6P8R0S2V"
	id3  = "01JC7Z3K9Q8V5T2M4N6P8R0S3W"
	hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ts   = "2026-11-05T12:00:00.123456789Z"
)

// seen is one request the fake daemon received.
type seen struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   string
}

// canned is a fixed response.
type canned struct {
	status int
	body   any
	ctype  string
	// location is the Location header of 201/202 responses.
	location string
}

// fakeDaemon serves canned, contract-checked responses and records requests.
// Every response is validated against api/openapi.yaml before it is sent, so a
// fixture that drifts from the contract fails the test that uses it.
type fakeDaemon struct {
	t    *testing.T
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []seen
	// routes maps "METHOD /path" to responses served in order; the last one repeats.
	routes map[string][]canned
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	f := &fakeDaemon{t: t, routes: map[string][]canned{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// on registers responses for "METHOD /path".
func (f *fakeDaemon) on(route string, resp ...canned) { f.routes[route] = resp }

func ok(body any) canned { return canned{status: 200, body: body} }

func problem(status int, class, title, detail string, extra map[string]any) canned {
	b := map[string]any{"type": "urn:hal9001:problem:" + class, "title": title, "status": status, "detail": detail}
	for k, v := range extra {
		b[k] = v
	}
	return canned{status: status, body: b, ctype: "application/problem+json"}
}

func (f *fakeDaemon) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, seen{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: string(body)})
	key := r.Method + " " + r.URL.Path
	list := f.routes[key]
	var c canned
	if len(list) > 0 {
		c = list[0]
		if len(list) > 1 {
			f.routes[key] = list[1:]
		}
	}
	f.mu.Unlock()
	if len(list) == 0 {
		http.Error(w, "no canned response for "+key, http.StatusTeapot)
		f.t.Errorf("unexpected request %s", key)
		return
	}

	payload, err := json.Marshal(c.body)
	if err != nil {
		f.t.Errorf("marshal canned body: %v", err)
	}
	ctype := c.ctype
	if ctype == "" {
		ctype = "application/json"
	}
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", ctype)
	if c.location != "" {
		rec.Header().Set("Location", c.location)
	}
	rec.WriteHeader(c.status)
	if c.status != http.StatusNoContent {
		_, _ = rec.Write(payload)
	}
	apispec.ValidateResponse(f.t, &http.Request{Method: r.Method, URL: &url.URL{Path: r.URL.Path}}, rec.Result())

	w.Header().Set("Content-Type", ctype)
	if c.location != "" {
		w.Header().Set("Location", c.location)
	}
	w.WriteHeader(c.status)
	if c.status != http.StatusNoContent {
		_, _ = w.Write(payload)
	}
}

func (f *fakeDaemon) requests() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.reqs...)
}

// only returns the single request the daemon received.
func (f *fakeDaemon) only() seen {
	f.t.Helper()
	r := f.requests()
	if len(r) != 1 {
		f.t.Fatalf("daemon got %d requests, want 1: %+v", len(r), r)
	}
	return r[0]
}

// result is the outcome of one CLI invocation.
type result struct {
	code   int
	stdout string
	stderr string
}

// runOpts adjusts the CLI environment of one invocation.
type runOpts struct {
	stdin string
	env   map[string]string
	hook  func(*cli.Env)
}

func run(t *testing.T, opts runOpts, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	env := cli.Env{
		Stdin:   strings.NewReader(opts.stdin),
		Stdout:  &out,
		Stderr:  &errb,
		Version: "9.9.9-test",
		Getenv:  func(k string) string { return opts.env[k] },
	}
	if opts.hook != nil {
		opts.hook(&env)
	}
	code := cli.Run(context.Background(), args, env)
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

// api runs the CLI against the fake daemon.
func (f *fakeDaemon) run(opts runOpts, args ...string) result {
	f.t.Helper()
	if opts.env == nil {
		opts.env = map[string]string{}
	}
	opts.env[cli.EnvURL] = f.srv.URL
	return run(f.t, opts, args...)
}

// jsonEqual reports whether two JSON documents are equal ignoring formatting.
func jsonEqual(t *testing.T, got string, want any) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Errorf("output is not JSON: %v\n%s", err, got)
		return false
	}
	wb, _ := json.Marshal(want)
	_ = json.Unmarshal(wb, &w)
	gb, _ := json.Marshal(g)
	wb, _ = json.Marshal(w)
	return string(gb) == string(wb)
}

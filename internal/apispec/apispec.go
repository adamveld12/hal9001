// Package apispec loads the OpenAPI contract in api/openapi.yaml and answers questions about it: which auth service
// guards an operation, and whether a response conforms to the contract.
//
// The document is parsed once and shared. Callers must treat it as read-only.
package apispec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"

	"hal9001/api"
)

// Extension names used in the contract.
const (
	// ExtService names the auth service that guards an operation: ui, api, approve, or admin.
	ExtService = "x-hal-service"
	// ExtPublic marks operations reachable without authentication (login, whoami, health).
	ExtPublic = "x-hal-public"
	// ExtPaused describes behaviour while the kill switch is engaged: "reject" (423) or "queue".
	ExtPaused = "x-hal-paused"
	// ExtSSEEvents maps each SSE event type to the component schema of its envelope.
	ExtSSEEvents = "x-hal-sse-events"
)

// Services are the valid values of ExtService.
var Services = []string{"ui", "api", "approve", "admin"}

type loaded struct {
	doc    *openapi3.T
	router routers.Router
	err    error
}

var (
	once   sync.Once
	cached loaded
)

func load() loaded {
	once.Do(func() {
		loader := openapi3.NewLoader()
		doc, err := loader.LoadFromData(api.OpenAPI)
		if err != nil {
			cached.err = fmt.Errorf("apispec: parse openapi.yaml: %w", err)
			return
		}
		if err := doc.Validate(context.Background()); err != nil {
			cached.err = fmt.Errorf("apispec: invalid openapi.yaml: %w", err)
			return
		}
		// The router matches on path only; the contract's server URL is a documentation default.
		routable := *doc
		routable.Servers = nil
		router, err := legacy.NewRouter(&routable)
		if err != nil {
			cached.err = fmt.Errorf("apispec: build router: %w", err)
			return
		}
		cached = loaded{doc: doc, router: router}
	})
	return cached
}

// Raw returns the embedded openapi.yaml.
func Raw() []byte { return api.OpenAPI }

// Load returns the parsed and validated contract. The result is shared; do not modify it.
func Load() (*openapi3.T, error) {
	l := load()
	return l.doc, l.err
}

// MustLoad is Load that panics on error. The document is embedded, so an error is a build defect.
func MustLoad() *openapi3.T {
	doc, err := Load()
	if err != nil {
		panic(err)
	}
	return doc
}

// FindOperation returns the contract operation for an HTTP method and URL path (no query string), with the
// path parameters it matched. It returns nil when the contract has no such operation.
func FindOperation(method, path string) (*openapi3.Operation, map[string]string) {
	l := load()
	if l.err != nil {
		return nil, nil
	}
	route, params, err := l.router.FindRoute(&http.Request{Method: method, URL: urlOf(path)})
	if err != nil {
		return nil, nil
	}
	return route.Operation, params
}

// ServiceFor returns the auth service (ui, api, approve, admin) that guards method and path, from the operation's
// x-hal-service. GET and HEAD requests for paths outside /api/ that match no operation are the embedded web UI
// (static assets and client-side routes) and belong to "ui". It returns "" for anything else the contract does
// not describe; callers should treat that as not found, not as unguarded.
func ServiceFor(method, path string) string {
	if op, _ := FindOperation(method, path); op != nil {
		if s, ok := op.Extensions[ExtService].(string); ok {
			return s
		}
		return ""
	}
	path = stripQuery(path)
	if (method == http.MethodGet || method == http.MethodHead) && !strings.HasPrefix(path, "/api/") && path != "/api" {
		return "ui"
	}
	return ""
}

// IsPublic reports whether the operation for method and path is marked x-hal-public: reachable without a session.
func IsPublic(method, path string) bool {
	op, _ := FindOperation(method, path)
	if op == nil {
		return false
	}
	b, _ := op.Extensions[ExtPublic].(bool)
	return b
}

// TB is the part of testing.TB that ValidateResponse needs.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
}

// ValidateResponse reports a test error unless resp is a response the contract allows for req: the operation
// exists, the status is documented (or covered by default), the content type is one of the documented ones, and a
// JSON body matches the schema. req must carry the method and URL that produced resp. resp.Body is read and
// replaced, so the caller can still read it.
//
// Event streams, NDJSON, and binary downloads are checked for status and content type only.
func ValidateResponse(t TB, req *http.Request, resp *http.Response) {
	t.Helper()
	l := load()
	if l.err != nil {
		t.Errorf("apispec: %v", l.err)
		return
	}
	route, params, err := l.router.FindRoute(req)
	if err != nil {
		t.Errorf("apispec: %s %s is not in the contract: %v", req.Method, req.URL.Path, err)
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Errorf("apispec: read response body: %v", err)
		return
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))

	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
		Status:                 resp.StatusCode,
		Header:                 resp.Header,
		Body:                   io.NopCloser(bytes.NewReader(body)),
		Options: &openapi3filter.Options{
			IncludeResponseStatus:   true,
			ExcludeResponseBody:     !isJSON(ct),
			SchemaValidationOptions: []openapi3.SchemaValidationOption{openapi3.EnableJSONSchema2020()},
		},
	}
	if err := openapi3filter.ValidateResponse(context.Background(), in); err != nil {
		t.Errorf("apispec: %s %s -> %d does not match the contract (operation %s): %v",
			req.Method, req.URL.Path, resp.StatusCode, route.Operation.OperationID, err)
		return
	}
	if !isJSON(ct) && len(body) > 0 {
		if !documentsContentType(route.Operation, resp.StatusCode, ct) {
			t.Errorf("apispec: %s %s -> %d: content type %q is not documented (operation %s)",
				req.Method, req.URL.Path, resp.StatusCode, ct, route.Operation.OperationID)
		}
	}
}

func isJSON(ct string) bool {
	return ct == "application/json" || strings.HasSuffix(ct, "+json")
}

// documentsContentType reports whether the operation lists ct for status (or for default).
func documentsContentType(op *openapi3.Operation, status int, ct string) bool {
	resp := op.Responses.Status(status)
	if resp == nil {
		resp = op.Responses.Default()
	}
	if resp == nil || resp.Value == nil {
		return false
	}
	for have := range resp.Value.Content {
		if have == ct || have == "*/*" {
			return true
		}
	}
	return false
}

func stripQuery(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		return p[:i]
	}
	return p
}

func urlOf(p string) *url.URL { return &url.URL{Path: stripQuery(p)} }

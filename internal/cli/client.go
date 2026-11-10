package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"hal9001/internal/auth"
)

// DefaultURL is where the daemon listens (REQUIREMENTS §10.3).
const DefaultURL = "http://127.0.0.1:9001"

// maxResponse bounds how much of a response the CLI reads.
const maxResponse = 64 << 20

// client is a minimal JSON-over-HTTP client for the hal9001 API. It adds no
// behavior of its own beyond the baseline's requirements (JSON content type on
// every mutating request) and the session cookie.
type client struct {
	base   string
	cookie string
	hc     *http.Client
	// headers are added to every request (for example the assist agent marker).
	headers http.Header
}

type response struct {
	Status int
	Body   []byte
}

func newClient(base, sessionCookie string, hc *http.Client) *client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &client{base: strings.TrimRight(base, "/"), cookie: cookieHeader(sessionCookie), hc: hc}
}

// cookieHeader accepts either a bare value or a full "name=value" pair.
func cookieHeader(v string) string {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return ""
	case strings.Contains(v, "="):
		return v
	}
	return auth.SessionCookie + "=" + v
}

// do sends a request. A non-GET request always carries
// Content-Type: application/json (the baseline rejects it otherwise), with
// "{}" as the body when none is given. Responses with status >= 400 come back
// as *problemError.
func (c *client) do(ctx context.Context, method, path string, query url.Values, body []byte) (*response, error) {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if method != http.MethodGet && method != http.MethodHead {
		if body == nil {
			body = []byte("{}")
		}
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	for k, v := range c.headers {
		req.Header[k] = v
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, &unreachableError{url: c.base, err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, newProblemError(resp.StatusCode, resp.Header.Get("Content-Type"), data)
	}
	return &response{Status: resp.StatusCode, Body: data}, nil
}

// unreachableError reports that the daemon could not be contacted.
type unreachableError struct {
	url string
	err error
}

func (e *unreachableError) Error() string {
	return fmt.Sprintf("cannot reach the daemon at %s: %v (is it running? try `hal9001 serve`, or `hal9001 service status` for a headless service, or the HAL9001 app's menu)", e.url, e.err)
}

func (e *unreachableError) Unwrap() error { return e.err }

// problemError is an application/problem+json (or other non-2xx) response.
type problemError struct {
	Status int
	Type   string
	Title  string
	Detail string
	// Raw is the response body.
	Raw []byte
	// IsJSON reports whether Raw is a JSON document.
	IsJSON bool
	// Diagnostics are spec diagnostics from a validation-failed problem, as lines.
	Diagnostics []string
}

func newProblemError(status int, contentType string, body []byte) *problemError {
	p := &problemError{Status: status, Raw: body}
	var doc struct {
		Type        string `json:"type"`
		Title       string `json:"title"`
		Detail      string `json:"detail"`
		Diagnostics []struct {
			File     string `json:"file"`
			Line     int    `json:"line"`
			Path     string `json:"path"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if json.Valid(body) && json.Unmarshal(body, &doc) == nil {
		p.IsJSON = true
		p.Type, p.Title, p.Detail = doc.Type, doc.Title, doc.Detail
		for _, d := range doc.Diagnostics {
			loc := d.File
			if d.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, d.Line)
			}
			if d.Path != "" {
				loc = strings.TrimSpace(loc + " " + d.Path)
			}
			p.Diagnostics = append(p.Diagnostics, fmt.Sprintf("%s: %s: %s", loc, d.Severity, d.Message))
		}
	}
	if p.Title == "" {
		p.Title = http.StatusText(status)
		if !p.IsJSON && strings.TrimSpace(string(body)) != "" {
			p.Detail = strings.TrimSpace(string(body))
		}
	}
	_ = contentType
	return p
}

// Class returns the short problem class, e.g. "not-found" for
// urn:hal9001:problem:not-found.
func (p *problemError) Class() string {
	if i := strings.LastIndex(p.Type, ":"); i >= 0 {
		return p.Type[i+1:]
	}
	return p.Type
}

// Summary is the one-line human form.
func (p *problemError) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s", p.Status, p.Title)
	if c := p.Class(); c != "" {
		fmt.Fprintf(&b, " (%s)", c)
	}
	if p.Detail != "" {
		b.WriteString(": " + p.Detail)
	}
	return b.String()
}

func (p *problemError) Error() string { return p.Summary() }

// asProblem returns err as a *problemError, if it is one.
func asProblem(err error) (*problemError, bool) {
	var p *problemError
	if errors.As(err, &p) {
		return p, true
	}
	return nil, false
}

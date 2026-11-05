// Package httpsec enforces the network baseline of REQUIREMENTS §10.3: Host
// allow-list (DNS-rebinding protection), Origin check and JSON-only mutating
// requests (CSRF protection). It never grants CORS. It is not pluggable.
package httpsec

import (
	"encoding/json"
	"net/http"
)

// Problem type URIs. Each is a stable identifier for one error class.
const (
	ProblemBase = "urn:hal9001:problem:"

	TypeHostNotAllowed     = ProblemBase + "host-not-allowed"
	TypeOriginNotAllowed   = ProblemBase + "origin-not-allowed"
	TypeUnsupportedMedia   = ProblemBase + "unsupported-media-type"
	TypeUnauthenticated    = ProblemBase + "unauthenticated"
	TypeForbidden          = ProblemBase + "forbidden"
	TypeBadRequest         = ProblemBase + "bad-request"
	TypeNotFound           = ProblemBase + "not-found"
	TypeInternal           = ProblemBase + "internal"
	TypeLoginExpired       = ProblemBase + "login-expired"
	TypeAuthenticationFail = ProblemBase + "authentication-failed"
)

// Problem is an RFC 9457 problem document. Extensions are merged into the
// top-level JSON object (RFC 9457 §3.2).
type Problem struct {
	Type       string
	Title      string
	Status     int
	Detail     string
	Extensions map[string]any
}

func (p Problem) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(p.Extensions)+4)
	for k, v := range p.Extensions {
		m[k] = v
	}
	m["type"] = p.Type
	m["title"] = p.Title
	m["status"] = p.Status
	if p.Detail != "" {
		m["detail"] = p.Detail
	}
	return json.Marshal(m)
}

// WriteProblem writes p as application/problem+json with p.Status.
func WriteProblem(w http.ResponseWriter, p Problem) {
	body, err := json.Marshal(p)
	if err != nil {
		// Extensions held an unmarshalable value; fall back to the bare problem.
		body, _ = json.Marshal(Problem{Type: TypeInternal, Title: "Internal Server Error", Status: http.StatusInternalServerError})
		p.Status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_, _ = w.Write(body)
}

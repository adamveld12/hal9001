package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"hal9001/internal/spec"
	"hal9001/internal/store"
)

const (
	defaultLimit = 50
	maxLimit     = 200

	maxWait = 5 * time.Minute

	// bodyLimit bounds ordinary JSON request bodies; injectLimit bounds the
	// event payload of POST /api/events.
	bodyLimit   = 1 << 20
	injectLimit = 8 << 20
)

var (
	ulidRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// query collects the errors found while reading query parameters so a request
// with several bad parameters reports all of them at once.
type query struct {
	v    url.Values
	errs []string
}

func newQuery(r *http.Request) *query { return &query{v: r.URL.Query()} }

func (q *query) fail(format string, args ...any) {
	q.errs = append(q.errs, fmt.Sprintf(format, args...))
}

// ok writes a 400 problem and returns false when any parameter was invalid.
func (q *query) ok(w http.ResponseWriter) bool {
	if len(q.errs) == 0 {
		return true
	}
	badRequest(w, "%s", strings.Join(q.errs, "; "))
	return false
}

func (q *query) str(name string) string { return q.v.Get(name) }

// enum returns the value of name, which must be one of allowed.
func (q *query) enum(name string, allowed ...string) string {
	v := q.v.Get(name)
	if v != "" && !slices.Contains(allowed, v) {
		q.fail("%s must be one of %s", name, strings.Join(allowed, ", "))
		return ""
	}
	return v
}

func (q *query) ulid(name string) string {
	v := q.v.Get(name)
	if v != "" && !ulidRE.MatchString(v) {
		q.fail("%s must be a ULID", name)
		return ""
	}
	return v
}

func (q *query) slug(name string) string {
	v := q.v.Get(name)
	if v != "" && !spec.ValidID(v) {
		q.fail("%s must be an id of letters, digits, - and _ (max 64)", name)
		return ""
	}
	return v
}

func (q *query) boolp(name string) *bool {
	v := q.v.Get(name)
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil || (v != "true" && v != "false") {
		q.fail("%s must be true or false", name)
		return nil
	}
	return &b
}

// list reads a comma-separated parameter.
func (q *query) list(name string) []string {
	v := q.v.Get(name)
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// limit reads the page size.
func (q *query) limit() int {
	if v := q.v.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLimit {
			q.fail("limit must be an integer from 1 to %d", maxLimit)
			return defaultLimit
		}
		return n
	}
	return defaultLimit
}

// page reads cursor and limit for the store's ULID cursors.
func (q *query) page() store.ListOpts {
	o := store.ListOpts{Limit: q.limit()}
	if c := q.v.Get("cursor"); c != "" {
		if !ulidRE.MatchString(c) {
			q.fail("cursor is not a cursor returned by this API")
		} else {
			o.Cursor = c
		}
	}
	return o
}

// timeRange reads since (inclusive) and until (exclusive).
func (q *query) timeRange() store.TimeRange {
	var tr store.TimeRange
	for name, dst := range map[string]*time.Time{"since": &tr.Since, "until": &tr.Until} {
		v := q.v.Get(name)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			q.fail("%s must be an RFC 3339 time", name)
			continue
		}
		*dst = t.UTC()
	}
	return tr
}

// wait reads the long-poll duration, capped at maxWait.
func (q *query) wait(def time.Duration) time.Duration {
	v := q.v.Get("wait")
	if v == "" {
		return def
	}
	d, err := spec.ParseDuration(v)
	if err != nil || d < 0 {
		q.fail("wait must be a duration such as 30s or 2m")
		return 0
	}
	return min(d, maxWait)
}

// decodeJSON reads a JSON request body into dst, rejecting unknown fields and
// trailing data. It answers 400 or 413 itself and returns false on failure.
// An empty body is accepted when optional is true (dst is left untouched).
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, optional bool) bool {
	body, ok := readBody(w, r, bodyLimit)
	if !ok {
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		if optional {
			return true
		}
		badRequest(w, "a JSON request body is required")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		badRequest(w, "invalid JSON body: %s", err.Error())
		return false
	}
	if dec.More() {
		badRequest(w, "invalid JSON body: unexpected data after the first value")
		return false
	}
	return true
}

// readBody reads at most limit bytes; a larger body is a 413.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			problem(w, http.StatusRequestEntityTooLarge, typePayloadTooBig, "Payload too large",
				fmt.Sprintf("the request body exceeds %d bytes", limit), nil)
			return nil, false
		}
		badRequest(w, "cannot read the request body: %s", err.Error())
		return nil, false
	}
	return body, true
}

// writeJSON writes v as an application/json response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		problem(w, http.StatusInternalServerError, typeInternalServer, "Internal server error", "response encoding failed", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// nextCursor maps the store's empty cursor to JSON null.
func nextCursor(c string) *string {
	if c == "" {
		return nil
	}
	return &c
}

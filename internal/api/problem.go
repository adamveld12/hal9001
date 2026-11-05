package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/httpsec"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// Problem types the contract defines that httpsec does not name.
const (
	typeConflict       = httpsec.ProblemBase + "conflict"
	typeAlreadyExists  = httpsec.ProblemBase + "already-exists"
	typeInvalidState   = httpsec.ProblemBase + "invalid-state"
	typePayloadTooBig  = httpsec.ProblemBase + "payload-too-large"
	typeValidation     = httpsec.ProblemBase + "validation-failed"
	typePaused         = httpsec.ProblemBase + "paused"
	typeUnavailable    = httpsec.ProblemBase + "unavailable"
	typeBadRequest     = httpsec.TypeBadRequest
	typeNotFound       = httpsec.TypeNotFound
	typeInternalServer = httpsec.TypeInternal
)

func problem(w http.ResponseWriter, status int, typ, title, detail string, ext map[string]any) {
	httpsec.WriteProblem(w, httpsec.Problem{Type: typ, Title: title, Status: status, Detail: detail, Extensions: ext})
}

func badRequest(w http.ResponseWriter, format string, args ...any) {
	problem(w, http.StatusBadRequest, typeBadRequest, "Bad request", fmt.Sprintf(format, args...), nil)
}

func notFound(w http.ResponseWriter, format string, args ...any) {
	problem(w, http.StatusNotFound, typeNotFound, "Not found", fmt.Sprintf(format, args...), nil)
}

// validationProblem answers 422 with the diagnostics that explain it.
func validationProblem(w http.ResponseWriter, detail string, ds []spec.Diagnostic) {
	if ds == nil {
		ds = []spec.Diagnostic{}
	}
	problem(w, http.StatusUnprocessableEntity, typeValidation, "Validation failed", detail, map[string]any{"diagnostics": ds})
}

// fieldProblem is a 422 for one invalid request field.
func fieldProblem(w http.ResponseWriter, path, message string) {
	validationProblem(w, message, []spec.Diagnostic{{
		Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: path, Message: message,
	}})
}

func conflictProblem(w http.ResponseWriter, typ, detail string, ext map[string]any) {
	title := "Conflict"
	switch typ {
	case typeAlreadyExists:
		title = "Already exists"
	case typeInvalidState:
		title = "Invalid state"
	}
	problem(w, http.StatusConflict, typ, title, detail, ext)
}

// fileConflict reports a hash mismatch (409 conflict) with the hashes involved.
func fileConflict(w http.ResponseWriter, detail, expected, current string) {
	ext := map[string]any{"expected_hash": expected, "current_hash": nil}
	if current != "" {
		ext["current_hash"] = current
	}
	conflictProblem(w, typeConflict, detail, ext)
}

// writeError maps an error from the store, spec or engine to the contract's
// problem types. Anything unrecognised is a 500 whose cause is logged, not
// returned.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *core.ValidationError
	switch {
	case errors.As(err, &verr):
		validationProblem(w, "validation failed", verr.Diagnostics)
	case errors.Is(err, store.ErrNotFound), errors.Is(err, spec.ErrNotFound), errors.Is(err, core.ErrUnknownRef):
		notFound(w, "%s", err.Error())
	case errors.Is(err, core.ErrPaused):
		ext := map[string]any{"paused_since": nil}
		if ks, kerr := s.opts.Store.GetKillSwitch(r.Context()); kerr == nil && ks.Paused && !ks.Since.IsZero() {
			ext["paused_since"] = ks.Since.UTC().Format(time.RFC3339Nano)
		}
		problem(w, http.StatusLocked, typePaused, "Paused", "the kill switch is engaged", ext)
	case errors.Is(err, core.ErrInvalidState), errors.Is(err, store.ErrConflict):
		conflictProblem(w, typeInvalidState, err.Error(), nil)
	case errors.Is(err, spec.ErrConflict):
		fileConflict(w, err.Error(), "", "")
	case errors.Is(err, spec.ErrInvalidID), errors.Is(err, store.ErrInvalid):
		badRequest(w, "%s", err.Error())
	case r.Context().Err() != nil:
		problem(w, http.StatusServiceUnavailable, typeUnavailable, "Unavailable", "the request was cancelled", nil)
	default:
		s.log().Error("api: request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		problem(w, http.StatusInternalServerError, typeInternalServer, "Internal server error", "an internal error occurred", nil)
	}
}

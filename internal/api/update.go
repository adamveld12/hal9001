package api

import (
	"errors"
	"net/http"

	"hal9001/internal/spec"
	"hal9001/internal/update"
)

// subscribeUpdate forwards the update service's events to the stream hub.
func (s *Server) subscribeUpdate() {
	if s.opts.Update == nil {
		return
	}
	s.unsubUpdate = s.opts.Update.Subscribe(func(ev update.Event) {
		s.hub.publishMsg(ev.Type, ev.Data, nil, false, ev.InstallerOnly)
	})
}

// pokeUpdate asks the update service to re-evaluate its install policy.
func (s *Server) pokeUpdate() {
	if s.opts.Update != nil {
		s.opts.Update.Poke()
	}
}

func (s *Server) getUpdate(w http.ResponseWriter, r *http.Request) {
	if s.opts.Update == nil {
		writeJSON(w, http.StatusOK, update.Status{
			DisabledReason: new("updates are not configured in this daemon"), Channel: spec.DefaultUpdateChannel, State: update.StateDisabled,
		})
		return
	}
	writeJSON(w, http.StatusOK, s.opts.Update.Status(r.Context()))
}

func (s *Server) updateUnavailable(w http.ResponseWriter) bool {
	if s.opts.Update != nil {
		return false
	}
	problem(w, http.StatusServiceUnavailable, typeUnavailable, "Unavailable", "updates are not configured in this daemon", nil)
	return true
}

func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updateUnavailable(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.opts.Update.Check(r.Context()))
}

func (s *Server) installUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updateUnavailable(w) {
		return
	}
	var req update.InstallRequest
	if !decodeJSON(w, r, &req, true) {
		return
	}
	st, err := s.opts.Update.Install(r.Context(), req.Force)
	var refused *update.InstallError
	switch {
	case errors.As(err, &refused):
		conflictProblem(w, typeInvalidState, refused.Detail, map[string]any{"current_state": refused.Reason, "release_url": refused.ReleaseURL})
	case err != nil:
		s.writeError(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, st)
	}
}

func (s *Server) reportUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updateUnavailable(w) {
		return
	}
	var req update.ReportRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	switch req.State {
	case update.PhaseDownloading, update.PhaseVerifying, update.PhaseInstalling, update.ReportFailed:
	default:
		fieldProblem(w, "state", "state must be one of downloading, verifying, installing, failed")
		return
	}
	switch err := s.opts.Update.Report(r.Context(), req.State, req.Error); {
	case errors.Is(err, update.ErrNotInstalling):
		conflictProblem(w, typeInvalidState, err.Error(), map[string]any{"current_state": "not_installing"})
	case err != nil:
		s.writeError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, s.opts.Update.Status(r.Context()))
	}
}

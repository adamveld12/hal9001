package api

import (
	"errors"
	"net/http"

	"hal9001/internal/spec"
	"hal9001/internal/tailscale"
)

// tailscaleManager answers 503 when the server has no Tailscale manager.
func (s *Server) tailscaleManager(w http.ResponseWriter) *tailscale.Manager {
	if s.opts.Tailscale == nil {
		problem(w, http.StatusServiceUnavailable, typeUnavailable, "Unavailable", "Tailscale support is not configured", nil)
		return nil
	}
	return s.opts.Tailscale
}

func (s *Server) getTailscaleStatus(w http.ResponseWriter, r *http.Request) {
	if m := s.tailscaleManager(w); m != nil {
		writeJSON(w, http.StatusOK, m.Status(r.Context(), false))
	}
}

func (s *Server) refreshTailscale(w http.ResponseWriter, r *http.Request) {
	if m := s.tailscaleManager(w); m != nil {
		writeJSON(w, http.StatusOK, m.Status(r.Context(), true))
	}
}

func (s *Server) enableTailscaleServe(w http.ResponseWriter, r *http.Request) {
	m := s.tailscaleManager(w)
	if m == nil {
		return
	}
	var req struct {
		HTTPSPort *int `json:"https_port"`
		Replace   bool `json:"replace"`
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	er := tailscale.EnableRequest{Replace: req.Replace}
	if req.HTTPSPort != nil {
		if *req.HTTPSPort < 1 || *req.HTTPSPort > 65535 {
			fieldProblem(w, "https_port", "https_port must be between 1 and 65535")
			return
		}
		er.HTTPSPort = *req.HTTPSPort
	}
	rep, err := m.Enable(r.Context(), er)
	if err != nil {
		s.tailscaleError(w, r, err)
		return
	}
	s.tailscaleConfigChanged()
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) disableTailscaleServe(w http.ResponseWriter, r *http.Request) {
	m := s.tailscaleManager(w)
	if m == nil {
		return
	}
	rep, err := m.Disable(r.Context())
	if err != nil {
		s.tailscaleError(w, r, err)
		return
	}
	s.tailscaleConfigChanged()
	writeJSON(w, http.StatusOK, rep)
}

// tailscaleConfigChanged applies and announces the config.yaml edit of an enable or disable.
func (s *Server) tailscaleConfigChanged() {
	s.reload()
	s.publishFileChange(spec.KindConfig, "")
}

// tailscaleError maps a manager error to the contract's problems.
func (s *Server) tailscaleError(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *tailscale.ConflictError
	var notReady *tailscale.NotReadyError
	var cmd *tailscale.CommandError
	switch {
	case errors.As(err, &conflict):
		conflictProblem(w, typeAlreadyExists, err.Error(), map[string]any{"target": conflict.Target, "https_port": conflict.HTTPSPort})
	case errors.As(err, &notReady):
		conflictProblem(w, typeInvalidState, err.Error(), map[string]any{"warnings": notReady.Warnings})
	case errors.Is(err, tailscale.ErrUnavailable):
		problem(w, http.StatusServiceUnavailable, typeUnavailable, "Unavailable", err.Error(), nil)
	case errors.As(err, &cmd):
		s.log().Warn("api: tailscale command failed", "err", err)
		problem(w, http.StatusServiceUnavailable, typeUnavailable, "Unavailable", err.Error(), nil)
	default:
		s.writeError(w, r, err)
	}
}

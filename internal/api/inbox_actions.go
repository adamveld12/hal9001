package api

import (
	"net/http"
	"strconv"

	"hal9001/internal/inboxact"
)

// inboxActions returns the inbox interaction service, or answers 503 when the daemon runs without one.
func (s *Server) inboxActions(w http.ResponseWriter) (*inboxact.Service, bool) {
	if s.opts.InboxActions == nil {
		problem(w, http.StatusServiceUnavailable, typeUnavailable, "Unavailable", "answering notes is not available in this daemon", nil)
		return nil, false
	}
	return s.opts.InboxActions, true
}

// respondInboxItem answers 202 with the current view of the item.
func (s *Server) respondInboxItem(w http.ResponseWriter, r *http.Request, id string) {
	it, err := s.opts.Store.GetInboxItem(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.resolver(r.Context()).inboxView(*it)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

func (s *Server) answerInboxItem(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.inboxActions(w)
	if !ok {
		return
	}
	var body struct {
		Answers []struct {
			QuestionID string   `json:"question_id"`
			Choices    []string `json:"choices"`
			Other      *string  `json:"other"`
		} `json:"answers"`
	}
	if !decodeJSON(w, r, &body, false) {
		return
	}
	in := make([]inboxact.AnswerInput, 0, len(body.Answers))
	for _, a := range body.Answers {
		in = append(in, inboxact.AnswerInput{QuestionID: a.QuestionID, Choices: a.Choices, Other: a.Other})
	}
	id := r.PathValue("id")
	if err := svc.Answer(r.Context(), id, in); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.respondInboxItem(w, r, id)
}

func (s *Server) runInboxAction(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.inboxActions(w)
	if !ok {
		return
	}
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 {
		notFound(w, "the note has no action %q", r.PathValue("index"))
		return
	}
	id := r.PathValue("id")
	if err := svc.Action(r.Context(), id, idx); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.respondInboxItem(w, r, id)
}

func (s *Server) replyInboxItem(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.inboxActions(w)
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &body, false) {
		return
	}
	id := r.PathValue("id")
	if err := svc.Reply(r.Context(), id, body.Text); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.respondInboxItem(w, r, id)
}

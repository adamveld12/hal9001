package api

import (
	"errors"
	"net/http"

	"hal9001/internal/conversation"
	"hal9001/internal/store"
)

// Conversation endpoints: assistant chats and the agent runs of flow nodes as one list with one transcript shape (see
// internal/conversation). Reads come straight from the store. Forking a run into a chat goes through core.Assist, like
// every call that starts something.

// conversationStatuses are the normalised statuses of the list filter.
var conversationStatuses = []string{
	conversation.StatusRunning, conversation.StatusAwaitingApproval, string(store.AssistIdle), string(store.AgentSucceeded), string(store.AgentFailed),
	string(store.AgentKilled), string(store.AgentDenied), string(store.AgentInterrupted), string(store.AgentTimeout), conversation.StatusClosed,
}

// maxConversationQuery bounds the text of the q filter.
const maxConversationQuery = 200

func (s *Server) conversations() conversation.Service {
	return conversation.Service{Store: s.opts.Store}
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	cq := conversation.Query{
		Source:     q.enum("source", conversation.SourceFlow, conversation.SourceChat),
		Kind:       q.enum("kind", store.AssistTargetGeneral, store.AssistTargetSensor, store.AssistTargetFlow, store.AssistTargetTool, store.ConversationKindAgent),
		FlowID:     q.slug("flow"),
		NodeID:     q.slug("node"),
		FlowRunID:  q.ulid("flow_run"),
		Runtime:    q.slug("runtime"),
		Status:     q.enum("status", conversationStatuses...),
		ForkedFrom: q.ulid("forked_from"),
		Q:          q.str("q"),
		Cursor:     q.str("cursor"),
		Limit:      q.limit(),
	}
	if len([]rune(cq.Q)) > maxConversationQuery {
		q.fail("q must be at most %d characters", maxConversationQuery)
	}
	tr := q.timeRange()
	cq.Since, cq.Until = tr.Since, tr.Until
	if !q.ok(w) {
		return
	}
	page, err := s.conversations().List(r.Context(), cq)
	if err != nil {
		if errors.Is(err, conversation.ErrBadCursor) {
			badRequest(w, "cursor is not a cursor returned by this API")
			return
		}
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	d, err := s.conversations().Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) listConversationMessages(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.conversations().Messages(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) forkConversation(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.assist(w)
	if !ok {
		return
	}
	sess, err := svc.Fork(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	d, err := s.conversations().Get(r.Context(), sess.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/conversations/"+d.ID)
	writeJSON(w, http.StatusCreated, d.Conversation)
}

package conversation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"hal9001/internal/store"
)

// ErrBadCursor is returned for a cursor this package did not issue.
var ErrBadCursor = errors.New("conversation: invalid cursor")

// Service builds conversation views from the store.
type Service struct {
	Store *store.Store
}

// Query selects conversations for List. Every field is optional.
type Query struct {
	Source    string // chat or flow
	Kind      string // assist target kind, or "agent"
	FlowID    string
	NodeID    string
	FlowRunID string
	Runtime   string
	Status    string
	// Q is a case-insensitive text search: titles, ids, flow and node ids, the messages of chats and the prompts and
	// results of flow conversations.
	Q string
	// ForkedFrom is an agent run id: only the chats forked from that run (or from another phase of its node run).
	ForkedFrom string
	// Since is inclusive and Until exclusive, on the last activity.
	Since, Until time.Time
	Cursor       string
	Limit        int
}

// Page is one page of the list. NextCursor is null on the last page.
type Page struct {
	Items      []Conversation `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

// List returns conversations ordered by last activity (newest first), then by id; the cursor continues behind the last
// item of the previous page, so entries with equal timestamps are neither skipped nor repeated.
func (s Service) List(ctx context.Context, q Query) (Page, error) {
	f := store.ConversationFilter{
		Source: q.Source, Kind: q.Kind, FlowID: q.FlowID, NodeID: q.NodeID, FlowRunID: q.FlowRunID, Runtime: q.Runtime, Status: q.Status,
		Q: q.Q, Time: store.TimeRange{Since: q.Since, Until: q.Until}, Limit: q.Limit,
	}
	if q.ForkedFrom != "" {
		chain, err := s.Store.AgentRunChain(ctx, q.ForkedFrom)
		if errors.Is(err, store.ErrNotFound) {
			chain, err = []string{q.ForkedFrom}, nil // the run may be pruned while its forks remain
		}
		if err != nil {
			return Page{}, err
		}
		f.ForkedFrom = chain
	}
	if q.Cursor != "" {
		at, id, err := decodeCursor(q.Cursor)
		if err != nil {
			return Page{}, err
		}
		f.AfterActivity, f.AfterID = at, id
	}
	rows, err := s.Store.ListConversations(ctx, f)
	if err != nil {
		return Page{}, err
	}
	items, err := s.convert(ctx, rows.Items)
	if err != nil {
		return Page{}, err
	}
	out := Page{Items: items}
	if rows.More && len(rows.Items) > 0 {
		last := rows.Items[len(rows.Items)-1]
		c := encodeCursor(last.UpdatedAt, last.ID)
		out.NextCursor = &c
	}
	return out, nil
}

func encodeCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(c string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", ErrBadCursor
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return time.Time{}, "", ErrBadCursor
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, "", ErrBadCursor
	}
	return t, id, nil
}

// Resolve maps an id to the conversation it names: an assist session id is itself, and an agent run id of a flow node
// resolves to the last phase of its node run's conversation (a propose-phase run belongs to the execute-phase run that
// continues it). store.ErrNotFound for anything else, including the agent runs that are turns of a chat.
func (s Service) Resolve(ctx context.Context, id string) (source, convID string, err error) {
	if _, err := s.Store.GetAssistSession(ctx, id); err == nil {
		return SourceChat, id, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", "", err
	}
	ar, err := s.Store.GetAgentRun(ctx, id)
	if err != nil {
		return "", "", err
	}
	if ar.NodeRunID == "" {
		return "", "", fmt.Errorf("%w: conversation %q", store.ErrNotFound, id)
	}
	chain, err := s.Store.AgentRunChain(ctx, id)
	if err != nil {
		return "", "", err
	}
	return SourceFlow, chain[len(chain)-1], nil
}

// Get returns one conversation with the chats forked from it. id is an assist session id or an agent run id.
func (s Service) Get(ctx context.Context, id string) (*Detail, error) {
	_, convID, err := s.Resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.ListConversations(ctx, store.ConversationFilter{ID: convID, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(rows.Items) == 0 {
		return nil, fmt.Errorf("%w: conversation %q", store.ErrNotFound, id)
	}
	items, err := s.convert(ctx, rows.Items)
	if err != nil {
		return nil, err
	}
	d := &Detail{Conversation: items[0], Forks: []Conversation{}}
	if d.Source == SourceFlow {
		forks, err := s.List(ctx, Query{ForkedFrom: d.ID, Limit: MaxForks})
		if err != nil {
			return nil, err
		}
		d.Forks = forks.Items
	}
	return d, nil
}

// convert builds the views of a page of rows: fork counts and previews are read per page.
func (s Service) convert(ctx context.Context, rows []store.ConversationRow) ([]Conversation, error) {
	var chains [][]string
	chainOf := map[string][]string{}
	for _, r := range rows {
		if r.Source != store.ConversationFlow {
			continue
		}
		chain, err := s.Store.AgentRunChain(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		chainOf[r.ID] = chain
		chains = append(chains, chain)
	}
	counts, err := s.Store.ForkCounts(ctx, chains)
	if err != nil {
		return nil, err
	}
	out := make([]Conversation, 0, len(rows))
	for _, r := range rows {
		c := Conversation{
			ID: r.ID, Source: r.Source, Kind: r.Kind, Status: r.Status, Runtime: r.Runtime, Model: nonEmpty(r.Model),
			FlowID: nonEmpty(r.FlowID), FlowRunID: nonEmpty(r.FlowRunID), NodeID: nonEmpty(r.NodeID), NodeRunID: nonEmpty(r.NodeRunID),
			AgentRunID: nonEmpty(r.AgentRunID), AssistSessionID: nonEmpty(r.AssistSessionID), ForkContext: nonEmpty(r.ForkContext),
			ForkCount: counts[r.ID], StartedAt: r.StartedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		}
		if r.EndedAt != nil {
			t := r.EndedAt.UTC()
			c.EndedAt = &t
		}
		c.Running = r.Status == StatusRunning || r.Status == StatusAwaitingApproval
		if r.Source == store.ConversationChat {
			c.Continuable = r.Status != StatusClosed
		} else {
			c.Continuable = !c.Running
		}
		if r.ForkedFromAgentRunID != "" {
			c.ForkedFrom = &ForkRef{AgentRunID: r.ForkedFromAgentRunID, FlowID: nonEmpty(r.FlowID), FlowRunID: nonEmpty(r.FlowRunID), NodeID: nonEmpty(r.NodeID)}
		}
		c.Title = titleOf(r)
		if c.Preview, err = s.preview(ctx, r); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// titleOf names a conversation: a chat's stored title (the start of its first message), a fork's origin before the
// first message, else a generic name; a flow conversation is named after its flow and node.
func titleOf(r store.ConversationRow) string {
	if r.Source == store.ConversationFlow {
		return r.FlowID + " / " + r.NodeID
	}
	switch {
	case r.Title != "":
		return r.Title
	case r.ForkedFromAgentRunID != "" && r.FlowID != "":
		return "Continued: " + r.FlowID + " / " + r.NodeID
	case r.ForkedFromAgentRunID != "":
		return "Continued from run " + r.ForkedFromAgentRunID
	case r.Kind == store.AssistTargetGeneral:
		return "New chat"
	}
	return "New " + r.Kind + " draft"
}

// preview is the short text a list shows under the title.
func (s Service) preview(ctx context.Context, r store.ConversationRow) (string, error) {
	if r.Source == store.ConversationChat {
		text, err := s.Store.LastChatText(ctx, r.ID)
		return clipLine(text, PreviewLength), err
	}
	ar, err := s.Store.GetAgentRun(ctx, r.ID)
	if err != nil {
		return "", err
	}
	result, err := s.Store.ReadContent(ar.Result)
	if err != nil {
		return "", err
	}
	text := resultText(string(result))
	if text == "" {
		text = ar.Error
	}
	if text == "" {
		prompt, err := s.Store.ReadContent(ar.Prompt)
		if err != nil {
			return "", err
		}
		text = string(prompt)
	}
	return clipLine(text, PreviewLength), nil
}

// resultText is the human text of a stored agent run result: its final message, else a note's title, else the ports
// it emitted on. The engine stores a JSON object (final_message, note, output, emissions); plain text is accepted too.
func resultText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var env struct {
		FinalMessage *string `json:"final_message"`
		Note         *struct {
			Title string `json:"title"`
		} `json:"note"`
		Title     string `json:"title"`
		Emissions []struct {
			Port string `json:"port"`
		} `json:"emissions"`
	}
	if json.Unmarshal([]byte(raw), &env) != nil {
		return raw
	}
	switch {
	case env.FinalMessage != nil && strings.TrimSpace(*env.FinalMessage) != "":
		return *env.FinalMessage
	case env.Note != nil && env.Note.Title != "":
		return env.Note.Title
	case env.Title != "":
		return env.Title
	case len(env.Emissions) > 0:
		ports := make([]string, len(env.Emissions))
		for i, e := range env.Emissions {
			ports[i] = e.Port
		}
		return "Emitted on " + strings.Join(ports, ", ")
	}
	return ""
}

// clipLine collapses whitespace and cuts the text to n characters.
func clipLine(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	return string([]rune(text)[:n]) + "…"
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

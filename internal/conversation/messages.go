package conversation

import (
	"context"
	"encoding/json"

	"hal9001/internal/store"
)

// Messages returns the transcript of a conversation (id as in Get): a chat's stored messages, or the events of the
// flow run folded into the same shape. At most MaxMessages entries are returned, the newest ones.
func (s Service) Messages(ctx context.Context, id string) (Messages, error) {
	source, convID, err := s.Resolve(ctx, id)
	if err != nil {
		return Messages{}, err
	}
	var msgs []Message
	if source == SourceChat {
		msgs, err = s.chatMessages(ctx, convID)
	} else {
		msgs, err = s.flowMessages(ctx, convID)
	}
	if err != nil {
		return Messages{}, err
	}
	out := Messages{Items: msgs}
	if len(msgs) > MaxMessages {
		out.Items, out.Truncated = msgs[len(msgs)-MaxMessages:], true
	}
	return out, nil
}

func (s Service) chatMessages(ctx context.Context, sessionID string) ([]Message, error) {
	rows, err := s.Store.ListAssistMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, m := range rows {
		out = append(out, FromAssist(m))
	}
	return out, nil
}

// FromAssist converts a stored chat message.
func FromAssist(m store.AssistMessage) Message {
	return Message{
		ID: m.ID, Role: m.Role, Kind: m.Kind, Text: m.Text, AgentRunID: nonEmpty(m.AgentRunID), ToolCall: rawOrNull(m.ToolCall),
		ApprovalID: nonEmpty(m.ApprovalID), CreatedAt: m.CreatedAt.UTC(),
	}
}

func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

// flowMessages folds the agent runs of a node (the propose phase, then the execute phase) into one transcript.
func (s Service) flowMessages(ctx context.Context, headID string) ([]Message, error) {
	chain, err := s.Store.AgentRunChain(ctx, headID)
	if err != nil {
		return nil, err
	}
	var out []Message
	for i, id := range chain {
		ar, err := s.Store.GetAgentRun(ctx, id)
		if err != nil {
			return nil, err
		}
		msgs, err := s.foldRun(ctx, ar, i == len(chain)-1)
		if err != nil {
			return nil, err
		}
		out = append(out, msgs...)
	}
	return out, nil
}

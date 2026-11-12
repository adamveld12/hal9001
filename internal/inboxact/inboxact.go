// Package inboxact runs the interactions of an inbox note with the agent that wrote it: answers to its questions,
// its offered actions and free replies. The first interaction forks the agent's conversation (assist.Manager.Fork)
// once per note; every interaction sends one message into that fork in the background and follows the turn it starts
// (running, waiting for an approval, done or failed) through core.TurnObserver.
package inboxact

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

const (
	// MaxReplyRunes bounds a reply.
	MaxReplyRunes = 8000
	// MaxResultRunes bounds the one-line result of an interaction.
	MaxResultRunes = 160
)

// AnswerInput is the user's answer to one question: labels of chosen options and/or free text.
type AnswerInput struct {
	QuestionID string
	Choices    []string
	// Other is the free text; nil when none was given.
	Other *string
}

// Service implements the three interactions and observes the turns they start.
type Service struct {
	Store  *store.Store
	Assist core.Assist
	Log    *slog.Logger

	mu    sync.Mutex
	locks map[string]*itemLock
	ctx   context.Context
}

type itemLock struct {
	mu   sync.Mutex
	refs int
}

var _ core.TurnObserver = (*Service)(nil)

// Start ends the interactions whose turns died with the previous daemon and sets the context the observer methods
// write the store with (the daemon's lifetime).
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	s.ctx = context.WithoutCancel(ctx)
	s.mu.Unlock()
	n, err := s.Store.FailActiveInboxInteractions(ctx, "the daemon stopped")
	if err != nil {
		return fmt.Errorf("inboxact: fail interrupted interactions: %w", err)
	}
	if n > 0 {
		s.log().Info("inboxact: interrupted interactions marked failed", "count", n)
	}
	return nil
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) storeCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// lock serializes the calls on one note; the returned function releases it.
func (s *Service) lock(itemID string) func() {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*itemLock{}
	}
	l := s.locks[itemID]
	if l == nil {
		l = &itemLock{}
		s.locks[itemID] = l
	}
	l.refs++
	s.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(s.locks, itemID)
		}
		s.mu.Unlock()
	}
}

func invalid(path, format string, args ...any) error {
	return &core.ValidationError{Diagnostics: []spec.Diagnostic{{
		Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: path, Message: fmt.Sprintf(format, args...),
	}}}
}

// load returns the note, which must have been written by an agent.
func (s *Service) load(ctx context.Context, itemID string) (*store.InboxItem, error) {
	it, err := s.Store.GetInboxItem(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if it.AgentRunID == "" {
		return nil, fmt.Errorf("%w: this note was not written by an agent, so there is nobody to answer", core.ErrInvalidState)
	}
	return it, nil
}

// Answer sends the answers to the note's questions to the agent.
func (s *Service) Answer(ctx context.Context, itemID string, in []AnswerInput) error {
	defer s.lock(itemID)()
	it, err := s.load(ctx, itemID)
	if err != nil {
		return err
	}
	answers, err := validateAnswers(it.Questions, in)
	if err != nil {
		return err
	}
	return s.send(ctx, it, &store.InboxInteraction{ItemID: itemID, Kind: store.InteractionAnswers, Answers: answers, Message: answersMessage(answers)})
}

// Action sends the prompt of action index to the agent.
func (s *Service) Action(ctx context.Context, itemID string, index int) error {
	defer s.lock(itemID)()
	it, err := s.load(ctx, itemID)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(it.Actions) {
		return fmt.Errorf("%w: the note has no action %d", store.ErrNotFound, index)
	}
	return s.send(ctx, it, &store.InboxInteraction{ItemID: itemID, Kind: store.InteractionAction, ActionIndex: &index, Message: it.Actions[index].Prompt})
}

// Reply sends text to the agent verbatim.
func (s *Service) Reply(ctx context.Context, itemID, text string) error {
	defer s.lock(itemID)()
	it, err := s.load(ctx, itemID)
	if err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(text) == "":
		return invalid("text", "the reply is empty")
	case utf8.RuneCountInString(text) > MaxReplyRunes:
		return invalid("text", "the reply is over %d characters", MaxReplyRunes)
	}
	return s.send(ctx, it, &store.InboxInteraction{ItemID: itemID, Kind: store.InteractionReply, Message: text})
}

// send forks the note's conversation when it has none, records the interaction (the store refuses what may not
// happen), and sends the message. A message that cannot be sent leaves no trace, so the action stays available.
func (s *Service) send(ctx context.Context, it *store.InboxItem, in *store.InboxInteraction) error {
	conv := it.ConversationID
	if conv == "" {
		sess, err := s.Assist.Fork(ctx, it.AgentRunID)
		if err != nil {
			return err
		}
		conv = sess.ID
	}
	in.ConversationID = conv
	if err := s.Store.StartInboxInteraction(ctx, in); err != nil {
		return err
	}
	if _, err := s.Assist.SendMessage(ctx, conv, in.Message, nil, nil); err != nil {
		if derr := s.Store.DeleteInboxInteraction(context.WithoutCancel(ctx), in.ID); derr != nil && !errors.Is(derr, store.ErrNotFound) {
			s.log().Error("inboxact: drop unsent interaction", "interaction", in.ID, "err", derr)
		}
		return err
	}
	return nil
}

func validateAnswers(qs []store.InboxQuestion, in []AnswerInput) ([]store.InboxAnswer, error) {
	if len(in) == 0 {
		return nil, invalid("answers", "no answers given")
	}
	byID := map[string]int{}
	for i, q := range qs {
		byID[q.ID] = i
	}
	type placed struct {
		q int
		a store.InboxAnswer
	}
	var out []placed
	seen := map[string]bool{}
	for i, a := range in {
		path := fmt.Sprintf("answers[%d]", i)
		qi, ok := byID[a.QuestionID]
		switch {
		case !ok:
			return nil, invalid(path+".question_id", "the note has no question %q", a.QuestionID)
		case seen[a.QuestionID]:
			return nil, invalid(path+".question_id", "question %q is answered twice", a.QuestionID)
		}
		seen[a.QuestionID] = true
		q := qs[qi]
		labels := map[string]bool{}
		for _, o := range q.Options {
			labels[o.Label] = true
		}
		got := map[string]bool{}
		for _, c := range a.Choices {
			switch {
			case !labels[c]:
				return nil, invalid(path+".choices", "%q is not an option of question %q", c, q.ID)
			case got[c]:
				return nil, invalid(path+".choices", "option %q is chosen twice", c)
			}
			got[c] = true
		}
		other := ""
		if a.Other != nil {
			if other = strings.TrimSpace(*a.Other); other == "" {
				return nil, invalid(path+".other", "the free text is empty")
			}
		}
		n := len(a.Choices)
		if other != "" {
			n++
		}
		switch {
		case q.Multi && n == 0:
			return nil, invalid(path, "question %q needs at least one choice or free text", q.ID)
		case !q.Multi && n != 1:
			return nil, invalid(path, "question %q takes exactly one choice or free text", q.ID)
		}
		out = append(out, placed{qi, store.InboxAnswer{QuestionID: q.ID, Choices: append([]string{}, a.Choices...), Other: other}})
	}
	slices.SortFunc(out, func(a, b placed) int { return a.q - b.q })
	res := make([]store.InboxAnswer, len(out))
	for i, p := range out {
		res[i] = p.a
	}
	return res, nil
}

func answersMessage(as []store.InboxAnswer) string {
	var b strings.Builder
	b.WriteString("My answers to your questions:")
	for _, a := range as {
		parts := append([]string{}, a.Choices...)
		if a.Other != "" {
			parts = append(parts, "Other: "+a.Other)
		}
		b.WriteString("\n- " + a.QuestionID + ": " + strings.Join(parts, ", "))
	}
	return b.String()
}

// TurnWaiting implements core.TurnObserver.
func (s *Service) TurnWaiting(sessionID, approvalID string) {
	s.settle(sessionID, func(in *store.InboxInteraction) {
		in.Status, in.ApprovalID = store.InteractionWaiting, approvalID
	})
}

// TurnResumed implements core.TurnObserver.
func (s *Service) TurnResumed(sessionID string) {
	s.settle(sessionID, func(in *store.InboxInteraction) {
		in.Status, in.ApprovalID = store.InteractionRunning, ""
	})
}

// TurnEnded implements core.TurnObserver.
func (s *Service) TurnEnded(sessionID string, failed bool, errText, result string) {
	s.settle(sessionID, func(in *store.InboxInteraction) {
		in.ApprovalID = ""
		if failed {
			in.Status, in.Error = store.InteractionFailed, errText
			return
		}
		in.Status, in.Result = store.InteractionDone, OneLine(result)
	})
}

func (s *Service) settle(sessionID string, fn func(*store.InboxInteraction)) {
	if _, err := s.Store.SettleInboxInteraction(s.storeCtx(), sessionID, fn); err != nil {
		s.log().Error("inboxact: settle interaction", "conversation", sessionID, "err", err)
	}
}

// OneLine reduces an agent message to one line: its first non-empty line without leading markdown markers, at most
// MaxResultRunes runes (cut with an ellipsis).
func OneLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimLeft(line, "#*->` \t"))
		line = strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "`", "").Replace(line))
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > MaxResultRunes {
			return strings.TrimSpace(string(r[:MaxResultRunes-1])) + "…"
		}
		return line
	}
	return ""
}

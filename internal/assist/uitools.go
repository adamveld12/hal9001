package assist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// The two tools that move the user: navigate_ui opens a page of the app, start_draft hands the chat over to a new
// draft session. Neither changes anything in the daemon; each publishes a stream event the web UI acts on, so neither
// needs approval. Every assist session gets both.

const (
	// maxNavigateReason bounds navigate_ui's reason (a one-line text; a longer one is cut), in bytes.
	maxNavigateReason = 197
	// maxDraftPrompt bounds start_draft's prompt, in bytes.
	maxDraftPrompt = 4000
	// maxListedIDs bounds the ids a start_draft error lists.
	maxListedIDs = 40
)

// UIPublisher delivers the events of the UI tools to the web UI (the API's stream: assist.navigate and
// assist.start_draft, filtered by assist session). The daemon sets it with Manager.SetUIPublisher.
type UIPublisher interface {
	PublishAssistNavigate(sessionID, path, reason string)
	PublishAssistStartDraft(sessionID, kind, id, prompt string)
}

// SetUIPublisher sets where navigate_ui and start_draft publish. Until it is set they fail with a tool error.
func (m *Manager) SetUIPublisher(p UIPublisher) {
	m.mu.Lock()
	m.ui = p
	m.mu.Unlock()
}

func (m *Manager) uiPublisher() UIPublisher {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ui
}

var errNoUI = errors.New("the app's UI is not connected to this daemon, so the user's view cannot be moved")

func (s *session) uiTools() []tools.Tool {
	return []tools.Tool{
		{
			Name: "navigate_ui",
			Description: "Open a page of the app for the user. It only moves the user's view (it changes nothing and needs no approval); the result says the view was asked to open the page, you cannot see it. " +
				"`path` is an in-app path from the list of pages in your instructions, for example `/flows/triage` or `/runs/<id>?node=<node>`; other paths are refused. " +
				"Use it when the user asks to see or open something, at most once per answer, and say where you took them.",
			InputSchema: obj(map[string]any{
				"path":   map[string]any{"type": "string", "description": "In-app path, starting with /, with an optional query."},
				"reason": map[string]any{"type": "string", "description": "Optional: one short line on why, shown to the user."},
			}, "path"),
			Run: s.runNavigateUI,
		},
		{
			Name: "start_draft",
			Description: "Offer the user a new draft session to create or edit a sensor, a flow or a custom tool: the chat switches to a new session on that target and `prompt` becomes its first message. " +
				"It changes nothing by itself and needs no approval. Use it instead of drafting yourself when the user wants to create or change one of these. " +
				"`id` names an existing sensor, flow or tool to edit (it must exist); omit it for a new one. `prompt` is the user's request restated with the details the draft session needs.",
			InputSchema: obj(map[string]any{
				"kind":   map[string]any{"type": "string", "enum": []string{"sensor", "flow", "tool"}},
				"id":     map[string]any{"type": "string", "description": "An existing sensor, flow or tool id to edit; omit for a new one."},
				"prompt": map[string]any{"type": "string", "description": fmt.Sprintf("The first message of the draft session (at most %d bytes).", maxDraftPrompt)},
			}, "kind"),
			Run: s.runStartDraft,
		},
	}
}

func (s *session) runNavigateUI(_ context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	if err := s.openLocked(); err != nil {
		return tools.Result{}, err
	}
	path, err := cleanUIPath(a.Path)
	if err != nil {
		return tools.Result{}, err
	}
	reason, _ := singleLine(a.Reason, maxNavigateReason)
	ui := s.m.uiPublisher()
	if ui == nil {
		return tools.Result{}, errNoUI
	}
	ui.PublishAssistNavigate(s.id, path, reason)
	return tools.Result{Text: "Asked the user's view to open " + path + ". The daemon cannot see whether the page opened; do not wait for it."}, nil
}

func (s *session) runStartDraft(_ context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Kind   string `json:"kind"`
		ID     string `json:"id"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	if err := s.openLocked(); err != nil {
		return tools.Result{}, err
	}
	switch a.Kind {
	case store.AssistTargetSensor, store.AssistTargetFlow, store.AssistTargetTool:
	default:
		return tools.Result{}, fmt.Errorf("start_draft: kind must be sensor, flow or tool, not %q", a.Kind)
	}
	if a.ID != "" {
		if err := s.m.checkDraftTarget(a.Kind, a.ID); err != nil {
			return tools.Result{}, err
		}
	}
	prompt := strings.TrimSpace(a.Prompt)
	if len(prompt) > maxDraftPrompt {
		return tools.Result{}, fmt.Errorf("start_draft: prompt is %d bytes, the limit is %d: shorten it", len(prompt), maxDraftPrompt)
	}
	ui := s.m.uiPublisher()
	if ui == nil {
		return tools.Result{}, errNoUI
	}
	ui.PublishAssistStartDraft(s.id, a.Kind, a.ID, prompt)
	what := "a new " + a.Kind
	if a.ID != "" {
		what = "the " + a.Kind + " `" + a.ID + "`"
	}
	return tools.Result{Text: "Asked the user's view to start a draft session on " + what + ". The draft session starts in the chat if the UI is open; tell the user briefly what you set up and stop."}, nil
}

// openLocked is usableLocked under the session's lock: a closed session's tools do nothing.
func (s *session) openLocked() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usableLocked()
}

// checkDraftTarget reports whether the sensor, flow or tool id is saved in the workspace; the error names the ids
// that exist.
func (m *Manager) checkDraftTarget(kind, id string) error {
	if !spec.ValidID(id) {
		return fmt.Errorf("start_draft: id %q is not a valid %s id", id, kind)
	}
	ws := spec.Load(m.home)
	var ids []string
	found := false
	switch kind {
	case store.AssistTargetSensor:
		found = ws.Sensor(id) != nil
		for _, f := range ws.Sensors {
			ids = append(ids, f.Name)
		}
	case store.AssistTargetFlow:
		found = ws.Flow(id) != nil
		for _, f := range ws.Flows {
			ids = append(ids, f.Name)
		}
	default:
		found = ws.Tool(id) != nil
		for _, f := range ws.Tools {
			ids = append(ids, f.Name)
		}
	}
	if found {
		return nil
	}
	sort.Strings(ids)
	list := "none are saved yet: omit id to start a new one"
	if len(ids) > 0 {
		more := ""
		if len(ids) > maxListedIDs {
			ids, more = ids[:maxListedIDs], fmt.Sprintf(" and %d more", len(ids)-maxListedIDs)
		}
		list = "saved " + kind + "s: " + nameList(ids) + more
	}
	return fmt.Errorf("start_draft: there is no %s %q (%s)", kind, id, list)
}

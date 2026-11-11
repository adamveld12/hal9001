package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Limits of send_note: sizes, and how many notes one client may post, so an agent in a loop cannot flood the Inbox.
const (
	MaxNoteTitle  = 200
	MaxNoteBody   = 16 << 10
	MaxNoteURL    = 2048
	MaxNoteLabel  = 100
	MaxNoteThread = 64
	// NotesPerMinute and NotesPerHour are per client (its name, else its principal).
	NotesPerMinute = 5
	NotesPerHour   = 60
)

// Limits of request_approval's content.
const (
	maxRequestTitle    = 200
	maxRequestBody     = 16 << 10
	maxEditableFields  = 20
	maxEditableKey     = 64
	maxEditableValue   = 8 << 10
	maxEditableKeyText = "dot-path such as reply or draft.subject"
)

var priorities = []string{"low", "normal", "high", "urgent"}

func prioProp(what string) map[string]any {
	return map[string]any{"type": "string", "enum": priorities, "description": what}
}

func validPriority(s string) bool {
	if s == "" {
		return true
	}
	for _, p := range priorities {
		if s == p {
			return true
		}
	}
	return false
}

// noteLimiter is a sliding-window rate limit per client.
type noteLimiter struct {
	mu   sync.Mutex
	sent map[string][]time.Time
}

// allow records a note of key at now, or reports how long to wait.
func (l *noteLimiter) allow(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sent == nil {
		l.sent = map[string][]time.Time{}
	}
	var recent []time.Time
	for _, t := range l.sent[key] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	inMinute := 0
	var oldestMinute time.Time
	for _, t := range recent {
		if now.Sub(t) < time.Minute {
			if inMinute == 0 {
				oldestMinute = t
			}
			inMinute++
		}
	}
	switch {
	case inMinute >= NotesPerMinute:
		l.sent[key] = recent
		return time.Minute - now.Sub(oldestMinute), false
	case len(recent) >= NotesPerHour:
		l.sent[key] = recent
		return time.Hour - now.Sub(recent[0]), false
	}
	l.sent[key] = append(recent, now)
	return 0, true
}

// hostTools are the operator tools that are not one REST call: validation and creation of specs, request_approval,
// send_note and approval_get.
func (o *Operator) hostTools() []operatorTool {
	ts := append(o.specTools(flowKind), o.specTools(sensorKind)...)
	return append(ts, o.requestApprovalTool(), o.sendNoteTool(), o.approvalGetTool())
}

// ---- approval_get ----

func (o *Operator) approvalGetTool() operatorTool {
	return o.hostTool("approval_get",
		"Read one approval: what is being asked, its status and the decision. For a request_approval it adds result: {decision, comment, values, edits}; poll this when request_approval returned status pending.",
		obj(map[string]any{"id": idProp("approval")}, "id"), "", false,
		func(a map[string]any) error { _, err := idArg(a); return err },
		func(ctx context.Context, env OperatorEnv, a map[string]any) (Result, error) {
			id, _ := idArg(a)
			view, err := getApproval(ctx, env.API, id)
			if err != nil {
				return Result{}, err
			}
			if t, _ := view["type"].(string); t == "request" {
				view["result"] = approvalResult(view)
			}
			return jsonResult(view)
		})
}

func getApproval(ctx context.Context, api APIFunc, id string) (map[string]any, error) {
	status, out, err := api(ctx, http.MethodGet, "/api/approvals/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, errors.New(problemText(status, out))
	}
	var view map[string]any
	if err := json.Unmarshal(out, &view); err != nil {
		return nil, fmt.Errorf("decode approval: %w", err)
	}
	return view, nil
}

// approvalResult is the outcome of a request approval as the agent reads it, from the approval's REST view:
// decision pending, approved, denied or expired, the approver's comment, and for an approval the final value of every
// editable field (values) with the ones that were changed (edits: {before, after}). A denial with a comment (or a held
// one) adds message: the steering the agent must follow.
func approvalResult(view map[string]any) map[string]any {
	status, _ := view["status"].(string)
	res := map[string]any{"approval_id": view["id"], "status": status}
	if status == "pending" {
		res["decision"] = "pending"
		if e := view["expires_at"]; e != nil {
			res["expires_at"] = e
		}
		return res
	}
	res["decision"] = status
	comment, _ := view["comment"].(string)
	res["comment"] = comment
	for _, k := range []string{"decided_by", "decided_at"} {
		if v := view[k]; v != nil {
			res[k] = v
		}
	}
	if status == "denied" {
		held, _ := view["held"].(bool)
		if msg := DeniedText(comment, held); msg != "" {
			res["message"] = msg
		}
	}
	if status != "approved" {
		return res
	}
	values := map[string]any{}
	editable, _ := view["editable"].([]any)
	for _, e := range editable {
		if m, ok := e.(map[string]any); ok {
			if k, _ := m["key"].(string); k != "" {
				values[k] = m["value"]
			}
		}
	}
	edits, _ := view["edits"].(map[string]any)
	for k, e := range edits {
		if m, ok := e.(map[string]any); ok {
			values[k] = m["after"]
		}
	}
	if len(values) > 0 {
		res["values"] = values
		res["edited"] = len(edits) > 0
	}
	if len(edits) > 0 {
		res["edits"] = edits
	}
	return res
}

// ---- request_approval ----

func (o *Operator) requestApprovalTool() operatorTool {
	schema := obj(requestApprovalProps(), "title", "description")
	validate := func(a map[string]any) error { _, err := parseQuestion(a); return err }
	return o.hostTool("request_approval",
		"Ask the user for a decision. Opens a request approval in the hal9001 Inbox (menubar count and a native notification with Approve and Deny) and waits up to wait_seconds (default 240, the most a client keeps a call open). description is required: say in one or two sentences what you want to do and why; the user sees it labelled as written by you, next to the title and body. Returns {approval_id, status, decision, comment, values, edits, message}: decision is approved, denied or expired, or pending when the wait ended with no decision. If the user denies with a comment, comment is their steering and message says so: do not retry the same action unchanged, do what they asked instead. If pending, poll approval_get with the approval_id (its result field has the same shape); the approval stays open until it is decided or its timeout_seconds (default 24 hours) passes. You cannot decide it yourself.",
		schema, ServiceAdmin, true, validate,
		func(ctx context.Context, env OperatorEnv, a map[string]any) (Result, error) {
			q, err := parseQuestion(a)
			if err != nil {
				return Result{}, err
			}
			q.Caller = env.Caller
			id, err := o.host.AskOperator(ctx, q)
			if err != nil {
				return Result{}, fmt.Errorf("tool request_approval: %w", err)
			}
			view, err := getApproval(ctx, env.API, id)
			if err != nil {
				return Result{}, err
			}
			res := approvalResult(view)
			if res["status"] == "pending" {
				res["hint"] = "No decision yet. The approval stays open: call approval_get with this approval_id and read result.decision."
			}
			return jsonResult(res)
		})
}

func intArg(a map[string]any, key string, lo, hi int64) (int64, bool, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return 0, false, nil
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, false, fmt.Errorf("%s must be an integer", key)
	}
	i, err := n.Int64()
	if err != nil || i < lo || i > hi {
		return 0, false, fmt.Errorf("%s must be an integer from %d to %d", key, lo, hi)
	}
	return i, true, nil
}

func textArg(a map[string]any, key string, maxLen int, required bool) (string, error) {
	s, err := strArg(a, key)
	if err != nil {
		return "", err
	}
	s = strings.TrimSpace(s)
	if required && s == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("%s is not valid UTF-8", key)
	}
	if len(s) > maxLen {
		return "", fmt.Errorf("%s is %d bytes; the limit is %d", key, len(s), maxLen)
	}
	return s, nil
}

// parseQuestion validates the arguments of request_approval.
func parseQuestion(a map[string]any) (OperatorQuestion, error) {
	title, err := textArg(a, "title", maxRequestTitle, true)
	if err != nil {
		return OperatorQuestion{}, err
	}
	desc, err := parseDescription(a)
	if err != nil {
		return OperatorQuestion{}, err
	}
	body, err := textArg(a, "body", maxRequestBody, false)
	if err != nil {
		return OperatorQuestion{}, err
	}
	prio, err := strArg(a, "priority")
	if err != nil {
		return OperatorQuestion{}, err
	}
	if !validPriority(prio) {
		return OperatorQuestion{}, fmt.Errorf("priority must be one of %s", strings.Join(priorities, ", "))
	}
	q := OperatorQuestion{Title: title, Description: desc, BodyMD: body, Priority: prio, Timeout: DefaultRequestTimeout, Wait: MaxRequestWait}
	if s, ok, err := intArg(a, "timeout_seconds", int64(MinRequestTimeout.Seconds()), int64(MaxRequestTimeout.Seconds())); err != nil {
		return OperatorQuestion{}, err
	} else if ok {
		q.Timeout = time.Duration(s) * time.Second
	}
	if s, ok, err := intArg(a, "wait_seconds", 0, int64(MaxRequestWait.Seconds())); err != nil {
		return OperatorQuestion{}, err
	} else if ok {
		q.Wait = time.Duration(s) * time.Second
	}
	if v, ok := a["editable"]; ok && v != nil {
		arr, ok := v.([]any)
		if !ok {
			return OperatorQuestion{}, errors.New("editable must be an array of {key, value}")
		}
		if len(arr) > maxEditableFields {
			return OperatorQuestion{}, fmt.Errorf("editable has %d fields; the limit is %d", len(arr), maxEditableFields)
		}
		seen := map[string]bool{}
		for i, e := range arr {
			m, ok := e.(map[string]any)
			if !ok {
				return OperatorQuestion{}, fmt.Errorf("editable[%d] must be an object {key, value}", i)
			}
			for k := range m {
				if k != "key" && k != "value" {
					return OperatorQuestion{}, fmt.Errorf("editable[%d]: unknown property %q", i, k)
				}
			}
			key, _ := m["key"].(string)
			if key == "" || len(key) > maxEditableKey || strings.ContainsAny(key, " \t\r\n") || strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") || strings.Contains(key, "..") {
				return OperatorQuestion{}, fmt.Errorf("editable[%d]: key %q must be a %s (max %d characters, no spaces)", i, key, maxEditableKeyText, maxEditableKey)
			}
			if seen[key] {
				return OperatorQuestion{}, fmt.Errorf("editable[%d]: key %q is repeated", i, key)
			}
			seen[key] = true
			raw, err := json.Marshal(m["value"])
			if err != nil || len(raw) > maxEditableValue {
				return OperatorQuestion{}, fmt.Errorf("editable[%d]: value of %q is too large (limit %d bytes)", i, key, maxEditableValue)
			}
			q.Editable = append(q.Editable, OperatorEditable{Key: key, Value: raw})
		}
	}
	return q, nil
}

// ---- send_note ----

func (o *Operator) sendNoteTool() operatorTool {
	schema := obj(map[string]any{
		"title":      prop("string", fmt.Sprintf("Note title, one line (max %d characters).", MaxNoteTitle)),
		"body":       prop("string", fmt.Sprintf("Markdown body (max %d bytes).", MaxNoteBody)),
		"priority":   prioProp("low, normal (default), high or urgent. high and urgent also play the notification sound."),
		"url":        prop("string", fmt.Sprintf("An http or https link to attach (max %d characters).", MaxNoteURL)),
		"link_label": prop("string", fmt.Sprintf("Label of the link (default: the URL; max %d characters).", MaxNoteLabel)),
		"thread":     prop("string", fmt.Sprintf("Groups notifications: a new note with the same thread replaces the previous native notification (max %d characters).", MaxNoteThread)),
	}, "title")
	return o.hostTool("send_note",
		fmt.Sprintf("Post a note to the user's hal9001 Inbox and raise a notification (native, or terminal-notifier, per the user's notify_via setting). Needs no approval. The note shows which MCP client sent it. Limits: %d notes per minute and %d per hour per client, title max %d characters, body max %d bytes.", NotesPerMinute, NotesPerHour, MaxNoteTitle, MaxNoteBody),
		schema, ServiceAdmin, false,
		func(a map[string]any) error { _, err := parseNote(a); return err },
		func(ctx context.Context, env OperatorEnv, a map[string]any) (Result, error) {
			n, err := parseNote(a)
			if err != nil {
				return Result{}, err
			}
			n.Caller = env.Caller
			key := env.Caller.Client
			if key == "" {
				key = env.Caller.Principal
			}
			if wait, ok := o.notes.allow(key, time.Now()); !ok {
				return Result{}, fmt.Errorf("rate limit: at most %d notes per minute and %d per hour per client; try again in %d seconds", NotesPerMinute, NotesPerHour, int(wait.Seconds())+1)
			}
			noted, err := o.host.PostOperatorNote(ctx, n)
			if err != nil {
				return Result{}, fmt.Errorf("tool send_note: %w", err)
			}
			return jsonResult(map[string]any{"item_id": noted.ItemID, "url": noted.URL})
		})
}

func parseNote(a map[string]any) (OperatorNote, error) {
	title, err := textArg(a, "title", MaxNoteTitle, true)
	if err != nil {
		return OperatorNote{}, err
	}
	if strings.ContainsAny(title, "\r\n") {
		return OperatorNote{}, errors.New("title must be one line")
	}
	body, err := textArg(a, "body", MaxNoteBody, false)
	if err != nil {
		return OperatorNote{}, err
	}
	prio, err := strArg(a, "priority")
	if err != nil {
		return OperatorNote{}, err
	}
	if !validPriority(prio) {
		return OperatorNote{}, fmt.Errorf("priority must be one of %s", strings.Join(priorities, ", "))
	}
	n := OperatorNote{Title: title, BodyMD: body, Priority: prio}
	if n.URL, err = textArg(a, "url", MaxNoteURL, false); err != nil {
		return OperatorNote{}, err
	}
	if n.URL != "" {
		u, err := url.Parse(n.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return OperatorNote{}, errors.New("url must be an absolute http or https URL")
		}
	}
	if n.URLLabel, err = textArg(a, "link_label", MaxNoteLabel, false); err != nil {
		return OperatorNote{}, err
	}
	if n.Thread, err = textArg(a, "thread", MaxNoteThread, false); err != nil {
		return OperatorNote{}, err
	}
	if strings.ContainsAny(n.Thread, "\r\n") {
		return OperatorNote{}, errors.New("thread must be one line")
	}
	return n, nil
}

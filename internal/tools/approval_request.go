package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// RequestApprovalToolName is the hub tool (and the operator tool) with which an agent asks the user for a decision.
const RequestApprovalToolName = "request_approval"

// ErrDescriptionRequired is the tool error of a request_approval call without a description.
var ErrDescriptionRequired = errors.New("description is required: say in one or two sentences what you want to do and why")

// maxRequestDescription is the longest description, in runes.
const maxRequestDescription = MaxApprovalDescription

// parseDescription reads and checks the required description argument of request_approval.
func parseDescription(a map[string]any) (string, error) {
	s, err := textArg(a, "description", 4*maxRequestDescription, false)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", ErrDescriptionRequired
	}
	if n := utf8.RuneCountInString(s); n > maxRequestDescription {
		return "", fmt.Errorf("description is %d characters; the limit is %d: shorten it to one or two sentences", n, maxRequestDescription)
	}
	return s, nil
}

// requestApprovalProps are the properties of request_approval's schema, shared by the hub tool (agents inside a run)
// and the operator tool (MCP clients).
func requestApprovalProps() map[string]any {
	return map[string]any{
		"title":       prop("string", fmt.Sprintf("What you are asking, one line (max %d characters). Shown as the approval's title and in the notification.", maxRequestTitle)),
		"description": prop("string", fmt.Sprintf("Required. One or two sentences: what you want to do and why (max %d characters). The user sees it labelled as written by you; hal shows the real command, diff or target next to it.", maxRequestDescription)),
		"body":        prop("string", fmt.Sprintf("Details as markdown (max %d bytes): context, what happens on approve or deny.", maxRequestBody)),
		"priority":    prioProp("Inbox priority (default normal). high and urgent also play the notification sound."),
		"editable": map[string]any{
			"type": "array", "maxItems": maxEditableFields,
			"description": "Values the user may edit before approving, for example a draft reply. Each is {key, value}: key is a " + maxEditableKeyText + ", value any JSON value that is shown as the starting value. On approval the result's values holds the final value of each.",
			"items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"key"},
				"properties": map[string]any{"key": prop("string", "Field name."), "value": map[string]any{"description": "Current value (any JSON). Default null."}},
			},
		},
		"timeout_seconds": map[string]any{"type": "integer", "minimum": int(MinRequestTimeout.Seconds()), "maximum": int(MaxRequestTimeout.Seconds()),
			"description": fmt.Sprintf("How long the approval stays pending before it expires (default %d = 24 hours, max %d = 7 days).", int(DefaultRequestTimeout.Seconds()), int(MaxRequestTimeout.Seconds()))},
		"wait_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": int(MaxRequestWait.Seconds()),
			"description": fmt.Sprintf("How long this call waits for the decision (default and max %d). When it elapses undecided the call returns status pending with the approval_id and the approval stays open. 0 returns at once.", int(MaxRequestWait.Seconds()))},
	}
}

// requestApprovalTool is the hub's request_approval: an agent run (a flow agent or the chat assistant) asks the user
// for a decision through Host.Ask. It only asks, so it is allowed in read-only and dry runs.
func requestApprovalTool(host Host) Tool {
	schema := obj(requestApprovalProps(), "title", "description")
	return Tool{
		Name: RequestApprovalToolName,
		Description: "Ask the user for a decision before you act, or when you need a choice only they can make. " +
			"Opens an approval in the hal9001 Inbox (and in the chat, when you run in one) and waits up to wait_seconds. " +
			"description is required: say in one or two sentences what you want to do and why; the user sees it labelled as written by you, next to the real command or target. " +
			"Returns {approval_id, status, decision, comment, values, edited, edits, message}: decision is approved, denied or expired, or pending when the wait ended with no decision (the request stays open). " +
			"If the user denies with a comment, the comment is their steering and message repeats it: do not retry the same action unchanged, do what they asked instead. " +
			"If message says the steering limit was reached, stop and wait for the user. Values the user edited (editable) come back in values. You cannot decide the request yourself.",
		InputSchema: schema,
		Run: func(ctx context.Context, c Call) (Result, error) {
			a, err := parseArgs(schema, c.Args)
			if err != nil {
				return Result{}, err
			}
			q, err := parseQuestion(a)
			if err != nil {
				return Result{}, err
			}
			if c.Info.AgentRunID == "" {
				return Result{}, errors.New("request_approval needs an agent run")
			}
			req := AskRequest{
				RunID: c.Info.AgentRunID, NodeRunID: c.Info.NodeRunID,
				Title: q.Title, Description: q.Description, BodyMD: q.BodyMD, Priority: q.Priority,
				Timeout: q.Timeout, Wait: q.Wait,
			}
			for _, e := range q.Editable {
				req.Editable = append(req.Editable, AskEditable(e))
			}
			r, err := host.Ask(ctx, req)
			if err != nil {
				return Result{}, fmt.Errorf("tool request_approval: %w", err)
			}
			return jsonResult(askResultJSON(r))
		},
	}
}

// askResultJSON is the result of a hub request_approval call as the agent reads it.
func askResultJSON(r AskResult) map[string]any {
	res := map[string]any{"approval_id": r.ApprovalID, "status": r.Status, "decision": r.Status}
	if r.Status == "pending" {
		res["hint"] = "No decision yet. The request stays open in the Inbox; carry on without it or ask again later."
		return res
	}
	res["comment"] = r.Comment
	if r.Status == "denied" {
		if msg := DeniedText(r.Comment, r.Held); msg != "" {
			res["message"] = msg
		}
	}
	if r.Status != "approved" {
		return res
	}
	if len(r.Values) > 0 {
		res["values"] = r.Values
		res["edited"] = len(r.Edits) > 0
	}
	if len(r.Edits) > 0 {
		edits := make(map[string]any, len(r.Edits))
		for k, e := range r.Edits {
			edits[k] = map[string]json.RawMessage{"before": e.Before, "after": e.After}
		}
		res["edits"] = edits
	}
	return res
}

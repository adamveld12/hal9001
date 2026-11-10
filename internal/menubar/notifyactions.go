package menubar

import (
	"context"
	"errors"
	"fmt"
)

// ActionHandler carries out the user's responses to notifications through the daemon's REST API.
type ActionHandler struct {
	Client *Client
	// Open opens a URL in the browser.
	Open func(url string) error
	// Fail posts a notification telling the user an action did not work; nil drops it.
	Fail func(Request)
}

// Handle performs a. A click on the notification, and the Open button, open its deep link; the other buttons call the
// API. When that fails, Fail is called with a small notification that names the error, and the error is returned.
func (h ActionHandler) Handle(ctx context.Context, a NotificationAction) error {
	verb, err := h.run(ctx, a)
	if err != nil && h.Fail != nil {
		h.Fail(failureRequest(a, verb, err))
	}
	return err
}

// run returns a verb for the failure message ("approve") and the outcome.
func (h ActionHandler) run(ctx context.Context, a NotificationAction) (string, error) {
	info := a.UserInfo
	t := true
	switch a.Action {
	case DefaultAction, ActOpen:
		if info[InfoURL] == "" {
			return "open the notification", errors.New("it has no link")
		}
		return "open the notification", h.Open(info[InfoURL])
	case ActMarkRead:
		id, err := need(info, InfoItemID, "inbox item")
		if err != nil {
			return "mark it read", err
		}
		return "mark it read", h.Client.PatchInbox(ctx, id, InboxPatch{Read: &t})
	case ActArchive:
		id, err := need(info, InfoItemID, "inbox item")
		if err != nil {
			return "archive it", err
		}
		return "archive it", h.Client.PatchInbox(ctx, id, InboxPatch{Archived: &t})
	case ActApprove, ActApproveComment:
		id, err := need(info, InfoApprovalID, "approval")
		if err != nil {
			return "approve", err
		}
		return "approve", h.Client.Approve(ctx, id, commentOf(a, ActApproveComment))
	case ActDeny, ActDenyComment:
		id, err := need(info, InfoApprovalID, "approval")
		if err != nil {
			return "deny", err
		}
		return "deny", h.Client.Deny(ctx, id, commentOf(a, ActDenyComment))
	}
	return "handle the action", fmt.Errorf("unknown action %q", a.Action)
}

func need(info map[string]string, key, what string) (string, error) {
	if v := info[key]; v != "" {
		return v, nil
	}
	return "", fmt.Errorf("the notification carries no %s", what)
}

// commentOf is the typed text of a comment button; plain buttons send none.
func commentOf(a NotificationAction, textAction string) string {
	if a.Action == textAction {
		return a.Text
	}
	return ""
}

// failureRequest is the notification posted when an action failed. A click on it opens the original link.
func failureRequest(a NotificationAction, verb string, err error) Request {
	return Request{
		ID:       "failure-" + a.RequestID + "-" + a.Action,
		Title:    "hal9001",
		Subtitle: "Could not " + verb,
		Body:     err.Error(),
		Thread:   a.ThreadID,
		UserInfo: map[string]string{InfoURL: a.UserInfo[InfoURL]},
	}
}

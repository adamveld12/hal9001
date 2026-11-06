package engine

import "testing"

func TestNotificationPaths(t *testing.T) {
	e := &Engine{opt: Options{UIBaseURL: "http://localhost:5173/"}}
	if got := e.url(InboxPath("n1")); got != "http://localhost:5173/inbox/n1" {
		t.Errorf("note url = %q", got)
	}
	if got := e.url(ApprovalPath("a1")); got != "http://localhost:5173/inbox/approval/a1" {
		t.Errorf("approval url = %q", got)
	}
}

package menubar

import (
	"encoding/json"
	"sync"
)

// Tracker remembers which delivered notification belongs to which approval or inbox item, so that a notification can
// be withdrawn once the thing it is about was dealt with elsewhere (the web UI, the CLI, another notification).
type Tracker struct {
	mu        sync.Mutex
	approvals map[string][]string // approval id -> notification ids
	items     map[string][]string // inbox item id -> notification ids
}

// Track records a posted notification.
func (t *Tracker) Track(n Notification) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.approvals == nil {
		t.approvals, t.items = map[string][]string{}, map[string][]string{}
	}
	if n.ApprovalID != "" {
		t.approvals[n.ApprovalID] = append(t.approvals[n.ApprovalID], n.ID)
	}
	if n.ItemID != "" {
		t.items[n.ItemID] = append(t.items[n.ItemID], n.ID)
	}
}

// Withdrawn returns the notification ids to remove for a stream event: an approval.updated that is no longer pending,
// or an inbox.updated whose item is read or archived. It forgets what it returns.
func (t *Tracker) Withdrawn(e Event) []string {
	var env struct {
		Data struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Read     bool   `json:"read"`
			Archived bool   `json:"archived"`
		} `json:"data"`
	}
	if (e.Type != "approval.updated" && e.Type != "inbox.updated") || json.Unmarshal(e.Data, &env) != nil {
		return nil
	}
	d := env.Data
	t.mu.Lock()
	defer t.mu.Unlock()
	var m map[string][]string
	switch {
	case e.Type == "approval.updated" && d.Status != "pending":
		m = t.approvals
	case e.Type == "inbox.updated" && (d.Read || d.Archived):
		m = t.items
	default:
		return nil
	}
	ids := m[d.ID]
	delete(m, d.ID)
	return ids
}

package menubar

// Notification is one `notification.created` event of the daemon's stream (the app registers as the notification sink
// with the request header `X-Hal9001-Notifier: app`). Only events with Channel "app" are the app's to show; for the
// others another notifier already did.
type Notification struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // note | proposal | tool_call | request
	FlowID       string `json:"flow_id"`
	Title        string `json:"title"` // the flow id
	Subtitle     string `json:"subtitle"`
	Body         string `json:"body"`
	Priority     string `json:"priority"` // low | normal | high | urgent
	DryRun       bool   `json:"dry_run"`
	ThreadID     string `json:"thread_id"`
	URL          string `json:"url"` // absolute deep link into the web UI
	ItemID       string `json:"item_id"`
	ApprovalID   string `json:"approval_id"`
	ApprovalType string `json:"approval_type"`
	Editable     bool   `json:"editable"`
	CreatedAt    string `json:"created_at"`
	Channel      string `json:"channel"` // app | terminal-notifier | none
	Status       string `json:"status"`
	Error        string `json:"error"`
}

// NotificationAction is the user's response to a delivered notification: a click on it (Action is the system's default
// action identifier) or on one of its buttons, with Text when the button took typed input.
type NotificationAction struct {
	Action    string
	Text      string
	RequestID string // the id the notification was posted with
	ThreadID  string
}

// Notifier shows native macOS notifications and reports the user's responses. The implementation lives in the cgo
// bridge of cmd/hal9001-app (UNUserNotificationCenter); this interface is the seam between it and the Go logic, and
// has no implementation yet.
type Notifier interface {
	// Post delivers n; posting an id that is already delivered replaces it.
	Post(n Notification) error
	// Remove withdraws delivered notifications, for example once the item was handled elsewhere.
	Remove(ids ...string)
	// OnAction registers the handler called (on its own goroutine) for every response; it replaces any earlier one.
	OnAction(handle func(NotificationAction))
}

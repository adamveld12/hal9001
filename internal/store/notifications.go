package store

import (
	"context"
	"fmt"
	"time"
)

// Notification kinds.
const (
	NotifyNote     = "note"
	NotifyProposal = "proposal"
	NotifyToolCall = "tool_call"
	NotifyRequest  = "request"
)

// Notification delivery channels and statuses (Notification.Channel, Notification.Status).
const (
	ChannelApp              = "app"
	ChannelTerminalNotifier = "terminal-notifier"
	ChannelNone             = "none"

	NotifySent   = "sent"    // handed to the channel
	NotifyNoSink = "no_sink" // channel app, but no app was connected to receive it
	NotifyFailed = "failed"
	NotifyLogged = "logged" // channel none: recorded only
)

// Notification is one rendered notification: what the daemon raised for an inbox note or an approval, the channel
// it was routed to and the outcome. It is also the `data` of the `notification.created` stream event.
type Notification struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // note | proposal | tool_call | request
	FlowID   string `json:"flow_id"`
	Title    string `json:"title"`    // the flow id
	Subtitle string `json:"subtitle"` // what the notification is about
	Body     string `json:"body"`
	Priority string `json:"priority"` // low | normal | high | urgent
	DryRun   bool   `json:"dry_run"`
	ThreadID string `json:"thread_id"` // groups notifications of one flow
	URL      string `json:"url"`       // deep link into the UI
	ItemID   string `json:"item_id"`   // inbox item id (kind note)
	// ApprovalID and ApprovalType identify the approval (kinds proposal, tool_call, request).
	ApprovalID   string `json:"approval_id"`
	ApprovalType string `json:"approval_type"`
	// Editable is true for a request approval with editable fields.
	Editable  bool      `json:"editable"`
	CreatedAt time.Time `json:"created_at"`
	Channel   string    `json:"channel"`
	Status    string    `json:"status"`
	Error     string    `json:"error"`
}

const notificationCols = `id, kind, flow_id, title, subtitle, body, priority, dry_run, thread_id, url, item_id, approval_id, approval_type,
	editable, channel, status, error, created_at`

func scanNotification(r rowScanner) (Notification, error) {
	var (
		n         Notification
		dry, edit int
		created   string
	)
	if err := r.Scan(&n.ID, &n.Kind, &n.FlowID, &n.Title, &n.Subtitle, &n.Body, &n.Priority, &dry, &n.ThreadID, &n.URL, &n.ItemID,
		&n.ApprovalID, &n.ApprovalType, &edit, &n.Channel, &n.Status, &n.Error, &created); err != nil {
		return Notification{}, scanErr(err)
	}
	n.DryRun, n.Editable = dry != 0, edit != 0
	var err error
	n.CreatedAt, err = parseTS(created)
	return n, err
}

// CreateNotification inserts n, assigning ID and CreatedAt, and announces it as a change.
func (s *Store) CreateNotification(ctx context.Context, n *Notification) error {
	if n.Priority == "" {
		n.Priority = string(PriorityNormal)
	}
	if n.ID == "" {
		n.ID = s.NewID()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = s.Now()
	}
	return s.write(ctx, func(t *txn) error {
		_, err := t.ExecContext(ctx, `INSERT INTO notifications (`+notificationCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			n.ID, n.Kind, n.FlowID, n.Title, n.Subtitle, n.Body, n.Priority, b2i(n.DryRun), n.ThreadID, n.URL, n.ItemID,
			n.ApprovalID, n.ApprovalType, b2i(n.Editable), n.Channel, n.Status, n.Error, ts(n.CreatedAt))
		if err != nil {
			return fmt.Errorf("store: insert notification: %w", err)
		}
		t.note(ResNotification, n.ID, OpCreate)
		return nil
	})
}

// GetNotification returns one notification.
func (s *Store) GetNotification(ctx context.Context, id string) (*Notification, error) {
	n, err := scanNotification(s.rdb.QueryRowContext(ctx, "SELECT "+notificationCols+" FROM notifications WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// NotificationFilter selects notifications for ListNotifications.
type NotificationFilter struct {
	FlowID  string
	Channel string
	ListOpts
}

// ListNotifications lists notifications, newest first.
func (s *Store) ListNotifications(ctx context.Context, f NotificationFilter) (Page[Notification], error) {
	var w where
	if f.FlowID != "" {
		w.add("flow_id = ?", f.FlowID)
	}
	if f.Channel != "" {
		w.add("channel = ?", f.Channel)
	}
	return listPage(ctx, s.rdb, "SELECT "+notificationCols+" FROM notifications", &w, f.ListOpts, "id",
		scanNotification, func(n Notification) string { return n.ID })
}

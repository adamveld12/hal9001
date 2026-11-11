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

// ChannelApp is the Notification.Channel the app shows.
const ChannelApp = "app"

// Request is a notification as the native layer posts it. Everything the app needs to act on a response travels in
// UserInfo, because the response arrives without the original Notification.
type Request struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Subtitle string            `json:"subtitle"`
	Body     string            `json:"body"`
	Thread   string            `json:"thread"`   // groups notifications (UNNotificationContent.threadIdentifier)
	Category string            `json:"category"` // one of the Category* ids, or empty for a notification without buttons
	Sound    bool              `json:"sound"`
	UserInfo map[string]string `json:"userInfo"`
}

// UserInfo keys of a Request.
const (
	InfoURL          = "url"
	InfoKind         = "kind"
	InfoItemID       = "item_id"
	InfoApprovalID   = "approval_id"
	InfoApprovalType = "approval_type"
	InfoEditable     = "editable"
)

// Category ids registered with the notification center.
const (
	CategoryNote     = "hal.note"
	CategoryApproval = "hal.approval"
	CategoryEditable = "hal.request.editable"
)

// Notification action ids (buttons).
const (
	ActOpen     = "open"
	ActMarkRead = "mark_read"
	ActArchive  = "archive"
	ActApprove  = "approve"
	ActDeny     = "deny"
	// ActDenySteer is "Deny…": a denial with typed text, which is the steering the agent gets.
	ActDenySteer = "deny_steer"
)

// DefaultAction is the action id of a click on the notification itself (UNNotificationDefaultActionIdentifier).
const DefaultAction = "com.apple.UNNotificationDefaultActionIdentifier"

// ActionDef is one button of a category. Text buttons take typed input; for a denial its text is the steering comment.
type ActionDef struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Destructive bool   `json:"destructive,omitempty"`
	Text        bool   `json:"text,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	SendTitle   string `json:"sendTitle,omitempty"`
}

// CategoryDef is a set of buttons registered under an id.
type CategoryDef struct {
	ID      string      `json:"id"`
	Actions []ActionDef `json:"actions"`
}

// Categories is every category the app registers: notes get Open, Mark read, Archive; approvals (proposals, tool calls,
// requests) get Approve, Deny and Deny… (typed steering text); a request whose fields can be edited gets Approve as-is,
// Deny, Deny… and Open to edit.
func Categories() []CategoryDef {
	steer := ActionDef{ID: ActDenySteer, Title: "Deny…", Destructive: true, Text: true, Placeholder: "Additional steering", SendTitle: "Deny"}
	return []CategoryDef{
		{ID: CategoryNote, Actions: []ActionDef{
			{ID: ActOpen, Title: "Open"},
			{ID: ActMarkRead, Title: "Mark read"},
			{ID: ActArchive, Title: "Archive"},
		}},
		{ID: CategoryApproval, Actions: []ActionDef{
			{ID: ActApprove, Title: "Approve"},
			{ID: ActDeny, Title: "Deny", Destructive: true},
			steer,
		}},
		{ID: CategoryEditable, Actions: []ActionDef{
			{ID: ActApprove, Title: "Approve as-is"},
			{ID: ActDeny, Title: "Deny", Destructive: true},
			steer,
			{ID: ActOpen, Title: "Open to edit"},
		}},
	}
}

// CategoryFor picks the category of a notification; empty for a kind this app does not know (it is then shown without
// buttons and a click still opens its URL).
func CategoryFor(n Notification) string {
	switch n.Kind {
	case "note":
		return CategoryNote
	case "request":
		if n.Editable {
			return CategoryEditable
		}
		return CategoryApproval
	case "proposal", "tool_call":
		return CategoryApproval
	}
	return ""
}

// PlanNotification turns a stream event into what to post. ok is false when the notification is not the app's to show:
// another channel (terminal-notifier) already delivered it.
func PlanNotification(n Notification) (Request, bool) {
	if n.Channel != ChannelApp {
		return Request{}, false
	}
	return Request{
		ID:       n.ID,
		Title:    n.Title,
		Subtitle: n.Subtitle,
		Body:     n.Body,
		Thread:   n.ThreadID,
		Category: CategoryFor(n),
		Sound:    n.Priority == "high" || n.Priority == "urgent",
		UserInfo: map[string]string{
			InfoURL:          n.URL,
			InfoKind:         n.Kind,
			InfoItemID:       n.ItemID,
			InfoApprovalID:   n.ApprovalID,
			InfoApprovalType: n.ApprovalType,
			InfoEditable:     boolString(n.Editable),
		},
	}, true
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// NotificationAction is the user's response to a delivered notification: a click on it (Action is DefaultAction) or on
// one of its buttons, with Text when the button took typed input.
type NotificationAction struct {
	Action    string
	Text      string
	RequestID string            // the id the notification was posted with
	ThreadID  string            // the thread the notification was posted with
	UserInfo  map[string]string // the Request.UserInfo it was posted with
}

// Notifier shows native macOS notifications and reports the user's responses. The implementation is the cgo bridge of
// cmd/hal9001-app (UNUserNotificationCenter).
type Notifier interface {
	// Post delivers r; posting an id that is already delivered replaces it.
	Post(r Request) error
	// Remove withdraws delivered notifications, for example once the item was handled elsewhere.
	Remove(ids ...string)
	// OnAction registers the handler called (on its own goroutine) for every response; it replaces any earlier one.
	OnAction(handle func(NotificationAction))
}

// AuthStatus is UNAuthorizationStatus.
type AuthStatus int

const (
	AuthNotDetermined AuthStatus = 0
	AuthDenied        AuthStatus = 1
	AuthAuthorized    AuthStatus = 2
	AuthProvisional   AuthStatus = 3
	AuthEphemeral     AuthStatus = 4
)

// AlertStyle is UNAlertStyle: how a delivered notification is presented.
type AlertStyle int

const (
	StyleNone   AlertStyle = 0
	StyleBanner AlertStyle = 1
	StyleAlert  AlertStyle = 2
)

// NotifySettings is what the user has decided about this app's notifications (System Settings > Notifications),
// read from getNotificationSettings, never inferred from errors of adding a request.
type NotifySettings struct {
	Auth AuthStatus
	// Style is how notifications are presented.
	Style AlertStyle
	// AlertEnabled and CenterEnabled report the "alert" and "notification center" switches.
	AlertEnabled, CenterEnabled bool
}

// Pending reports that the user has not been asked yet (or has not answered).
func (s NotifySettings) Pending() bool { return s.Auth == AuthNotDetermined }

// Allowed reports that notifications can be shown: authorized, with at least one way to show them.
func (s NotifySettings) Allowed() bool {
	granted := s.Auth == AuthAuthorized || s.Auth == AuthProvisional || s.Auth == AuthEphemeral
	return granted && (s.AlertEnabled || s.CenterEnabled)
}

// Off reports that the user turned notifications off: denied, or allowed with every way of showing them disabled.
func (s NotifySettings) Off() bool { return !s.Pending() && !s.Allowed() }

// Sink reports whether the app should register as the daemon's notification sink. Only while notifications can really
// be shown: with the sink registered the daemon stops running terminal-notifier, so anything the app cannot show
// would be lost, which includes the time before the user answers the authorization prompt.
func (s NotifySettings) Sink() bool { return s.Allowed() }

// HintAlertStyle reports whether to suggest the Alerts style: an urgent notification was shown as a banner, which
// disappears by itself, while an alert stays until it is dealt with.
func (s NotifySettings) HintAlertStyle(n Notification) bool {
	return s.Allowed() && s.Style == StyleBanner && n.Priority == "urgent"
}

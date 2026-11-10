// Package menubar holds the platform-independent parts of the macOS app: the daemon API client, the menu model, the
// icon renderer and the native-notification interface. The cgo glue lives in cmd/hal9001-app.
package menubar

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Status is the subset of GET /api/status the menubar uses.
type Status struct {
	UnreadInbox      int `json:"unread_inbox"`
	PendingApprovals int `json:"pending_approvals"`
	RunningFlowRuns  int `json:"running_flow_runs"`
}

// Version is the subset of GET /api/version the menubar uses.
type Version struct {
	Version string `json:"version"`
	Home    string `json:"home"`
}

// Flow is the subset of a FlowSummary the menubar uses.
type Flow struct {
	ID      string `json:"id"`
	Enabled *bool  `json:"enabled"`
	Health  struct {
		Running int `json:"running"`
	} `json:"health"`
}

// Sensor is the subset of a SensorSummary the menubar uses.
type Sensor struct {
	ID     string `json:"id"`
	Status struct {
		Paused bool `json:"paused"`
	} `json:"status"`
}

// UpdateRelease is the part of a release the menu shows.
type UpdateRelease struct {
	Version string `json:"version"`
	Build   int    `json:"build"`
}

// UpdateStatus is the subset of GET /api/update the menubar uses. Phase and Error are empty when the daemon sends null.
type UpdateStatus struct {
	Enabled   bool           `json:"enabled"`
	State     string         `json:"state"`
	Available bool           `json:"available"`
	Pending   bool           `json:"pending"`
	Phase     string         `json:"phase"`
	Latest    *UpdateRelease `json:"latest"`
	Error     string         `json:"error"`
}

// Snapshot is everything one menu render needs. A nil Snapshot means the daemon is unreachable.
type Snapshot struct {
	Status  Status
	Version Version
	Flows   []Flow
	Sensors []Sensor
	// Update is nil when the daemon has no updater (an older daemon) or did not answer for it.
	Update *UpdateStatus
}

// Client is a thin client of the daemon REST API (localhost trust, no credentials).
type Client struct {
	Base string // API base URL without a trailing slash
	HTTP *http.Client
}

// NewClient returns a Client for base.
func NewClient(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 5 * time.Second}}
}

// errNotFound marks a GET the daemon answered with 404.
var errNotFound = errors.New("not found")

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return fmt.Errorf("GET %s: %s: %w", path, resp.Status, errNotFound)
	default:
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Fetch loads status, flows and sensors. Any failure means the daemon is unusable and is returned as an error. The
// version and the update status are informational: a daemon that cannot report them still counts as running.
func (c *Client) Fetch(ctx context.Context) (*Snapshot, error) {
	var s Snapshot
	var flows struct {
		Items []Flow `json:"items"`
	}
	var sensors struct {
		Items []Sensor `json:"items"`
	}
	if err := c.get(ctx, "/api/status", &s.Status); err != nil {
		return nil, err
	}
	if err := c.get(ctx, "/api/flows", &flows); err != nil {
		return nil, err
	}
	if err := c.get(ctx, "/api/sensors", &sensors); err != nil {
		return nil, err
	}
	_ = c.get(ctx, "/api/version", &s.Version)
	if u, ok, err := c.UpdateStatus(ctx); err == nil && ok {
		s.Update = &u
	}
	s.Flows, s.Sensors = flows.Items, sensors.Items
	return &s, nil
}

// UpdateStatus loads GET /api/update. The bool is false when the daemon does not know the endpoint (it is older than
// the updater): there is no update information then, and the menu shows none.
func (c *Client) UpdateStatus(ctx context.Context) (UpdateStatus, bool, error) {
	var st UpdateStatus
	err := c.get(ctx, "/api/update", &st)
	switch {
	case errors.Is(err, errNotFound):
		return UpdateStatus{}, false, nil
	case err != nil:
		return UpdateStatus{}, false, err
	}
	return st, true, nil
}

// refreshEvents are the SSE event types that can change what the menu shows.
const refreshEvents = "status.changed,killswitch.changed,sensor.changed,config.changed,inbox.created,inbox.updated,flow_run.created,flow_run.updated,approval.created,approval.updated"

// Stream header and event type of a notification sink.
const (
	NotifierHeader    = "X-Hal9001-Notifier"
	NotifierApp       = "app"
	EventNotification = "notification.created"
)

// Stream header and event types of the updater. Every connection may subscribe to update.status. The connection that
// executes installs registers with the header and subscribes to update.install, which only it receives.
const (
	UpdaterHeader      = "X-Hal9001-Updater"
	UpdaterApp         = "app"
	EventUpdateStatus  = "update.status"
	EventUpdateInstall = "update.install"
)

// StreamOptions configures Stream.
type StreamOptions struct {
	// Notifications registers the connection as the daemon's notification sink: it adds notification.created to the
	// event types and announces the app with the X-Hal9001-Notifier header. While a sink is connected the daemon stops
	// running terminal-notifier itself, so set it only when the app can really show notifications.
	Notifications bool
	// Updater registers the connection as the daemon's update installer: it adds update.install to the event types and
	// announces the app with the X-Hal9001-Updater header. The daemon then asks this connection to install updates, so
	// set it only when the app can really replace itself.
	Updater bool
}

// Event is one message of the stream.
type Event struct {
	Type string
	// Data is the JSON envelope of the message (`{"type":...,"data":{...}}`); empty for events without one.
	Data []byte
}

// Notification decodes the payload of a notification.created event.
func (e Event) Notification() (Notification, error) {
	var env struct {
		Data Notification `json:"data"`
	}
	err := json.Unmarshal(e.Data, &env)
	return env.Data, err
}

// Payload decodes the data of the event's envelope into v.
func (e Event) Payload(v any) error {
	env := struct {
		Data any `json:"data"`
	}{Data: v}
	return json.Unmarshal(e.Data, &env)
}

// Stream connects to GET /api/stream and calls onEvent for each message received (including stream.resync) until the
// connection ends or ctx is done. It returns the connection error, never nil.
//
// A daemon from before the updater answers 400 to the update event types. That is the normal state for up to two
// minutes after the app replaced itself, while the old daemon still runs, so Stream then connects again without them
// (and without the installer registration; the notification sink keeps working).
func (c *Client) Stream(ctx context.Context, opts StreamOptions, onEvent func(Event)) error {
	err := c.stream(ctx, opts, true, onEvent)
	var refused *APIError
	if errors.As(err, &refused) && refused.Status == http.StatusBadRequest {
		return c.stream(ctx, opts, false, onEvent)
	}
	return err
}

// stream is one connection; updates says whether to ask for the update events.
func (c *Client) stream(ctx context.Context, opts StreamOptions, updates bool, onEvent func(Event)) error {
	types := refreshEvents
	installer := updates && opts.Updater
	if updates {
		types += "," + EventUpdateStatus
	}
	if installer {
		types += "," + EventUpdateInstall
	}
	if opts.Notifications {
		types += "," + EventNotification
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/api/stream?types="+types, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if opts.Notifications {
		req.Header.Set(NotifierHeader, NotifierApp)
	}
	if installer {
		req.Header.Set(UpdaterHeader, UpdaterApp)
	}
	hc := &http.Client{} // no overall timeout: the stream is long-lived
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream: %w", problemOf(resp))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var ev Event
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "": // end of a message
			if ev.Type != "" {
				onEvent(ev)
			}
			ev = Event{}
		case strings.HasPrefix(line, "event: "):
			ev.Type = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.Data = []byte(strings.TrimPrefix(line, "data: "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("stream closed")
}

// APIError is a request the daemon refused: the HTTP status and the detail of its problem document.
type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string { return fmt.Sprintf("%s (%d)", e.Detail, e.Status) }

// problemOf reads the problem document of an error response (status 400 or more).
func problemOf(resp *http.Response) *APIError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var problem struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(raw, &problem)
	msg := strings.TrimSpace(problem.Detail)
	if msg == "" {
		msg = strings.TrimSpace(problem.Title)
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{Status: resp.StatusCode, Detail: msg}
}

// send sends a JSON request that expects no response body (none is sent when body is nil); any status of 400 or more
// is an *APIError naming the daemon's problem document.
func (c *Client) send(ctx context.Context, method, path string, body any) error {
	return c.sendVia(ctx, c.HTTP, method, path, body)
}

func (c *Client) sendVia(ctx context.Context, hc *http.Client, method, path string, body any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 400 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return problemOf(resp)
}

// Decision is the body of an approve or deny request.
type Decision struct {
	Comment string `json:"comment,omitempty"`
}

// Approve approves a proposal, tool call or request as it is (POST /api/approvals/{id}/approve).
func (c *Client) Approve(ctx context.Context, id, comment string) error {
	return c.send(ctx, http.MethodPost, "/api/approvals/"+url.PathEscape(id)+"/approve", Decision{Comment: comment})
}

// Deny denies an approval (POST /api/approvals/{id}/deny).
func (c *Client) Deny(ctx context.Context, id, comment string) error {
	return c.send(ctx, http.MethodPost, "/api/approvals/"+url.PathEscape(id)+"/deny", Decision{Comment: comment})
}

// InboxPatch changes flags of an inbox item; nil fields stay as they are.
type InboxPatch struct {
	Read     *bool `json:"read,omitempty"`
	Archived *bool `json:"archived,omitempty"`
}

// PatchInbox updates an inbox item (PATCH /api/inbox/{id}).
func (c *Client) PatchInbox(ctx context.Context, id string, p InboxPatch) error {
	return c.send(ctx, http.MethodPatch, "/api/inbox/"+url.PathEscape(id), p)
}

// checkTimeout is how long CheckUpdate waits: the daemon answers when it has fetched the update feed, which can take
// longer than the few seconds of a status call, and the check goes on in the daemon even if the app stops waiting.
const checkTimeout = 30 * time.Second

// CheckUpdate asks the daemon to look for a new build now (POST /api/update/check).
func (c *Client) CheckUpdate(ctx context.Context) error {
	hc := *c.HTTP
	hc.Timeout = checkTimeout
	return c.sendVia(ctx, &hc, http.MethodPost, "/api/update/check", nil)
}

// InstallUpdate asks the daemon to install the available update (POST /api/update/install). It installs at once, or
// once no flow is running. The body is empty on purpose: the app never forces an install over running flows. A refusal
// (409: nothing to install, updates off, no installer connected) is an *APIError whose Detail is fit to show.
func (c *Client) InstallUpdate(ctx context.Context) error {
	return c.send(ctx, http.MethodPost, "/api/update/install", struct{}{})
}

// ReportUpdate tells the daemon how the install this app executes is going (POST /api/update/report): state is
// downloading, verifying, installing or failed, and errMsg says why for failed.
func (c *Client) ReportUpdate(ctx context.Context, state, errMsg string) error {
	return c.send(ctx, http.MethodPost, "/api/update/report", struct {
		State string `json:"state"`
		Error string `json:"error,omitempty"`
	}{State: state, Error: errMsg})
}

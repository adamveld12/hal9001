// Package menubar holds the platform-independent parts of the macOS app: the daemon API client, the menu model, the
// icon renderer and the native-notification interface. The cgo glue lives in cmd/hal9001-app.
package menubar

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

// Snapshot is everything one menu render needs. A nil Snapshot means the daemon is unreachable.
type Snapshot struct {
	Status  Status
	Version Version
	Flows   []Flow
	Sensors []Sensor
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
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Fetch loads status, flows and sensors. Any failure means the daemon is unusable and is returned as an error. The
// version is informational: a daemon that cannot report it still counts as running.
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
	s.Flows, s.Sensors = flows.Items, sensors.Items
	return &s, nil
}

// refreshEvents are the SSE event types that can change what the menu shows.
const refreshEvents = "status.changed,killswitch.changed,sensor.changed,config.changed,inbox.created,inbox.updated,flow_run.created,flow_run.updated,approval.created,approval.updated"

// Stream header and event type of a notification sink.
const (
	NotifierHeader    = "X-Hal9001-Notifier"
	NotifierApp       = "app"
	EventNotification = "notification.created"
)

// StreamOptions configures Stream.
type StreamOptions struct {
	// Notifications registers the connection as the daemon's notification sink: it adds notification.created to the
	// event types and announces the app with the X-Hal9001-Notifier header. While a sink is connected the daemon stops
	// running terminal-notifier itself, so set it only when the app can really show notifications.
	Notifications bool
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

// Stream connects to GET /api/stream and calls onEvent for each message received (including stream.resync) until the
// connection ends or ctx is done. It returns the connection error, never nil.
func (c *Client) Stream(ctx context.Context, opts StreamOptions, onEvent func(Event)) error {
	types := refreshEvents
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
	hc := &http.Client{} // no overall timeout: the stream is long-lived
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream: %s", resp.Status)
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

// send sends a JSON request that expects no response body; any status of 400 or more is an error naming the daemon's
// problem document.
func (c *Client) send(ctx context.Context, method, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 400 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
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
	return fmt.Errorf("%s (%d)", msg, resp.StatusCode)
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

// Package menubar holds the platform-independent parts of the macOS menubar app: the daemon API client, the menu
// model and the icon renderer. The cgo systray glue lives in cmd/hal9001-menubar.
package menubar

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Status is the subset of GET /api/status the menubar uses.
type Status struct {
	UnreadInbox      int `json:"unread_inbox"`
	PendingApprovals int `json:"pending_approvals"`
	RunningFlowRuns  int `json:"running_flow_runs"`
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

// Fetch loads status, flows and sensors. Any failure means the daemon is unusable and is returned as an error.
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
	s.Flows, s.Sensors = flows.Items, sensors.Items
	return &s, nil
}

// refreshEvents are the SSE event types that can change what the menu shows.
const refreshEvents = "status.changed,killswitch.changed,sensor.changed,config.changed,inbox.created,inbox.updated,flow_run.created,flow_run.updated,approval.created,approval.updated"

// Stream connects to GET /api/stream and calls onEvent for each event type received (including stream.resync)
// until the connection ends or ctx is done. It returns the connection error, never nil.
func (c *Client) Stream(ctx context.Context, onEvent func(typ string)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/api/stream?types="+refreshEvents, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
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
	for sc.Scan() {
		if typ, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			onEvent(typ)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("stream closed")
}

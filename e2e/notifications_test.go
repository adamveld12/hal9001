package e2e

import (
	"bufio"
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// notifyStream follows GET /api/stream?types=notification.created and keeps the data lines it receives.
type notifyStream struct {
	mu     sync.Mutex
	data   []string
	cancel context.CancelFunc
}

// openNotifyStream opens the notification stream; asSink adds the header that registers the connection as the app sink.
func (d *Daemon) openNotifyStream(asSink bool) *notifyStream {
	d.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL()+"/api/stream?types=notification.created", nil)
	if err != nil {
		d.t.Fatal(err)
	}
	if asSink {
		req.Header.Set("X-Hal9001-Notifier", "app")
	}
	if d.Cookie != "" {
		req.Header.Set("Cookie", d.Cookie)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine
	if err != nil {
		cancel()
		d.t.Fatalf("GET /api/stream: %v", err)
	}
	s := &notifyStream{cancel: cancel}
	ready := make(chan struct{})
	go func() {
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		var once sync.Once
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "event: stream.ready") {
				once.Do(func() { close(ready) })
			}
			if rest, ok := strings.CutPrefix(line, "data: "); ok && strings.Contains(rest, `"notification.created"`) {
				s.mu.Lock()
				s.data = append(s.data, rest)
				s.mu.Unlock()
			}
		}
	}()
	<-ready
	d.t.Cleanup(cancel)
	return s
}

func (s *notifyStream) events() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.data...)
}

type notificationRow struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	FlowID   string `json:"flow_id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Body     string `json:"body"`
	Priority string `json:"priority"`
	ThreadID string `json:"thread_id"`
	URL      string `json:"url"`
	ItemID   string `json:"item_id"`
	Channel  string `json:"channel"`
	Status   string `json:"status"`
}

func (d *Daemon) notificationLog() []notificationRow {
	d.t.Helper()
	var l struct {
		Items []notificationRow `json:"items"`
	}
	d.Get("/api/notifications?limit=50").Want(200).JSON(&l)
	return l.Items
}

const notifyFlow = `id: tell
enabled: true
subscribe: [src]
nodes:
  - {id: note, type: inbox, title: "Hello {{.input.id}}", priority: high, summary_md: "**Bold** summary\nsecond line"}
`

// Without a connected app, an inbox note is logged, announced on the stream and delivered by terminal-notifier
// with the rich arguments.
func TestNotificationLoggedStreamedAndSentByTerminalNotifier(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("tell", notifyFlow)
	d.Start()
	stream := d.openNotifyStream(false)

	d.RunOf("src", map[string]any{"id": "7"})
	it := d.Inbox()[0]
	d.Eventually("the notification log row", func() bool { return len(d.notificationLog()) == 1 })
	row := d.notificationLog()[0]
	if row.Kind != "note" || row.FlowID != "tell" || row.Title != "tell" || row.Subtitle != "Hello 7" || row.Body != "Bold summary\nsecond line" ||
		row.Priority != "high" || row.ThreadID != "tell" || row.ItemID != it.ID || row.URL != "http://localhost:"+strconv.Itoa(d.Port())+"/inbox/"+it.ID ||
		row.Channel != "terminal-notifier" || row.Status != "sent" {
		t.Errorf("log row: %+v", row)
	}
	d.Eventually("the stream event", func() bool { return len(stream.events()) == 1 })
	if ev := stream.events()[0]; !strings.Contains(ev, `"id":"`+row.ID+`"`) || !strings.Contains(ev, `"subtitle":"Hello 7"`) || !strings.Contains(ev, `"channel":"terminal-notifier"`) {
		t.Errorf("stream event: %s", ev)
	}
	d.Eventually("terminal-notifier run", func() bool { return len(d.Notifications()) == 1 })
	n := d.Notifications()[0]
	if n["title"] != "tell" || n["subtitle"] != "Hello 7" || n["message"] != "Bold summary\nsecond line" || n["group"] != "hal9001:tell" ||
		n["sound"] != "default" || n["open"] != row.URL || !strings.HasSuffix(n["contentImage"], "hal-eye.png") {
		t.Errorf("terminal-notifier args: %v", n)
	}
}

// With an app sink connected (notify_via auto) terminal-notifier stays unused; the row records channel app.
func TestNotificationGoesToAppSinkNotTerminalNotifier(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("tell", notifyFlow)
	d.Start()
	sink := d.openNotifyStream(true)

	d.RunOf("src", map[string]any{"id": "8"})
	d.Eventually("the notification log row", func() bool { return len(d.notificationLog()) == 1 })
	if row := d.notificationLog()[0]; row.Channel != "app" || row.Status != "sent" {
		t.Errorf("log row: %+v", row)
	}
	d.Eventually("the sink's event", func() bool { return len(sink.events()) == 1 })
	if n := d.Notifications(); len(n) != 0 {
		t.Errorf("terminal-notifier ran with an app sink connected: %v", n)
	}
}

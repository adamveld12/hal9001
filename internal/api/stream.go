package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"hal9001/internal/apispec"
	"hal9001/internal/auth"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/update"
)

// SSE hub design (GET /api/stream)
//
// The store calls the notifier after every commit, on the committing
// goroutine. The notifier only does a non-blocking send into hub.in, so
// writers never wait on SSE clients. One goroutine (hub.run) drains hub.in,
// reads the changed record from the store, converts it to the contract's
// envelope, stamps it with the next sequence number, appends it to a ring of
// the last ringSize messages and offers it to every connected client.
//
// Backpressure never blocks other clients or the store:
//   - Each client has a bounded queue (clientBuffer messages). When a client is
//     too slow and its queue is full, the queue is emptied and the client is
//     flagged. Before its next message the client receives one
//     `stream.resync` (reason `buffer_overrun`) and must refetch what it
//     displays. Messages are dropped, never reordered.
//   - If hub.in overflows (the hub itself fell behind), the changes that did
//     not fit are lost; the hub then broadcasts `stream.resync` to everyone.
//   - A reconnecting client sends Last-Event-ID. Messages still in the ring are
//     replayed; if the gap is larger than the ring, or the id comes from a
//     previous daemon run, the client gets `stream.resync` instead.
//   - Bulk deletes by retention pruning are broadcast as `stream.resync`
//     (reason `pruned`).
//
// Sequence numbers start at the daemon's start time in milliseconds times
// 1000, so ids keep increasing across daemon restarts.
const (
	heartbeatInterval = 15 * time.Second
	ringSize          = 1024
	clientBuffer      = 256
	ingestBuffer      = 8192
)

// NotifierHeader marks a stream connection as a notification sink when its value is NotifierApp.
const (
	NotifierHeader = "X-Hal9001-Notifier"
	NotifierApp    = "app"
)

// controlTypes are always delivered, whatever `types` says.
var controlTypes = map[string]bool{"stream.ready": true, "stream.resync": true}

// streamTypes returns the event types the contract lists for /api/stream.
func streamTypes() map[string]bool {
	out := map[string]bool{}
	item := apispec.MustLoad().Paths.Find("/api/stream")
	if item == nil || item.Get == nil {
		return out
	}
	if m, ok := item.Get.Extensions[apispec.ExtSSEEvents].(map[string]any); ok {
		for k := range m {
			out[k] = true
		}
	}
	return out
}

type streamMsg struct {
	seq   uint64
	typ   string
	data  []byte // the JSON envelope
	meta  map[string]string
	admin bool // only for callers granted the admin service
	// installer messages go to installer connections only and are never replayed: a stale install order must not
	// start a second install after a reconnect.
	installer bool
}

type envelope struct {
	Type string          `json:"type"`
	ID   string          `json:"id"`
	TS   time.Time       `json:"ts"`
	Data json.RawMessage `json:"data"`
}

type client struct {
	types   map[string]bool // nil: every type
	filters map[string]string
	admin   bool
	// installer marks the connection that executes updates (header X-Hal9001-Updater: app).
	installer bool

	mu      sync.Mutex
	q       []streamMsg
	dropped bool
	wake    chan struct{}
}

func (c *client) wants(m streamMsg) bool {
	if controlTypes[m.typ] {
		return true
	}
	if m.admin && !c.admin {
		return false
	}
	if m.installer && !c.installer {
		return false
	}
	if c.types != nil && !c.types[m.typ] {
		return false
	}
	// A resource filter applies to the types that carry that resource and
	// leaves the others untouched.
	for k, want := range c.filters {
		if got, ok := m.meta[k]; ok && got != want {
			return false
		}
	}
	return true
}

func (c *client) push(m streamMsg) {
	if !c.wants(m) {
		return
	}
	c.mu.Lock()
	if len(c.q) >= clientBuffer {
		c.q = c.q[:0]
		c.dropped = true
	}
	c.q = append(c.q, m)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *client) take() ([]streamMsg, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q, d := c.q, c.dropped
	c.q, c.dropped = nil, false
	return q, d
}

type hub struct {
	s        *Server
	in       chan store.Change
	overflow atomic.Bool
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	finished chan struct{}

	mu      sync.Mutex
	seq     uint64
	base    uint64
	ring    []streamMsg
	clients map[*client]struct{}

	// owned by the run goroutine
	lastStatus    []byte
	pendingResync string
}

func newHub(s *Server) *hub {
	ctx, cancel := context.WithCancel(context.Background())
	base := uint64(time.Now().UnixMilli()) * 1000 //nolint:gosec // wall clock is positive
	return &hub{
		s: s, in: make(chan store.Change, ingestBuffer), ctx: ctx, cancel: cancel,
		done: make(chan struct{}), finished: make(chan struct{}),
		seq: base, base: base, clients: map[*client]struct{}{},
	}
}

func (h *hub) enqueue(c store.Change) {
	select {
	case h.in <- c:
	default:
		h.overflow.Store(true)
	}
}

func (h *hub) stop() {
	close(h.done)
	h.cancel()
	<-h.finished
}

// publish stamps and delivers one message.
func (h *hub) publish(typ string, data any, meta map[string]string, admin bool) {
	h.publishMsg(typ, data, meta, admin, false)
}

func (h *hub) publishMsg(typ string, data any, meta map[string]string, admin, installer bool) {
	raw, err := json.Marshal(data)
	if err != nil {
		h.s.log().Error("api: encode stream event", "type", typ, "err", err)
		return
	}
	h.mu.Lock()
	h.seq++
	id := strconv.FormatUint(h.seq, 10)
	body, err := json.Marshal(envelope{Type: typ, ID: id, TS: time.Now().UTC(), Data: raw})
	if err != nil {
		h.mu.Unlock()
		return
	}
	m := streamMsg{seq: h.seq, typ: typ, data: body, meta: meta, admin: admin, installer: installer}
	if !installer {
		if len(h.ring) >= ringSize {
			h.ring[0] = streamMsg{}
			h.ring = h.ring[1:]
		}
		h.ring = append(h.ring, m)
	}
	for c := range h.clients {
		c.push(m)
	}
	h.mu.Unlock()
}

// subscribe registers c and returns the messages to replay after lastID. The
// registration and the ring snapshot happen under one lock, so no message is
// lost or duplicated between replay and live delivery. reason is non-empty
// when the client must resync instead.
func (h *hub) subscribe(c *client, lastID string) (replay []streamMsg, reason string, current uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
	current = h.seq
	if lastID == "" {
		return nil, "", current
	}
	last, err := strconv.ParseUint(lastID, 10, 64)
	if err != nil || last < h.base || last > h.seq {
		return nil, "daemon_restarted", current
	}
	first := h.seq + 1
	if len(h.ring) > 0 {
		first = h.ring[0].seq
	}
	if last+1 < first {
		return nil, "buffer_overrun", current
	}
	for _, m := range h.ring {
		if m.seq > last && c.wants(m) {
			replay = append(replay, m)
		}
	}
	return replay, "", current
}

func (h *hub) unsubscribe(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// snapshot reports the newest buffered id for stream.ready.
func (h *hub) snapshot() (current uint64, buffered bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq, len(h.ring) > 0
}

func (h *hub) run() {
	defer close(h.finished)
	if sv, err := h.s.statusView(h.ctx); err == nil {
		h.lastStatus, _ = json.Marshal(sv)
	}
	for {
		select {
		case <-h.done:
			return
		case c := <-h.in:
			dirty := h.handle(c)
		drain:
			for {
				select {
				case c := <-h.in:
					dirty = h.handle(c) || dirty
				default:
					break drain
				}
			}
			if h.overflow.Swap(false) {
				h.pendingResync, dirty = "buffer_overrun", true
			}
			if h.pendingResync != "" {
				h.publish("stream.resync", map[string]string{"reason": h.pendingResync}, nil, false)
				h.pendingResync = ""
			}
			if dirty {
				h.publishStatus()
			}
		}
	}
}

func (h *hub) publishStatus() {
	sv, err := h.s.statusView(h.ctx)
	if err != nil {
		return
	}
	b, err := json.Marshal(sv)
	if err != nil || bytes.Equal(b, h.lastStatus) {
		return
	}
	h.lastStatus = b
	h.publish("status.changed", sv, nil, false)
}

func (h *hub) logRead(what, id string, err error) {
	if h.ctx.Err() == nil {
		h.s.log().Warn("api: stream: cannot read changed record", "what", what, "id", id, "err", err)
	}
}

// handle converts one store change into a stream message. It reports whether
// the header counters may have changed.
func (h *hub) handle(c store.Change) bool {
	if c.ID == "*" {
		h.pendingResync = "pruned"
		return true
	}
	ctx, st := h.ctx, h.s.opts.Store
	rv := h.s.resolver(ctx)
	kind := "updated"
	if c.Op == store.OpCreate {
		kind = "created"
	}
	switch c.Resource {
	case store.ResSetting:
		if c.ID != store.KillSwitchKey && c.ID != killSwitchReasonKey {
			return false
		}
		if v, err := h.s.killSwitchView(ctx); err == nil {
			h.publish("killswitch.changed", v, nil, false)
		}
		return true
	case store.ResSensorState:
		f := h.s.opts.Workspace().Sensor(c.ID)
		if f == nil {
			return false
		}
		ks, err := st.GetKillSwitch(ctx)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		status, err := h.s.sensorStatus(ctx, f.Name, f.Value, f.Valid(), ks.Paused)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("sensor.changed", sensorChangedView{ID: f.Name, Status: status}, map[string]string{"sensor_id": f.Name}, false)
	case store.ResSensorRun:
		r, err := st.GetSensorRun(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("sensor_run."+kind, h.s.sensorRunView(*r, true), map[string]string{"sensor_id": r.SensorID}, false)
	case store.ResEvent:
		if c.Op != store.OpCreate {
			return false
		}
		ev, err := st.GetEvent(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		runs, err := st.ListFlowRuns(ctx, store.FlowRunFilter{EventID: ev.ID, ListOpts: store.ListOpts{Limit: maxLimit, Oldest: true}})
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("event.created", h.s.eventSummary(*ev, runs.Items), map[string]string{"sensor_id": ev.SensorID}, false)
	case store.ResFlowRun:
		fr, err := st.GetFlowRun(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("flow_run."+kind, flowRunFull(*fr), map[string]string{"flow_id": fr.FlowID, "flow_run_id": fr.ID}, false)
		return true
	case store.ResNodeRun:
		nr, err := st.GetNodeRun(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		meta := map[string]string{"flow_run_id": nr.FlowRunID}
		if fr, _ := rv.flowRun(nr.FlowRunID); fr != nil {
			meta["flow_id"] = fr.FlowID
		}
		h.publish("node_run."+kind, rv.nodeRunView(*nr, true), meta, false)
	case store.ResAgentRun:
		ar, err := st.GetAgentRun(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		v, err := rv.agentRunView(*ar, true)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("agent_run."+kind, v, optionalMeta(map[string]string{
			"agent_run_id": ar.ID, "flow_run_id": deref(v.FlowRunID), "flow_id": deref(v.FlowID), "assist_session_id": ar.AssistSessionID,
		}), false)
		return true
	case store.ResAgentEvent:
		if c.Op != store.OpCreate {
			return false
		}
		runID, seqText, _ := strings.Cut(c.ID, "/")
		seq, err := strconv.ParseInt(seqText, 10, 64)
		if err != nil {
			return false
		}
		evs, err := st.ListAgentEvents(ctx, store.AgentEventFilter{AgentRunID: runID, AfterSeq: seq - 1, Limit: 1})
		if err != nil || len(evs) == 0 || evs[0].Seq != seq {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("agent_event.appended", h.s.agentEventView(evs[0], true), map[string]string{"agent_run_id": runID}, false)
	case store.ResPermissionRequest:
		p, err := st.GetPermissionRequest(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		v, err := rv.permissionRequestView(*p)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("permission_request."+kind, v, map[string]string{"agent_run_id": p.AgentRunID}, false)
	case store.ResApproval:
		a, err := st.GetApproval(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		v, err := rv.approvalView(*a)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("approval."+kind, v, optionalMeta(map[string]string{
			"agent_run_id": a.AgentRunID, "flow_run_id": deref(v.FlowRunID), "flow_id": deref(v.FlowID), "assist_session_id": deref(v.AssistSessionID),
		}), false)
		return true
	case store.ResInboxItem:
		it, err := st.GetInboxItem(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		v, err := rv.inboxView(*it)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("inbox."+kind, v, map[string]string{"flow_run_id": v.FlowRunID, "flow_id": v.FlowID}, false)
		return true
	case store.ResNotification:
		n, err := st.GetNotification(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("notification.created", n, optionalMeta(map[string]string{"flow_id": n.FlowID}), false)
		return false
	case store.ResAssistSession:
		a, err := st.GetAssistSession(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("assist.updated", assistSession(*a), map[string]string{"assist_session_id": a.ID}, false)
	case store.ResAssistMessage:
		m, err := st.GetAssistMessage(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("assist.message", assistStreamMessageView{assistMessageView: assistMessage(*m), SessionID: m.SessionID}, map[string]string{"assist_session_id": m.SessionID}, false)
	case store.ResAuthEvent:
		if c.Op != store.OpCreate {
			return false
		}
		e, err := st.GetAuthEvent(ctx, c.ID)
		if err != nil {
			h.logRead(c.Resource, c.ID, err)
			return false
		}
		h.publish("auth_event.created", authEvent(*e), nil, true)
	}
	return false
}

// sensorChangedView is the data of `sensor.changed`: a sensor's status moved
// (paused or resumed, or a poll recorded its outcome).
type sensorChangedView struct {
	ID     string           `json:"id"`
	Status sensorStatusView `json:"status"`
}

// ---- config changes ----

type configChangeView struct {
	Kind    string  `json:"kind"`
	ID      *string `json:"id"`
	Hash    *string `json:"hash"`
	Deleted bool    `json:"deleted"`
}

// PublishConfigChange announces files that changed under the home directory
// as `config.changed` stream events. paths are home-relative as reported by
// spec.Watch; paths that are not configuration files are ignored. The API
// calls it after its own writes; the daemon calls it for hot reloads of edits
// made on disk.
func (s *Server) PublishConfigChange(paths []string) {
	seen := map[string]bool{}
	for _, p := range paths {
		k, id, ok := spec.ClassifyPath(p)
		if !ok || seen[p] {
			continue
		}
		seen[p] = true
		v := configChangeView{Kind: string(k)}
		if k == spec.KindConfig {
			v.Kind = "settings"
		}
		if id != "" {
			v.ID = &id
		}
		if _, hash, err := spec.ReadFile(s.opts.Home, k, id); err == nil {
			// A hash of a secrets file would let a stream reader test guesses
			// against it, so per-owner env files announce no hash.
			if k != spec.KindSensorEnv && k != spec.KindToolEnv && k != spec.KindSandboxEnv {
				v.Hash = &hash
			}
		} else {
			v.Deleted = true
		}
		s.hub.publish("config.changed", v, nil, false)
	}
}

// publishFileChange announces one file written or deleted through the API.
func (s *Server) publishFileChange(k spec.Kind, id string) {
	s.PublishConfigChange([]string{k.Rel(id)})
}

// ---- handler ----

// discardWriter lets the admin probe run Require without producing output.
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (d *discardWriter) WriteHeader(int)             {}

// callerIsAdmin reports whether the request would pass the admin service.
func (s *Server) callerIsAdmin(r *http.Request) bool {
	ok := false
	s.opts.Auth.Require(auth.ServiceAdmin)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ok = true })).
		ServeHTTP(&discardWriter{h: http.Header{}}, r)
	return ok
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	known := streamTypes()
	var types map[string]bool
	for _, t := range q.list("types") {
		if !known[t] {
			q.fail("unknown event type %q", t)
			continue
		}
		if types == nil {
			types = map[string]bool{}
		}
		types[t] = true
	}
	filters := map[string]string{}
	if v := q.slug("sensor_id"); v != "" {
		filters["sensor_id"] = v
	}
	if v := q.slug("flow_id"); v != "" {
		filters["flow_id"] = v
	}
	if v := q.ulid("flow_run_id"); v != "" {
		filters["flow_run_id"] = v
	}
	if v := q.ulid("agent_run_id"); v != "" {
		filters["agent_run_id"] = v
	}
	if v := q.ulid("assist_session_id"); v != "" {
		filters["assist_session_id"] = v
	}
	if !q.ok(w) {
		return
	}
	c := &client{types: types, filters: filters, wake: make(chan struct{}, 1)}
	if types == nil || types["auth_event.created"] {
		c.admin = s.callerIsAdmin(r)
	}
	// The installer (the macOS app, which downloads and swaps the bundle) announces itself with a header and must be
	// granted the admin service, since it acts on update.install. It is counted as connected only after it is
	// subscribed, so an install order cannot be published before it can receive it.
	c.installer = s.opts.Update != nil && r.Header.Get(update.InstallerHeader) == update.InstallerApp &&
		(types == nil || types[update.EventInstall]) && s.callerIsAdmin(r)

	// A notification sink (the macOS app) announces itself with a header; while it is connected the daemon leaves
	// notification delivery to it (notify_via: auto).
	if r.Header.Get(NotifierHeader) == NotifierApp && s.opts.Sinks != nil && (types == nil || types["notification.created"]) {
		defer s.opts.Sinks.Connect()()
	}

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	_ = rc.SetWriteDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}

	replay, reason, current := s.hub.subscribe(c, r.Header.Get("Last-Event-ID"))
	defer s.hub.unsubscribe(c)

	last := ""
	if _, buffered := s.hub.snapshot(); buffered {
		last = strconv.FormatUint(current, 10)
	}
	ready := envelope{Type: "stream.ready", ID: orZero(last), TS: time.Now().UTC()}
	ready.Data, _ = json.Marshal(map[string]string{
		"server_time": time.Now().UTC().Format(time.RFC3339Nano), "version": s.opts.Version, "last_event_id": last,
	})
	if !writeSSE(w, rc, last, "stream.ready", mustJSON(ready)) {
		return
	}
	if reason != "" && !s.writeResync(w, rc, current, reason) {
		return
	}
	for _, m := range replay {
		if !writeSSE(w, rc, strconv.FormatUint(m.seq, 10), m.typ, m.data) {
			return
		}
	}
	if c.installer {
		defer s.opts.Update.ConnectInstaller(r.Context())()
	}

	tick := time.NewTicker(s.heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.hub.done:
			return
		case <-tick.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil || rc.Flush() != nil {
				return
			}
		case <-c.wake:
			msgs, dropped := c.take()
			if dropped {
				at := current
				if len(msgs) > 0 {
					at = msgs[0].seq - 1
				}
				if !s.writeResync(w, rc, at, "buffer_overrun") {
					return
				}
			}
			for _, m := range msgs {
				if !writeSSE(w, rc, strconv.FormatUint(m.seq, 10), m.typ, m.data) {
					return
				}
				current = m.seq
			}
		}
	}
}

// deref returns *s, or "" for nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// optionalMeta drops the empty entries of an SSE filter map: a record without
// that resource (for example an assist turn has no flow) is not tagged with it.
func optionalMeta(m map[string]string) map[string]string {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	return m
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (s *Server) writeResync(w http.ResponseWriter, rc *http.ResponseController, id uint64, reason string) bool {
	ids := strconv.FormatUint(id, 10)
	data, _ := json.Marshal(map[string]string{"reason": reason})
	body := mustJSON(envelope{Type: "stream.resync", ID: ids, TS: time.Now().UTC(), Data: data})
	return writeSSE(w, rc, ids, "stream.resync", body)
}

// writeSSE writes one message and flushes. An empty id omits the id line.
func writeSSE(w http.ResponseWriter, rc *http.ResponseController, id, typ string, data []byte) bool {
	var b bytes.Buffer
	if id != "" {
		b.WriteString("id: " + id + "\n")
	}
	b.WriteString("event: " + typ + "\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	if _, err := w.Write(b.Bytes()); err != nil {
		return false
	}
	return rc.Flush() == nil
}

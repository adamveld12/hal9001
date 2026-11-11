package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"hal9001/internal/notify"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// Approval nodes ("Request approval", and the approval gate of nodes with requires_approval: gate.go) raise a request approval and park the node as awaiting_approval, holding no
// concurrency slot. The decision (or its expiry) is applied by applyRequestDecision straight from DecideApproval, from
// finishNode when the decision beat the parking, and from reconcileRequests after a restart; it emits the node's input
// plus the decision on port approved or denied. Test runs (node tests and flow tests) never wait: they take the
// decision from the simulate_approval parameter.

const (
	errApprovalTimeout = "approval_timeout"

	// dryRunNotice is the first line of the body of a dry run's approval.
	dryRunNotice = notify.DryRunNotice

	// requestExpiryInterval is how often pending request approvals are checked for their timeout.
	requestExpiryInterval = 500 * time.Millisecond
)

// approvalInfo is the "approval" object of an approval node's output.
type approvalInfo struct {
	Decision  string  `json:"decision"` // approved | denied | timeout
	Comment   string  `json:"comment"`
	DecidedBy string  `json:"decided_by"`
	DecidedAt *string `json:"decided_at"`
	// ApprovalID is the id of the approval; set on the denied payload of a node's approval gate (requires_approval).
	ApprovalID string `json:"approval_id,omitempty"`
	// Edited is true when the approver changed editable input values; Edits then lists them by path.
	Edited bool                         `json:"edited"`
	Edits  map[string]store.RequestEdit `json:"edits,omitempty"`
}

// SimulatedApproval values for core.TestNodeRequest.SimulateApproval and core.FlowTestRequest.SimulateApproval.
const (
	SimulateApproved = "approved"
	SimulateDenied   = "denied"
)

func (e *Engine) runApproval(ne *nodeEnv) nodeOutcome {
	n := ne.node
	title, err := ne.render("title", n.Title)
	if err != nil {
		return failure(errTemplate, err.Error())
	}
	body, err := ne.render("body", n.Body)
	if err != nil {
		return failure(errTemplate, err.Error())
	}
	desc, err := ne.render("description", n.Description)
	if err != nil {
		return failure(errTemplate, err.Error())
	}
	red := ne.snap.red
	title = strings.TrimSpace(red.String(title))
	if title == "" {
		return failure(errTemplate, "title rendered empty")
	}
	desc = strings.TrimSpace(red.String(desc))
	if desc == "" {
		return failure(errTemplate, "description rendered empty")
	}
	if n := utf8.RuneCountInString(desc); n > store.MaxApprovalDescription {
		return failure(errTemplate, fmt.Sprintf("description rendered %d characters; the limit is %d", n, store.MaxApprovalDescription))
	}
	spc := ApprovalSpec{
		Type: store.ApprovalRequest, Title: title, Description: desc, BodyMD: body, Priority: store.Priority(n.Priority),
		DryRun: ne.fr.DryRun, NodeRunID: ne.nr.ID, FlowID: ne.fr.FlowID,
		Meta: store.RequestApproval{OnTimeout: n.OnTimeout},
	}
	for _, path := range n.Fields {
		spc.Fields = append(spc.Fields, store.RequestField{Key: path, Value: fieldText(lookupPath(ne.inputV, path))})
	}
	for _, path := range n.Editable {
		v, err := json.Marshal(lookupPath(ne.inputV, path))
		if err != nil {
			return failure(errConfig, err.Error())
		}
		spc.Editable = append(spc.Editable, store.RequestEditable{Key: path, Value: v})
	}
	if spc.DryRun {
		spc.BodyMD = strings.TrimRight(dryRunNotice+"\n\n"+spc.BodyMD, "\n")
	}

	if ne.fr.Kind == store.KindTest {
		decision := SimulateApproved
		if v, ok := e.simulations.Load(ne.fr.ID); ok {
			decision = v.(string)
		}
		if e.assistTestOf(ne.fr.ID) == nil {
			e.simulations.Delete(ne.fr.ID) // a node test runs one node; TestFlow deletes its own
		}
		now := time.Now().UTC()
		return e.requestOutcome(ne, decision, "", "simulated", &now, "simulated approval: "+decision+": "+truncate(title, 120), nil)
	}

	if n.Timeout > 0 {
		exp := time.Now().UTC().Add(n.Timeout.Std())
		spc.ExpiresAt = &exp
	}
	r, err := e.RaiseApproval(e.dbCtx, spc)
	if err != nil {
		return failure(errConfig, "create approval: "+err.Error())
	}
	return nodeOutcome{status: store.NodeAwaitingApproval, reason: "awaiting approval " + r.Approval.ID}
}

// requestOutcome is the outcome of a decided approval node: the input plus the approval, on port approved or denied.
func (e *Engine) requestOutcome(ne *nodeEnv, decision, comment, by string, at *time.Time, reason string, edits map[string]store.RequestEdit) nodeOutcome {
	info := approvalInfo{Decision: decision, Comment: comment, DecidedBy: by}
	if at != nil {
		s := at.UTC().Format(time.RFC3339)
		info.DecidedAt = &s
	}
	input := json.RawMessage(nonEmptyJSON(ne.input))
	if decision == "approved" && len(edits) > 0 {
		edited, err := applyEdits(input, edits)
		if err != nil {
			return failure(errConfig, "apply edits: "+err.Error())
		}
		input, info.Edited, info.Edits = edited, true, edits
		reason += " (edited " + strings.Join(sortedPaths(edits), ", ") + ")"
	}
	payload, err := json.Marshal(map[string]any{"input": input, "approval": info})
	if err != nil {
		return failure(errConfig, err.Error())
	}
	port := spec.PortDenied
	if decision == "approved" {
		port = spec.PortApproved
	}
	return nodeOutcome{
		status: store.NodeDone, decision: store.DecisionEmitted, reason: reason,
		outputs: []runner.Output{{Port: port, Payload: payload}},
	}
}

// applyRequestDecision carries a decided request approval into its parked node run. It does nothing while the node
// is not parked yet (finishNode calls it again once it is) or the approval is still pending.
func (e *Engine) applyRequestDecision(nodeRunID string) {
	unlock := e.locks.lock("node:" + nodeRunID)
	defer unlock()
	nr, err := e.st.GetNodeRun(e.dbCtx, nodeRunID)
	if err != nil || nr.Status != store.NodeAwaitingApproval {
		return
	}
	if nr.NodeType != store.NodeApproval { // a node waiting on its approval gate (requires_approval)
		e.applyGateDecision(nr)
		return
	}
	apps, err := e.st.ListApprovals(e.dbCtx, store.ApprovalFilter{NodeRunID: nr.ID, Type: store.ApprovalRequest, ListOpts: store.ListOpts{Limit: 1}})
	if err != nil || len(apps.Items) == 0 || apps.Items[0].Status == store.ApprovalPending {
		return
	}
	ap := apps.Items[0]
	fr, err := e.st.GetFlowRun(e.dbCtx, nr.FlowRunID)
	if err != nil {
		e.log.Error("engine: load flow run", "flow_run", nr.FlowRunID, "err", err)
		return
	}
	ne, err := e.newNodeEnv(fr, nr)
	if err != nil {
		e.failNodeRun(nr, err.Error())
		e.reconcileFlowRun(fr.ID)
		return
	}
	var edits map[string]store.RequestEdit
	var rq store.RequestApproval
	if json.Unmarshal([]byte(ap.Request), &rq) == nil {
		edits = rq.Edits
	}
	var out nodeOutcome
	switch ap.Status {
	case store.ApprovalApproved:
		out = e.requestOutcome(ne, "approved", ap.Comment, ap.DecidedBy, ap.DecidedAt, "approved"+commentSuffix(ap.Comment), edits)
	case store.ApprovalDenied:
		out = e.requestOutcome(ne, "denied", ap.Comment, ap.DecidedBy, ap.DecidedAt, "denied"+commentSuffix(ap.Comment), nil)
	default: // expired
		switch ne.node.OnTimeout {
		case spec.OnTimeoutApproved:
			out = e.requestOutcome(ne, "timeout", ap.Comment, ap.DecidedBy, ap.DecidedAt, "timed out, treated as approved", nil)
			out.outputs[0].Port = spec.PortApproved
		case spec.OnTimeoutError:
			out = failure(errApprovalTimeout, "approval timed out")
		default:
			out = e.requestOutcome(ne, "timeout", ap.Comment, ap.DecidedBy, ap.DecidedAt, "timed out, treated as denied", nil)
		}
	}
	e.finishNode(ne, out)
}

func commentSuffix(c string) string {
	if c == "" {
		return ""
	}
	return ": " + truncate(c, 120)
}

// expireRequests expires pending request approvals whose timeout has passed and applies the outcome.
func (e *Engine) expireRequests() {
	apps, err := e.st.ListApprovals(e.dbCtx, store.ApprovalFilter{
		Type: store.ApprovalRequest, Statuses: []store.ApprovalState{store.ApprovalPending}, Slim: true, ListOpts: store.ListOpts{Limit: 500},
	})
	if err != nil {
		e.log.Warn("engine: list request approvals", "err", err)
		return
	}
	now := time.Now().UTC()
	for _, ap := range apps.Items {
		if kind := classifyRequest(&ap); kind.waited() {
			if kind.detached() && ap.ExpiresAt != nil && !ap.ExpiresAt.After(now) {
				// request_approval: nobody has to be waiting. A gated MCP call's own wait expires its approval.
				e.expireWaitedApproval(ap.ID, "no decision before the approval's timeout")
			}
			continue
		}
		if ap.ExpiresAt == nil || ap.ExpiresAt.After(now) {
			continue
		}
		_, err := e.st.DecideApproval(e.dbCtx, ap.ID, store.ApprovalDecision{Status: store.ApprovalExpired, DecidedBy: "system", Comment: "timed out"})
		if err != nil {
			continue // decided concurrently
		}
		e.applyRequestDecision(ap.NodeRunID)
	}
}

// requestExpiryLoop checks request approvals for timeouts until the engine stops.
func (e *Engine) requestExpiryLoop(ctx context.Context) {
	t := time.NewTicker(requestExpiryInterval)
	defer t.Stop()
	for {
		e.expireRequests()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// reconcileRequests applies request decisions made while the engine was not running (or before the node parked),
// of approval nodes and of nodes waiting on their approval gate.
func (e *Engine) reconcileRequests(ctx context.Context) error {
	nodes, err := e.st.ListNodeRuns(ctx, store.NodeRunFilter{
		Statuses: []store.NodeRunStatus{store.NodeAwaitingApproval}, Slim: true, ListOpts: store.ListOpts{Limit: 500},
	})
	if err != nil {
		return err
	}
	for _, n := range nodes.Items {
		e.applyRequestDecision(n.ID)
	}
	return nil
}

// lookupPath follows a dot-path (a.b.0.c) through decoded JSON; a missing step gives nil.
func lookupPath(v any, path string) any {
	for _, seg := range strings.Split(path, ".") {
		switch t := v.(type) {
		case map[string]any:
			v = t[seg]
		case []any:
			var i int
			if _, err := fmt.Sscanf(seg, "%d", &i); err != nil || i < 0 || i >= len(t) {
				return nil
			}
			v = t[i]
		default:
			return nil
		}
	}
	return v
}

// fieldText is the table text of a field value: strings as they are, null as empty, the rest as compact JSON.
func fieldText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func sortedPaths[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// recordEdits validates the edits against the request's editable paths and returns the request JSON with the changed
// ones recorded as {before, after}; a value equal to the current one is not an edit. Unchanged JSON comes back as "".
func recordEdits(raw string, edits map[string]json.RawMessage) (string, error) {
	var req store.RequestApproval
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return "", err
	}
	for _, path := range sortedPaths(edits) {
		i := -1
		for j, ed := range req.Editable {
			if ed.Key == path {
				i = j
			}
		}
		if i < 0 {
			return "", fmt.Errorf("%w: %q is not an editable path of this approval", store.ErrInvalid, path)
		}
		var after bytes.Buffer
		if err := json.Compact(&after, edits[path]); err != nil {
			return "", fmt.Errorf("%w: edit of %q is not valid JSON", store.ErrInvalid, path)
		}
		var before bytes.Buffer
		_ = json.Compact(&before, req.Editable[i].Value)
		if bytes.Equal(before.Bytes(), after.Bytes()) {
			continue
		}
		if req.Edits == nil {
			req.Edits = map[string]store.RequestEdit{}
		}
		req.Edits[path] = store.RequestEdit{Before: before.Bytes(), After: after.Bytes()}
	}
	if len(req.Edits) == 0 {
		return "", nil
	}
	out, err := json.Marshal(req)
	return string(out), err
}

// applyEdits sets each edit's after value at its dot-path in the input JSON.
func applyEdits(input json.RawMessage, edits map[string]store.RequestEdit) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return nil, err
	}
	for _, path := range sortedPaths(edits) {
		var after any
		if err := json.Unmarshal(edits[path].After, &after); err != nil {
			return nil, err
		}
		var err error
		if v, err = setPath(v, strings.Split(path, "."), after); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return json.Marshal(v)
}

// setPath returns v with the value at segs replaced, creating missing objects on the way.
func setPath(v any, segs []string, val any) (any, error) {
	if len(segs) == 0 {
		return val, nil
	}
	switch t := v.(type) {
	case nil:
		child, err := setPath(nil, segs[1:], val)
		return map[string]any{segs[0]: child}, err
	case map[string]any:
		child, err := setPath(t[segs[0]], segs[1:], val)
		t[segs[0]] = child
		return t, err
	case []any:
		var i int
		if _, err := fmt.Sscanf(segs[0], "%d", &i); err != nil || i < 0 || i >= len(t) {
			return v, fmt.Errorf("no list element %q", segs[0])
		}
		child, err := setPath(t[i], segs[1:], val)
		t[i] = child
		return t, err
	}
	return v, fmt.Errorf("cannot go into a %T value", v)
}

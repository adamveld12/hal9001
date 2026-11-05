package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/coder/acp-go-sdk"
)

// drainLimit bounds how long finish waits for notifications the agent sent
// before it exited to be processed.
const drainLimit = 5 * time.Second

// updateGate accounts for the session/update notifications of one connection.
//
// When the agent exits, the SDK closes the connection (Prompt returns an
// error) as soon as the pipe hits EOF, but the notifications it had already
// read are handed to the client afterwards, on another goroutine. A run that
// is finalized at that moment would lose the agent's last output. The gate
// counts the updates read from the pipe and the updates the client finished
// handling, so a run can wait until the two agree before it records its final
// message and status.
type updateGate struct {
	received atomic.Int64
	handled  atomic.Int64
	kick     chan struct{}
}

func newUpdateGate() *updateGate { return &updateGate{kick: make(chan struct{}, 1)} }

// observe is called for every line read from the agent.
func (g *updateGate) observe(line []byte) {
	if !bytes.Contains(line, []byte(`"session/update"`)) {
		return
	}
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(line, &m) == nil && m.Method == acp.ClientMethodSessionUpdate && len(m.ID) == 0 {
		g.received.Add(1)
	}
}

func (g *updateGate) done() {
	g.handled.Add(1)
	select {
	case g.kick <- struct{}{}:
	default:
	}
}

// drain waits until every update read so far was handled, at most limit.
func (g *updateGate) drain(limit time.Duration) {
	if g.handled.Load() >= g.received.Load() {
		return
	}
	t := time.NewTimer(limit)
	defer t.Stop()
	for g.handled.Load() < g.received.Load() {
		select {
		case <-g.kick:
		case <-t.C:
			return
		}
	}
}

// gatedClient reports every handled session/update to the gate. It forwards
// the optional (unstable) client methods the SDK looks for by type assertion.
type gatedClient struct {
	acp.Client
	gate *updateGate
}

func (c gatedClient) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	defer c.gate.done()
	return c.Client.SessionUpdate(ctx, n)
}

func (c gatedClient) UnstableCreateElicitation(ctx context.Context, req acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	if x, ok := c.Client.(interface {
		UnstableCreateElicitation(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error)
	}); ok {
		return x.UnstableCreateElicitation(ctx, req)
	}
	return acp.UnstableCreateElicitationResponse{}, acp.NewMethodNotFound(acp.ClientMethodElicitationCreate)
}

func (c gatedClient) UnstableCompleteElicitation(ctx context.Context, n acp.UnstableCompleteElicitationNotification) error {
	if x, ok := c.Client.(interface {
		UnstableCompleteElicitation(context.Context, acp.UnstableCompleteElicitationNotification) error
	}); ok {
		return x.UnstableCompleteElicitation(ctx, n)
	}
	return nil
}

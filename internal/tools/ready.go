package tools

import (
	"context"
	"time"
)

// ReadyTimeout bounds how long the first prompt waits for the agent's MCP client to connect.
const ReadyTimeout = 30 * time.Second

// ToolsReady returns the agent.RunRequest.ToolsReady hook of a session: it waits for the agent's MCP client.
func ToolsReady(s *Session) func(ctx context.Context) {
	return func(ctx context.Context) { s.WaitReady(ctx, ReadyTimeout) }
}

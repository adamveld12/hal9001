package cli

import (
	"context"
	"flag"
	"net/http"
)

// conversationCmds are the `hal9001 conversations` commands: assistant chats and agent runs of flow nodes as one list
// (GET /api/conversations), their transcripts, and forking a run into a chat.
func conversationCmds() []*command {
	var lf listFlags
	var source, kind, flow, node, flowRun, runtime, status, text, since, until, forkedFrom string
	list := &leaf{
		usage: "[--source flow|chat] [--flow ID] [--node ID] [--runtime NAME] [--status S] [--q TEXT] [--since T] [--until T] [--forked-from RUN]", maxArgs: 0,
		flags: func(fs *flag.FlagSet) {
			lf.register(fs)
			fs.StringVar(&source, "source", "", "only flow (agent runs of flow nodes) or chat (assistant chats) conversations")
			fs.StringVar(&kind, "kind", "", "a chat's target kind (general, sensor, flow, tool), or agent for flow conversations")
			fs.StringVar(&flow, "flow", "", "only conversations of this flow id (runs, their forks, chats that draft it)")
			fs.StringVar(&node, "node", "", "only conversations of this node id")
			fs.StringVar(&flowRun, "flow-run", "", "only the agent runs of this flow run and their forks")
			fs.StringVar(&runtime, "runtime", "", "only this runtime")
			fs.StringVar(&status, "status", "", "running, awaiting_approval, idle, succeeded, failed, killed, denied, interrupted, timeout or closed")
			fs.StringVar(&text, "q", "", "text to search for in titles, ids, messages, prompts and results")
			fs.StringVar(&since, "since", "", "only conversations with activity at or after this RFC 3339 time")
			fs.StringVar(&until, "until", "", "only conversations with activity before this RFC 3339 time")
			fs.StringVar(&forkedFrom, "forked-from", "", "only the chats forked from this agent run id")
		},
		run: func(ctx context.Context, c *call) error {
			qv := lf.apply(query("source", source, "kind", kind, "flow", flow, "node", node, "flow_run", flowRun, "runtime", runtime,
				"status", status, "q", text, "since", since, "until", until, "forked_from", forkedFrom))
			return c.doEmit(ctx, http.MethodGet, "/api/conversations", qv, nil, "id", "source", "title", "status", "runtime", "flow_id", "node_id", "updated_at")
		},
	}
	get := idGet("/api/conversations/%s")
	messages := &leaf{usage: "<id>", minArgs: 1, maxArgs: 1, run: func(ctx context.Context, c *call) error {
		return c.doEmit(ctx, http.MethodGet, "/api/conversations/"+esc(c.args[0])+"/messages", nil, nil, "id", "role", "kind", "text", "created_at")
	}}
	fork := &leaf{usage: "<agent-run-id>", minArgs: 1, maxArgs: 1, run: func(ctx context.Context, c *call) error {
		return c.doEmit(ctx, http.MethodPost, "/api/conversations/"+esc(c.args[0])+"/fork", nil, nil)
	}}
	return []*command{
		cmd("list", "list assistant chats and agent runs of flow nodes, newest activity first", list),
		cmd("get", "show a conversation with the chats forked from it (an assist session id or an agent run id)", get),
		cmd("messages", "show a conversation's transcript", messages),
		cmd("fork", "continue an agent run in a new chat (send the first message with `assist send`)", fork),
	}
}

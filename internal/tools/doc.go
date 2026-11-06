// Package tools is hal's own tool set for agents, served through the hal MCP server (docs/design/agent-access.md).
//
// # Pieces
//
//   - Tool and Registry: a tool is a name, a JSON Schema for its arguments, an optional Approval function and a Run
//     function. A Registry is a named set of tools.
//   - Hub: per-process owner of run sessions. Hub.Open creates a Session for one agent run (or one assist session),
//     builds its tool set, and issues a random capability token (only its SHA-256 hash is stored). Session.Close
//     revokes the token and releases anything the tool set holds.
//   - Session.Call: the single execution path. It looks the tool up, asks the tool whether the arguments need
//     approval, blocks on Host.Approve when they do, runs the tool, and records every call through Host.
//   - Host: what the daemon supplies (the approval queue and the run trace). internal/engine implements it with
//     tool_call approvals and permission_requests.
//
// The daemon exposes Hub.Lookup over HTTP under /api/mcp/ (token auth); `hal9001 mcp --token T` is the stdio MCP
// server the agent spawns, a thin proxy of tools/list and tools/call to those endpoints.
//
// # Built-in tools
//
// Every session gets: gh_api, pr_view, pr_diff, pr_list, issue_view, search (the daemon's `gh`; GET and the read
// helpers run without approval, other gh_api methods need it), now, run_context, node_input and event.
//
// # Adding tools for a run
//
// Other packages contribute tools by registering a Provider on the Hub:
//
//	hub.AddProvider(func(ctx context.Context, spec tools.RunSpec) (tools.Extension, error) {
//		if spec.Sandbox == "" {
//			return tools.Extension{}, nil // nothing to add for this run
//		}
//		sb, err := startSandbox(ctx, spec)
//		if err != nil {
//			return tools.Extension{}, err
//		}
//		return tools.Extension{
//			Tools: []tools.Tool{fsRead(sb), shell(sb)},
//			Close: sb.Stop, // called once when the session closes
//		}, nil
//	})
//
// A provider sees the RunSpec (node fields Sandbox, Image, Repo, Ref and Tools, the run kind, and Info). It is
// called on every Hub.Open, in registration order; a returned error fails the open (the run does not start). A
// tool name that is already taken (by a built-in or an earlier provider) fails the open too. A tool's Approval
// function decides per call whether the arguments need a user's approval; Run's error becomes a tool error the agent
// sees. Tool names are lowercase snake_case.
//
// # Sandbox tools
//
// SandboxProvider(manager, opts) adds fs_read, fs_write, fs_list, fs_search and shell to runs whose node sets
// `sandbox:`. It opens a session through the sandbox Manager (node image, repo, ref; the node input becomes
// input.json) and closes it with Session.CloseWith(success): a failed run keeps its per_run workspace. fs_write and
// shell approvals follow the sandbox policy (auto / approve / deny); an approval's reason carries the policy rule and,
// for fs_write, a unified diff. A deny rule fails the call in Run.
//
// RunInSandbox(ctx, session, lang, script, stdin, env, timeout) runs a tool script (bash, python, node, or exec for a
// command line) inside a sandbox session; env values are exported for that command and redacted from the result.
//
// Tests of a tool set need no daemon: build a Hub with a fake Host, Open a session and call Session.Call.
package tools

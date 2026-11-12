package assist

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"hal9001/internal/tools"
	halupdate "hal9001/internal/update"
)

// helperTools are the tools every assist session gets besides the draft tools: the read-only inspection tools
// (tools.ReadTools: everything the CLI can list, get and inspect) and github_issue_link. None of them needs approval
// and none changes anything; edits still go through draft_write, the user's Save and the approval gate.
//
// Results pass through the session's redactor, so a secret value that an inspected record happens to contain (a
// command's output, a rendered prompt) never reaches the agent.
func (s *session) helperTools() []tools.Tool {
	read := tools.ReadTools()
	out := make([]tools.Tool, 0, len(read)+1)
	for _, t := range read {
		run := t.Run
		t.Run = func(ctx context.Context, c tools.Call) (tools.Result, error) {
			res, err := run(ctx, c)
			res.Text = s.redact(res.Text)
			return res, err
		}
		out = append(out, t)
	}
	return append(out, tools.IssueLinkTool(tools.IssueLinkOptions{Env: s.m.issueEnv, Redact: s.redact}))
}

// issueEnv is the installation's version information for the diagnostics block of a bug report. It reads build
// metadata and platform names only: never settings, paths or environment values.
func (m *Manager) issueEnv() tools.IssueEnv {
	env := tools.IssueEnv{
		Version: m.opt.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(), MacOS: halupdate.MacOSVersion(),
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range bi.Settings {
			switch kv.Key {
			case "vcs.revision":
				env.Commit = kv.Value
				if len(env.Commit) > 12 {
					env.Commit = env.Commit[:12]
				}
			case "vcs.time":
				env.BuildTime = kv.Value
			}
		}
	}
	return env
}

// inspectGuide is the instructions section about the inspection tools and bug reports, shared by every session kind.
// drafts is false for a general session, which has no draft tools.
func inspectGuide(drafts bool) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("## Looking at what hal9001 did (debugging)\n\n")
	if drafts {
		w("Besides the draft tools you can read everything the `hal9001` CLI can show. These tools only read: they never ask the user and never change anything. ")
	} else {
		w("You can read everything the `hal9001` CLI can show. These tools only read: they never ask the user and never change anything. ")
	}
	w("Use them whenever the user asks why something happened, failed or did not fire, and before you guess.\n\n")
	w("- Flows and runs: `flows_list`, `flow_get`, `flow_runs_list` (`flow_id`, `status`, `event_id`, `kind`, `since`, `until`, `limit`), `flow_run_get` (the run with its node runs), `node_run_get` (decision, error, stdin, stdout, stderr, agent runs), `queue_list`.\n")
	w("- Agent runs: `agent_runs_list` (`flow_run_id`, `node_run_id`, `status`, `runtime`), `agent_run_get` (error, rendered prompt, result, approvals), `agent_run_events` (the transcript, with `after_seq`, `limit`, `kind`), `agent_run_permission_requests` (each tool call and how it was decided), `agent_run_sandbox_log` and `node_run_sandbox_log` (sandbox commands and file operations; `tail: true` for the newest).\n")
	w("- Conversations: `conversations_list` (assistant chats and agent runs of flow nodes in one list; `source`, `flow`, `node`, `status`, `q`, `forked_from`), `conversation_get` (with the chats forked from a run) and `conversation_messages` (one transcript shape for both: messages, tool calls with arguments and results, approvals).\n")
	w("- Sensors and events: `sensors_list`, `sensor_get`, `sensor_runs_list` (`id`, `status`), `sensor_run_get` (stdout, stderr, items), `events_list` (`sensor_id`, `flow_id`, `status`, `decision`, `q`), `event_get` (payload and fan-out).\n")
	w("- Inbox and approvals: `inbox_list`, `inbox_get`, `approvals_list`, `approval_get`. Only the user decides approvals.\n")
	w("- Configuration: `tools_list`, `tool_get`, `prompts_list`, `prompt_get`, `runtimes_list`, `sandboxes_list`, `sandbox_get`, `settings_get`, `status`. No secret values are in any of them.\n\n")
	w("How to debug:\n\n")
	w("1. \"Why did my flow fail?\": `flow_runs_list` with `flow_id` and `status: failed` (`limit` 5); `flow_run_get` on the run: find the node runs whose decision is `error` and read their error; `node_run_get` on that node run for its input, stdout and stderr. ")
	w("If the node is an agent node, `agent_runs_list` with `flow_run_id`, then `agent_run_get` (the error and the rendered prompt) and `agent_run_events` with `kind: tool_call,tool_call_update` (what the agent called and got back); `agent_run_permission_requests` shows denied calls. ")
	w("If it used a sandbox, `agent_run_sandbox_log` with `tail: true`.\n")
	w("2. \"Why did the sensor not fire?\": `sensor_get` (state, paused, last status), `sensor_runs_list` (`status: failed` for errors), `sensor_run_get` (stderr, item count), `events_list` with `sensor_id`: items whose dedup key was already seen produce no event. Then `flow_runs_list` with `event_id` for what the flows did with it.\n")
	w("3. \"What happened to this event or item?\": `event_get` shows the flow runs it fanned out to; a transform that dropped it gives a reason on its node run.\n")
	if drafts {
		w("4. Report what you found in plain language: the cause, the ids of the runs you looked at (so the user can open them), and the fix. If the fix is a change to a draft you can make, make it with `draft_write`.\n\n")
	} else {
		w("4. Report what you found in plain language: the cause, the ids of the runs you looked at (so the user can open them), and the fix. If the fix is a change to a sensor, flow or tool, offer a draft session with `start_draft`.\n\n")
	}
	w("Limits: lists take `limit` (at most 200) and `cursor` from the previous `next_cursor`; the transcript and the sandbox log page with `after_seq` and `after_id`; a result is at most 64 KB and a single long string (a prompt, a script, command output) is cut in the middle. Narrow the query instead of asking for everything. ")
	if drafts {
		w("These tools cannot run a flow or poll a sensor, and changes to saved files still go through the draft and the user's Save; tell the user to use Run now, or to do it on the page, when running something would help.\n\n")
	} else {
		w("These tools cannot run a flow or poll a sensor, and changes to saved files go through a draft session and the user's Save; tell the user to use Run now, or to do it on the page (open it with navigate_ui), when running something would help.\n\n")
	}
	w("## Reporting a bug or filing an issue\n\n")
	w("When the user reports a bug in hal9001, or asks you to file or open an issue, draft a good one and offer the link with `github_issue_link`. It builds a GitHub new-issue link with the form filled in; nothing is created until the user opens the link and submits it, so it needs no approval and you do not ask first. ")
	w("Investigate with the tools above first, so the issue holds facts: a short title; a body with what the user tried (steps to reproduce), what they expected, what happened instead (error text, decisions), and the ids of the runs involved (flow run, node run, agent run, sensor, event). ")
	w("Set `diagnostics: true` and `refs` to add the hal9001 version and build, the macOS version and the ids; it never adds settings or secrets. Never put secret values, tokens or environment values in an issue, and cut any you see in output you quote. ")
	w("Default repository is `%s` (bugs and requests about hal9001 itself); pass `repo: owner/name` only when the user names another one. Use `labels: [\"bug\"]` for bugs. ", tools.DefaultIssueRepo)
	w("After the call say in a sentence that the issue is ready to open from the button in the chat, and mention anything the user should add. If the result says `truncated`, tell them the link holds only the start and to paste the full text with Copy body.\n\n")
	return b.String() + mcpGuide(drafts)
}

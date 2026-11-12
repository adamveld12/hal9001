package cli_test

// Contract-valid response bodies (checked against api/openapi.yaml by the fake daemon).

func blob(text string) map[string]any {
	return map[string]any{"text": text, "size": len(text), "truncated": false, "blob": nil}
}

func killSwitch(paused bool) map[string]any {
	return map[string]any{"paused": paused, "kill_running": false, "since": nil, "by": nil, "reason": nil}
}

func statusBody() map[string]any {
	return map[string]any{
		"kill_switch": killSwitch(false), "running_flow_runs": 1, "queued_flow_runs": 2, "queue_total": 2,
		"running_agent_runs": 0, "queued_agent_runs": 0, "pending_approvals": 3, "unread_inbox": 4,
	}
}

func page(items ...any) map[string]any {
	if items == nil {
		items = []any{}
	}
	return map[string]any{"items": items, "next_cursor": nil}
}

func flowRun(id, status string) map[string]any {
	return map[string]any{
		"id": id, "flow_id": "triage-github", "flow_hash": hash, "event_id": id2, "kind": "live", "replay_of": nil,
		"dry_run": false, "status": status, "error": nil, "cancelled_by": nil, "created_at": ts, "started_at": ts, "ended_at": nil, "continuation": nil,
	}
}

func nodeRun(status string) map[string]any {
	return map[string]any{
		"id": id3, "flow_run_id": id1, "node_id": "classify", "node_type": "command", "parent_node_run_id": nil,
		"status": status, "decision": nil, "reason": nil, "error": nil, "ports": []string{}, "emitted_count": 0,
		"node": map[string]any{"id": "classify", "type": "command", "lang": "bash", "script": "cat"},
		"argv": nil, "env_keys": []string{"GITHUB_TOKEN"}, "stdin": blob("{}"), "stdout": blob(""), "stderr": blob(""),
		"exit_code": nil, "duration_ms": nil, "created_at": ts, "started_at": ts, "ended_at": nil,
	}
}

func flowRunDetail(status string) map[string]any {
	return map[string]any{"flow_run": flowRun(id1, status), "node_runs": []any{nodeRun("running")}}
}

func event() map[string]any {
	return map[string]any{
		"id": id2, "sensor_id": "github-notifications", "sensor_run_id": nil, "dedup_key": "42:2026-11-05",
		"payload": map[string]any{"id": "42"}, "created_at": ts, "injected": true,
	}
}

func eventSummary() map[string]any {
	return map[string]any{
		"id": id2, "sensor_id": "github-notifications", "sensor_run_id": nil, "dedup_key": "42:2026-11-05",
		"payload_preview": `{"id":"42"}`, "created_at": ts, "injected": false, "flow_runs": []any{},
	}
}

func flowRunBrief() map[string]any {
	return map[string]any{
		"id": id1, "flow_id": "triage-github", "flow_hash": hash, "kind": "live", "dry_run": false,
		"status": "queued", "replay_of": nil, "created_at": ts, "ended_at": nil,
	}
}

func sensorSummary() map[string]any {
	return map[string]any{
		"id": "github-notifications", "path": "sensors/github-notifications.yaml", "hash": hash,
		"sensor":      map[string]any{"id": "github-notifications", "lang": "bash", "interval": "2m", "script": "gh api notifications"},
		"diagnostics": []any{},
		"status": map[string]any{
			"state": "idle", "paused": false, "paused_at": nil, "paused_by": nil, "last_run": nil, "last_success_at": ts, "next_poll_at": nil, "manual": false, "reset_at": nil, "reset_by": nil, "new_items_24h": 7,
		},
	}
}

func probeResult(okay bool) map[string]any {
	return map[string]any{"runtime": "claude", "ok": okay, "checked_at": ts, "duration_ms": 120, "error": nil}
}

func inboxItem(read bool) map[string]any {
	return map[string]any{
		"id": id1, "source": "agent_run", "source_id": id2, "flow_run_id": id1, "node_run_id": id3, "agent_run_id": id2,
		"flow_id": "triage-slack", "node_id": "note", "event_id": nil, "title": "Large PR needs you", "priority": "high",
		"summary_md": "babylist/web#4120", "suggested_reply": nil, "links": []any{}, "read": read, "starred": false,
		"archived": false, "dry_run": false, "parse_error": false, "agent_chats": 0, "created_at": ts,
	}
}

func approval(status string) map[string]any {
	return map[string]any{
		"id": id1, "agent_run_id": id2, "node_run_id": id3, "flow_run_id": id1, "flow_id": "triage-github",
		"node_id": "review", "assist_session_id": nil, "event_id": nil, "runtime": "claude", "type": "proposal", "permission_request_id": nil,
		"permission_request": nil, "proposal_md": "Fix the flaky test", "status": status, "comment": nil, "cancel_run": nil,
		"decided_by": nil, "decided_at": nil, "created_at": ts, "expires_at": nil,
		"title": "Fix the flaky test", "description": "Fix the flaky test", "description_by": "hal", "priority": "normal",
		"origin":   map[string]any{"kind": "flow", "label": "triage-github / review", "client": nil},
		"details":  map[string]any{"body_md": "", "fields": []any{}, "command": nil, "tool": nil, "diffs": []any{}, "dry_run": false},
		"editable": []any{}, "edits": nil, "held": false, "held_note": nil, "gate": false, "on_timeout": nil,
		"conversation": map[string]any{"id": id2, "kind": "flow", "anchor": nil, "path": "/chat/" + id2},
	}
}

func agentRun(status string) map[string]any {
	return map[string]any{
		"id": id2, "node_run_id": id3, "flow_run_id": id1, "flow_id": "triage-github", "node_id": "review", "assist_session_id": nil,
		"runtime": "claude", "agent_info": nil, "capabilities": map[string]any{}, "mode": "readonly", "approve_style": nil,
		"policy": "readonly", "phase": "execute", "session_id": nil, "resumed_via": nil, "resume_fallback": nil,
		"cwd": nil, "rendered_prompt": "Review it", "argv": []string{"claude-agent-acp"}, "status": status,
		"stop_reason": nil, "final_message": nil, "result": nil, "output": nil, "emissions": []any{}, "parse_error": false, "error": nil, "raw_blob": nil,
		"raw_size": 0, "created_at": ts, "started_at": ts, "ended_at": nil,
	}
}

func agentEvent(seq int) map[string]any {
	return map[string]any{"agent_run_id": id2, "seq": seq, "ts": ts, "kind": "message", "tool_call_id": nil, "payload": map[string]any{"text": "hi"}}
}

func diagnostic(sev, code, msg string) map[string]any {
	return map[string]any{"severity": sev, "code": code, "message": msg, "file": "flows/x.yaml", "line": 3, "path": "nodes[0]"}
}

func sensorRun() map[string]any {
	return map[string]any{
		"id": id1, "sensor_id": "github-notifications", "trigger": "manual", "status": "running", "baseline": false,
		"started_at": ts, "ended_at": nil, "duration_ms": nil, "exit_code": nil, "item_count": nil, "new_count": nil,
		"stdout": blob(""), "stderr": blob(""), "error": nil,
	}
}

func pollResult() map[string]any {
	r := sensorRun()
	r["new_events"], r["rerun_events"], r["flow_run_ids"] = 1, 0, []any{id1}
	return r
}

func runFlowResult() map[string]any {
	return map[string]any{
		"flow_id": "triage-github", "flow_run_ids": []any{id1},
		"sensors": []any{map[string]any{
			"sensor_id": "github-notifications", "run": sensorRun(), "new_events": 1, "rerun_events": 0, "flow_run_ids": []any{id1}, "error": nil,
		}},
	}
}

func resetResult(run bool) map[string]any {
	out := map[string]any{"sensor_id": "gh", "reset_at": ts, "reset_by": "localhost", "keys_cleared": 3, "run": nil}
	if run {
		out["run"] = pollResult()
	}
	return out
}

const sensorYAML = "id: gh\nlang: bash\nscript: echo '[]'\n"

func assistDraft() map[string]any {
	return map[string]any{
		"kind": "sensor", "yaml": sensorYAML, "hash": hash, "sensor_id": "gh", "flow_id": nil, "tool_id": nil, "prompts": []any{}, "sensors": []any{},
		"diagnostics": []any{diagnostic("warning", "no-key", "key template missing")},
		"updated_at":  ts, "updated_by": "agent",
	}
}

func assistSession(status string) map[string]any {
	return map[string]any{
		"id": id1, "title": nil, "target": map[string]any{"kind": "sensor", "id": "gh"}, "runtime": "claude", "model": nil,
		"status": status, "error": nil, "draft": assistDraft(), "last_agent_run_id": nil,
		"created_at": ts, "updated_at": ts,
	}
}

func assistSessionDetail() map[string]any {
	d := assistSession("idle")
	d["messages"] = []any{map[string]any{
		"id": id2, "role": "user", "text": "make it poll hourly", "agent_run_id": nil, "tool_call": nil, "approval_id": nil, "created_at": ts,
	}}
	return d
}

func assistTestResult(exitCode int) map[string]any {
	return map[string]any{
		"kind": "sensor", "sensor_id": nil, "exit_code": exitCode, "duration_ms": 42, "timed_out": false,
		"items":      []any{map[string]any{"dedup_key": "1", "key_error": nil, "item": map[string]any{"id": 1}}},
		"item_count": 1, "parse_error": nil, "stdout": blob(`[{"id":1}]`), "stderr": blob("boom"),
	}
}

// runtimeItem is an item of GET /api/runtimes. warnings are RuntimeWarning objects; installing marks a running adapter install.
func runtimeItem(name, status string, warnings []any, opts ...func(map[string]any)) map[string]any {
	if warnings == nil {
		warnings = []any{}
	}
	det := map[string]any{
		"status": status,
		"cli":    map[string]any{"name": name, "found": true, "path": "/usr/local/bin/" + name, "version": "1.2.3"},
		"auth":   map[string]any{"status": "ok", "command": name + " login"},
		"adapter": map[string]any{"source": "managed", "installed": true, "version": "0.100.0", "pinned_version": "0.100.0",
			"path": "/home/data/adapters/x/dist/index.js", "installable": true, "installing": false},
		"node":       map[string]any{"found": true, "version": "24.1.0", "ok": true},
		"command":    []string{"/usr/bin/node", "/home/data/adapters/x/dist/index.js"},
		"warnings":   warnings,
		"checked_at": ts,
	}
	item := map[string]any{"name": name, "kind": name, "command": []string{}, "modes": map[string]any{}, "env_keys": []string{}, "last_probe": nil, "detection": det}
	for _, o := range opts {
		o(item)
	}
	return item
}

func withAdapter(patch map[string]any) func(map[string]any) {
	return func(item map[string]any) {
		ad := item["detection"].(map[string]any)["adapter"].(map[string]any)
		for k, v := range patch {
			ad[k] = v
		}
	}
}

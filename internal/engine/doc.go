// Package engine executes hal9001's work (REQUIREMENTS §4-§8): sensor
// scheduling and dedup, fan-out of events into flow runs, the DAG executor for
// command, inbox and agent nodes, concurrency queues, approvals, the kill
// switch, replay, node tests and retention. *Engine implements core.Engine.
//
// # Lifecycle
//
// New, Apply (the loaded spec.Workspace; again on every hot reload), Start,
// Stop. Apply is safe before Start and while work runs: running nodes keep
// going, queued and later work sees the new files. Start runs
// store.MarkInterrupted (running work becomes interrupted and is not retried,
// per-tool approvals expire, queued runs and proposal approvals stay), restores
// the kill switch and starts the dispatcher, the sensor schedulers and the daily
// retention job. Stop cancels running nodes with agent.ErrInterrupted and waits.
//
// # Execution model
//
// Everything is persisted before it runs. A queued flow run is claimed
// (store.ClaimNextFlowRun, capped per flow by max_concurrent) and gets one
// queued node run per entry node, with the event as input. Queued node runs are
// claimed under the pool caps (store.ClaimNextNodeRun: max_command_runs commands,
// max_agent_runs agent nodes, inbox unlimited). A node's outputs are written to
// its node run before the node runs of the edges leaving it are queued, so a
// restart never loses an emitted payload. Flow runs follow the flow version
// stored with them (flow_versions), not the current file.
//
// A flow run is running while any node is queued or running, awaiting_approval
// while only approvals are outstanding, and then terminal: killed, else
// interrupted, else failed (some node failed without an error route), else
// succeeded.
//
// # Node results
//
// Command nodes follow the §4 contract through internal/runner. NodeRun.Error
// holds the error class (timeout, exit_nonzero, undeclared_port, invalid_output,
// killed, interrupted, approval_timeout, denied, ...) and NodeRun.Reason the
// summary or the output's _reason. A failed node whose flow wires its error
// port routes {"input": <node input>, "error": class, "reason": summary,
// "exit_code": n, "stderr": tail} there and is recorded done with decision error;
// otherwise the node and its branch fail. Agent nodes emit
// {"input": <node input>, "agent": {run_id, status, final_message, note,
// proposal, comment}} on out, or on denied after a denied proposal or a
// denied-and-cancelled tool call. Inbox nodes pass their input through on out.
//
// # Agents
//
// The agent Executor (internal/agent) is driven with a store-backed Recorder
// and ApprovalBroker. Dry-run forces readonly. A proposal approval parks the node
// (awaiting_approval, no concurrency slot); approving requeues it and the next
// claim resumes the same session, denying routes to port denied. A per-tool
// approval keeps the agent process, and its slot, alive while it waits.
// agent_runs.result is {"final_message", "note", "parse_error", "resume_fallback"}; raw JSON-RPC is
// stored as JSON lines {"t","dir":"->"|"<-","msg"}. Isolated CODEX_HOME and
// PI_CODING_AGENT_DIR directories named in a runtime's env are created with a
// symlink to the existing login (docs/acp-gate.md); "{{data}}" in runtime
// config stands for <home>/data.
//
// # Kill switch
//
// Pause stops sensor polling and the start of any node (queued work waits).
// With killRunning, running nodes and polls are cancelled: agents with
// agent.ErrKilled, commands by killing their process group. PollSensor and
// TestNode are rejected with core.ErrPaused; InjectEvent, Replay and
// DecideApproval are accepted and queue. The state is persisted.
package engine

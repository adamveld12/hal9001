// Package store is hal9001's persistence layer: SQLite (modernc.org/sqlite,
// pure Go) for traces, events, queues, inbox, approvals and auth state, plus a
// content-addressed blob store (package blob) for large payloads.
//
// # Tables (REQUIREMENTS §9, with what §5, §8 and §10 imply)
//
//	sensor_runs           one poll: times, exit, item/new counts, stdout/stderr
//	sensor_state          per sensor: last_success_at (HAL_LAST_SUCCESS_AT), failure streak, pause (paused_at/by)
//	seen_keys             (sensor_id, dedup_key, first_seen); outlives events
//	events                deduplicated sensor items, immutable
//	flow_versions         (flow_id, hash) -> YAML snapshot
//	flow_runs             flow, flow_hash, event, kind live|replay|test, replay_of, dry_run, status; also the flow-run queue
//	node_runs             per node: parent, type, status, decision, reason, ports, script/argv/env keys snapshot,
//	                      stdin/stdout/stderr, exit, timing; also the node-run queue
//	agent_runs            per agent phase: runtime, info, capabilities, mode, style, policy, phase, session, resume, cwd,
//	                      worktree, prompt, argv, status, result, raw JSON-RPC
//	agent_events          transcript, (agent_run_id, seq)
//	sandbox_sessions      per sandbox an agent run opened: backend, ids, image, repo, status, timing; follows its node run
//	sandbox_log_entries   that sandbox's log: lifecycle, commands with output, file operations, denied calls
//	permission_requests   every session/request_permission and its decision
//	approvals             proposal, tool_call and request approvals (one shape: title, description, details, origin, conversation)
//	inbox_items           notes; links to runs by plain ids so they can outlive pruning
//	auth_events           every authentication decision
//	sessions              id is the caller-computed hash of the cookie secret
//	webauthn_credentials  passkeys per relying-party id
//	settings              key/value JSON (kill switch lives at "kill_switch")
//	schema_migrations     applied migration versions
//
// # Invariants
//
//   - Schema is versioned by embedded, ordered migrations (migrations/NNNN_*.sql)
//     applied in transactions; Open is idempotent and refuses a database
//     newer than the binary.
//   - Writes are serialised through one connection (BEGIN IMMEDIATE); reads use a
//     separate WAL reader pool. Methods are safe for concurrent goroutines and
//     never see SQLITE_BUSY in-process. WAL, busy_timeout and foreign_keys are
//     set on every connection.
//   - IDs are ULIDs, strictly increasing per Store. List methods paginate by
//     ID cursor (Page.NextCursor), newest first, and are stable under concurrent
//     inserts.
//   - Timestamps are stored as fixed-width UTC text so string order is time order.
//   - Enum columns have CHECK constraints with the exact strings of the shared
//     enums; Go methods reject values outside them with ErrInvalid.
//   - Dedup is atomic: ClaimEvent / IngestEvent insert into seen_keys, so of any
//     number of concurrent callers exactly one sees a key as new.
//   - A flow run references a stored flow version (foreign key); pass
//     FlowRun.FlowYAML to store the version in the same transaction.
//   - A node run is finished together with the node runs it feeds (FinishNodeRun),
//     and node runs that start together are queued together (CreateNodeRuns): a
//     reader never sees a finished node run without its follow-ups, so a flow
//     run whose node runs are all finished has nothing left to queue.
//   - Payloads above SpillThreshold (64 KiB) are written to the blob store and the
//     row keeps only the ref (see Content). Blobs are content-addressed, so
//     several rows may share one; a blob is deleted only when no row references it.
//   - Approvals are decided by compare-and-set (DecideApproval): exactly one
//     decision wins, others get ErrConflict.
//   - Queues are persistent: queued flow runs and node runs survive restarts and
//     are claimed oldest-first under per-flow / per-type concurrency caps
//     (ClaimNextFlowRun, ClaimNextNodeRun, ClaimNextAgentRun).
//   - MarkInterrupted runs once at startup: running work becomes interrupted,
//     pending tool_call approvals expire, pending proposals are untouched.
//   - Prune never removes a flow run linked to a starred inbox item or a pending
//     approval (nor the events, node/agent runs and approvals under it), keeps dedup keys
//     for their own, longer retention, and reports blobs and worktrees that
//     became unreferenced instead of deleting them.
//   - Change notifications (SetNotifier) fire after commit, never for rolled-back
//     work. Bulk deletes by Prune are reported once per resource with ID "*".
//
// Secret redaction happens before data reaches the store (the runner redacts);
// the store records what it is given.
package store

# hal9001 REST API

The contract is [`api/openapi.yaml`](../api/openapi.yaml) (OpenAPI 3.1). The web UI and the `hal9001` CLI are clients of it; there are no private endpoints. This guide covers what the schema cannot say well: auth, pagination, long-polling, the event stream, errors, and an end-to-end `curl` session.

Go code reaches the contract through `hal9001/internal/apispec`:

| Function | Use |
|---|---|
| `Load()` / `MustLoad()` | Parsed and validated `*openapi3.T` (shared, read-only). |
| `ServiceFor(method, path)` | Auth service (`ui`, `api`, `approve`, `admin`) that guards a request, from `x-hal-service`. Non-`/api/` GETs that match no operation are the UI and return `ui`; unknown paths return `""`. |
| `IsPublic(method, path)` | True for operations marked `x-hal-public` (login, whoami, logout, health). |
| `ValidateResponse(t, req, resp)` | Test helper: fails the test unless `resp` is a response the contract allows for `req` (status, content type, JSON body schema). |

## Request rules

- Base URL `http://127.0.0.1:9001`. The `Host` header must be `localhost:9001`, `127.0.0.1:9001`, or an entry of `allowed_hosts`, otherwise `403 host-not-allowed`.
- Every request other than GET/HEAD must send `Content-Type: application/json`, even with an empty body (use `{}` or no body). Otherwise `415 unsupported-media-type`. A request with an `Origin` header that is not an allowed origin gets `403 origin-not-allowed`. Browsers send `Origin` on cross-site writes; `curl` does not.
- JSON bodies use snake_case and reject unknown fields (`400 bad-request`).
- Times are RFC 3339 UTC with nanoseconds. IDs of runs, events, approvals, and inbox items are ULIDs; sensors, flows, prompts, and runtimes are addressed by file name (`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`).

## Auth model

Each operation declares its service in `x-hal-service`:

| Service | Operations |
|---|---|
| `ui` | The login, logout, and whoami calls. The UI shell and its assets are public (they hold no data and contain the login page); every `/api/` route is guarded. |
| `api` | Reads, the event stream, and the compute-only POSTs (`/api/flows/validate`, `/api/sensors/validate`, `/api/prompts/render`, `/api/transform/preview`, `/api/transform/compile`). |
| `approve` | `POST /api/approvals/{id}/approve` and `/deny`. |
| `admin` | Everything that writes files, runs code, or changes state: flows, sensors, prompts, env, settings, kill switch, queue removal and clearing, event injection, replay, test-node, runtime probes, inbox flags, session and passkey management. Auth reads that expose security data (sessions, auth events, passkeys, Tailscale status) are also `admin`. |

The `config.yaml` stack for the service decides who gets in (REQUIREMENTS §10.4). With the default config a local `curl` passes through `localhost-trust` and needs no credentials. When the stack does not admit you:

1. The API answers `401 unauthenticated`. The problem body has `service` and `methods`, the interactive modules of that service's stack (for example `["webauthn", "password"]`).
2. `POST /api/auth/{service}/begin` with `{"method": "password", "password": "..."}`. A module that needs more input answers `200 {"status": "challenge", "login_id": ..., "step": {...}}`; reply with `POST /api/auth/{service}/continue` `{"login_id": ..., "response": ...}`.
3. Success is `200 {"status": "success", "services": [...], "session": {...}}` and sets the `hal9001_session` cookie (`HttpOnly`, `SameSite=Strict`, `Secure` over HTTPS). Send it back on later requests (`curl -c jar -b jar`). One session covers every service its stack evaluation granted; there is no step-up.
4. A failed login is `401 authentication-failed`. A session that does not grant the service gets `403 forbidden`.

`GET /api/auth/whoami` always answers 200; `authenticated: false` means show a login. Passkey registration (`/api/auth/webauthn/register/*`) is allowed for any caller the `admin` service admits (a session, or the stack itself such as `localhost-trust`); begin and finish must come from the same caller.

## Pagination

Collections that can grow (events, flow runs, sensor runs, agent runs, approvals, inbox, flow versions) return `{"items": [...], "next_cursor": "..."|null}`, newest first. Pass `next_cursor` back as `?cursor=`; `?limit=` is 1 to 200, default 50. Cursors are opaque and valid for the data as it was when issued; new items never shift a cursor. Small bounded collections (sensors, flows, prompts, runtimes, env keys) return `{"items": [...]}` without a cursor.

Agent transcripts page by sequence instead: `GET /api/agent-runs/{id}/events?after_seq=N&limit=M` returns `{"items", "last_seq", "has_more"}`.

Files that live under the home dir (sensors, flows, prompts, settings) carry a `hash`, the lowercase hex SHA-256 of the file bytes. To change one, send the hash from your last read as `expected_hash`; if the file changed on disk since, the answer is `409 conflict` with `current_hash`, and nothing is written. Creates need no hash; an existing id is `409 already-exists`.

## Long polling

`GET /api/flow-runs/{id}?wait=30s` returns as soon as the run is terminal (`succeeded`, `failed`, `killed`, `interrupted`, `cancelled`) or `awaiting_approval`, or when the wait elapses (cap 5 minutes). It always answers 200 with the current state, so check `flow_run.status` rather than the HTTP status. The same `wait` parameter works on `GET /api/sensor-runs/{id}` (until not `running`), `GET /api/node-runs/{id}`, and `POST /api/flows/{id}/test-node` (default 30s; 202 when the node is still running).

## Event stream

`GET /api/stream?types=flow_run.updated,approval.created` is `text/event-stream`. Each message:

```
id: 1042
event: flow_run.updated
data: {"type":"flow_run.updated","id":"1042","ts":"2026-11-05T10:00:00.123456789Z","data":{...FlowRun...}}
```

- `types` is a comma-separated subset of `StreamEventType`; omit it for everything. `sensor_id`, `flow_id`, `flow_run_id`, `agent_run_id`, and `assist_session_id` narrow the types that carry that resource.
- Ordering: messages are delivered in the order the daemon committed the changes they announce, one at a time, so for one record (an assist session, its messages) updates never arrive out of order. The hub reads the record when it handles a change, so a message carries the record's state at that moment, which can already include a later change (an `assist.updated` for two quick updates may show the second state twice). A change committed just before a client connects can still be delivered after `stream.ready`, for example the `assist.updated` of a session created a moment earlier. Clients must treat events as "this record changed", not as a log to replay: match on the record id, expect an event for a record they already fetched, and refetch on `stream.resync`.
- `stream.ready` is the first message. `Last-Event-ID` resumes from the server's buffer; when the gap is too large the server sends `stream.resync` and the client must refetch what it shows.
- Payloads are the same records the REST endpoints return, with text previews cut to 4 KiB. The envelope schema of every type is listed under `x-hal-sse-events` on the stream operation.
- Types: `status.changed`, `killswitch.changed`, `config.changed`, `sensor_run.{created,updated}`, `event.created`, `flow_run.{created,updated}`, `node_run.{created,updated}`, `agent_run.{created,updated}`, `agent_event.appended`, `permission_request.{created,updated}`, `approval.{created,updated}`, `inbox.{created,updated}`, `auth_event.created`, `assist.updated`, `assist.message` (see [assist.md](assist.md)).

## Errors

Every error is `application/problem+json` (RFC 9457). Branch on `type`, not on `title` or `detail`:

| `type` (prefix `urn:hal9001:problem:`) | Status | Meaning and extension members |
|---|---|---|
| `bad-request` | 400 | Malformed JSON, unknown field, bad query or path parameter. |
| `unauthenticated` | 401 | No principal for the service. `service`, `methods`, optional `next_step`. |
| `authentication-failed` | 401 | A login attempt failed. `service`, `methods`, `trace`. |
| `forbidden` | 403 | Session does not grant the service. |
| `host-not-allowed` | 403 | `Host` check failed. |
| `origin-not-allowed` | 403 | `Origin` check failed. |
| `not-found` | 404 | Unknown id, name, or hash. |
| `conflict` | 409 | `expected_hash` does not match. `expected_hash`, `current_hash`. |
| `already-exists` | 409 | Create of an existing id. |
| `invalid-state` | 409 | The resource cannot take the operation now: approving a decided approval, polling a sensor that is already polling. `current_state`. |
| `login-expired` | 410 | Login exchange or passkey registration expired. |
| `payload-too-large` | 413 | Body over the limit. |
| `unsupported-media-type` | 415 | Mutating request without `Content-Type: application/json`. |
| `validation-failed` | 422 | Content is invalid. `diagnostics`: `[{severity, code, message, file, path, node_id?, line?}]`. |
| `paused` | 423 | Kill switch is engaged and the operation would start work (poll, test-node). |
| `internal` | 500 | Bug. |
| `unavailable` | 503 | A dependency the operation needs is down. |

While the kill switch is engaged, event injection, replay, and approvals are still accepted and their runs stay `queued` until resume (`x-hal-paused: queue`); poll-now and test-node are refused with 423 (`x-hal-paused: reject`).

`POST /api/flows/validate` (and the sensor equivalent) is not an error path: it answers 200 with `valid` and `diagnostics` so an editor can show every finding at once. Saving the same content with `PUT`/`POST` answers 422 when there are `error` diagnostics; `warning` and `info` do not block.

## Curl walkthrough

Assumes the `triage-github` flow of REQUIREMENTS §6, where the `review-small` agent node runs in `approve` mode with `approve: proposal` and its `out` port feeds an inbox node. Inject a GitHub notification, wait for the proposal, approve it, and read the result.

```sh
H='Content-Type: application/json'
API=http://localhost:9001/api

# 1. Inject an item for the sensor. It goes through dedup and fan-out like a polled item.
curl -sS -X POST -H "$H" "$API/events?sensor_id=github-notifications" -d '{
  "id": "9001", "updated_at": "2026-11-05T10:00:00Z", "reason": "review_requested",
  "repository": {"name": "stork"}, "subject": {"title": "Fix retry loop", "url": "https://github.com/babylist/stork/pull/42"}
}' | tee /tmp/inject.json
RUN=$(jq -r '.flow_runs[0].id' /tmp/inject.json)
# Posting the same item again answers 200 with "deduplicated": true and creates nothing.

# 2. Long-poll until the run needs a decision (or finishes). Check .flow_run.status.
curl -sS "$API/flow-runs/$RUN?wait=60s" | jq '{status: .flow_run.status, nodes: [.node_runs[] | {node_id, status, decision, reason}]}'
# status is "awaiting_approval": classify emitted on port "small" and review-small produced a proposal.

# 3. Find and approve the proposal.
AID=$(curl -sS "$API/approvals?status=pending&type=proposal" | jq -r '.items[0].id')
curl -sS "$API/approvals/$AID" | jq -r '.proposal_md'
curl -sS -X POST -H "$H" "$API/approvals/$AID/approve" -d '{"comment": "Go ahead, but skip the docs."}' | jq '{status, decided_at}'
# To refuse: POST .../deny {"comment": "..."} routes the node to port "denied".
# A tool_call approval: .../approve allows the call, .../deny rejects it and the agent continues,
# .../deny with {"cancel_run": true} ends the run.

# 4. Wait for the resumed run to finish, then look at what the agent did.
curl -sS "$API/flow-runs/$RUN?wait=300s" | jq '.flow_run.status'
AGENT=$(curl -sS "$API/flow-runs/$RUN" | jq -r '.node_runs[] | select(.node_id == "review-small") | .id' \
  | xargs -I{} curl -sS "$API/node-runs/{}" | jq -r '.agent_runs[-1].id')
curl -sS "$API/agent-runs/$AGENT" | jq '{phase, resumed_via, status, stop_reason}'
curl -sS "$API/agent-runs/$AGENT/permission-requests" | jq '.items[] | {title, decision, decided_by, matched_rule}'
curl -sS "$API/agent-runs/$AGENT/raw" -o agent-run.jsonl        # raw JSON-RPC, one frame per line

# 5. Read the inbox, then mark the item read and starred.
curl -sS "$API/inbox?read=false" | jq '.items[] | {id, priority, title}'
ID=$(curl -sS "$API/inbox?read=false" | jq -r '.items[0].id')
curl -sS -X PATCH -H "$H" "$API/inbox/$ID" -d '{"read": true, "starred": true}' | jq '{read, starred}'
```

Clearing the inbox in bulk (`admin`; see `POST /api/inbox/bulk` in `api/openapi.yaml`):

```sh
# Count first (changes nothing), then archive everything read and older than 7 days.
F='{"read": true, "older_than": "7d"}'
curl -sS -X POST -H "$H" "$API/inbox/bulk" -d "{\"action\":\"archive\",\"dry_run\":true,\"filter\":$F}" | jq '{matched, changed, skipped_starred}'
curl -sS -X POST -H "$H" "$API/inbox/bulk" -d "{\"action\":\"archive\",\"filter\":$F}" > cleared.json
# Undo: unarchive exactly the items that changed.
curl -sS -X POST -H "$H" "$API/inbox/bulk" -d "{\"action\":\"unarchive\",\"ids\":$(jq .changed_ids cleared.json)}"
```

- `action` is `archive` (reversible: items stay linked to their runs), `unarchive`, `mark_read`, `mark_unread` or `delete`. Send exactly one of `ids` (1 to 1000; unknown ids are ignored) or `filter`.
- `filter` takes the fields of `GET /api/inbox` (`read`, `starred`, `archived`, `priority`, `dry_run`, `flow_id`, `q`) as JSON, plus `max_priority` (`priority` is a minimum, so `"max_priority": "low"` selects only low items) and `older_than` (a duration such as `7d`, or an RFC 3339 time). The server evaluates it inside one transaction, so it covers every match, not just the rows a client has loaded. `filter.dry_run` means items of dry-run flows; the top-level `dry_run: true` previews the action.
- `filter.archived` defaults to unarchived items, except for `unarchive` and `delete`, which default to archived items.
- Starred items are never archived or deleted unless `include_starred: true`. `mark_read`, `mark_unread` and `unarchive` ignore stars.
- `delete` is permanent and only removes archived items. Other targets are skipped, not rejected: they are counted in `skipped_not_archived`.
- The result is `{matched, changed, skipped_starred, skipped_not_archived, changed_ids}`. `matched` includes skipped items; `changed` counts items actually modified (or that a preview would modify).
- Each changed item emits `inbox.updated`; a delete emits `stream.resync` (reason `pruned`). `status.changed` carries the new `unread_inbox`.

Other everyday calls:

```sh
# Edit a flow without a YAML parser on the client: read, change the JSON, write back with the hash.
curl -sS "$API/flows/triage-github" > flow.json
jq '.flow.max_concurrent = 3' flow.json \
  | jq '{expected_hash: .hash, flow: .flow}' \
  | curl -sS -X PUT -H "$H" "$API/flows/triage-github" -d @- | jq '{hash, diagnostics}'

# Check a node on sample input without creating timeline noise (kind: test).
curl -sS -X POST -H "$H" "$API/flows/triage-github/test-node" -d '{
  "node_id": "only-review-requests",
  "input": {"source": "json", "json": {"reason": "review_requested"}}
}' | jq '.node_run | {status, decision, stdout: .stdout.text, exit_code}'

# Replay a stored event into the current version of a flow.
curl -sS -X POST -H "$H" "$API/events/$EVENT_ID/replay" -d '{"flow": "triage-github", "dry_run": true}' | jq '{id, replay_of, flow_hash}'

# Kill switch.
curl -sS -X POST -H "$H" "$API/killswitch/pause" -d '{"kill_running": true, "reason": "maintenance"}'
curl -sS -X POST -H "$H" "$API/killswitch/resume"

# The queue: runs waiting for a flow, agent or command slot, in claim order.
curl -sS "$API/queue" | jq '{total, items: [.items[] | {position, run: .flow_run.id, waiting_for}]}'
# Remove one (the run ends `cancelled` and stays in history; 409 once it runs a node), or clear them all.
curl -sS -X DELETE -H "$H" "$API/queue/$RUN" | jq '{status, error, cancelled_by}'
curl -sS -X POST -H "$H" "$API/queue/clear" -d '{"flow_id": "triage-github"}'   # flow_id is optional

# Watch everything live.
curl -sS -N "$API/stream?types=flow_run.updated,approval.created,inbox.created"
```

import { useEffect, useMemo } from "react";
import { Link } from "react-router";
import { useAgentTranscript } from "../../api/runs";
import { buildTranscript } from "../runs/transcript";
import type { Approval } from "../../api/approvals";
import { JsonView } from "../../components/JsonView";
import { Markdown } from "../../components/Markdown";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  Field,
  Kbd,
  Panel,
  StatusBadge,
  TextArea,
} from "../../components/ui";
import { shortId } from "../../lib/format";
import { TypeBadge } from "./ApprovalList";
import { Countdown } from "./Countdown";
import { timeLeft, useNow } from "./timeLeft";
import { diffsFromToolContent, extractCommand, extractDiffs } from "./diff";
import { DiffView } from "./DiffView";
import { approvalTitle, describeDecision, isOperatorApproval } from "./queue";
import { EditFields } from "./EditFields";
import { initialDrafts } from "./edits";

const STALE_POLL_MS = 3000;

function Context({ a }: { a: Approval }) {
  const cls = "text-accent underline";
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted">
      {a.flow_id && (
        <Link to={`/flows/${a.flow_id}`} className={cls}>
          flow {a.flow_id}
        </Link>
      )}
      {a.node_id && <span>node {a.node_id}</span>}
      {isOperatorApproval(a) && <span>MCP client</span>}
      {a.assist_session_id && (
        <Link to={`/sensors?assist=${a.assist_session_id}`} className={cls}>
          assist session {shortId(a.assist_session_id)}
        </Link>
      )}
      {a.type !== "request" && <span>runtime {a.runtime}</span>}
      {a.flow_run_id && (
        <Link to={`/runs/${a.flow_run_id}`} className={cls}>
          run {shortId(a.flow_run_id)}
        </Link>
      )}
      {a.agent_run_id && (
        <Link to={`/agent-runs/${a.agent_run_id}`} className={cls}>
          agent run {shortId(a.agent_run_id)}
        </Link>
      )}
      {a.event_id && (
        <Link to={`/events/${a.event_id}`} className={cls}>
          event {shortId(a.event_id)}
        </Link>
      )}
    </div>
  );
}

function ToolCall({ a }: { a: Approval }) {
  const req = a.permission_request;
  if (!req) {
    return <p className="m-0 text-xs text-muted">No tool call was recorded.</p>;
  }
  return <ToolCallBody a={a} req={req} />;
}

function ToolCallBody({
  a,
  req,
}: {
  a: Approval;
  req: NonNullable<Approval["permission_request"]>;
}) {
  const command = extractCommand(req.raw_input);
  const transcript = useAgentTranscript(a.agent_run_id ?? "");
  const diffs = useMemo(() => {
    const fromInput = extractDiffs(req.raw_input);
    if (fromInput.length > 0 || !transcript.data) return fromInput;
    for (const item of buildTranscript(transcript.data)) {
      if (item.type === "tool" && item.toolCallId === req.tool_call_id) {
        return diffsFromToolContent(item.content);
      }
    }
    return fromInput;
  }, [req.raw_input, req.tool_call_id, transcript.data]);
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{req.title}</span>
        <Badge>{req.tool_kind}</Badge>
      </div>
      {command !== null && (
        <pre className="m-0 overflow-auto whitespace-pre-wrap break-words rounded-md border border-line bg-raised p-2 font-mono text-xs">
          {command}
        </pre>
      )}
      {diffs.map((d, i) => (
        <DiffView key={`${i}:${d.path ?? ""}`} diff={d} />
      ))}
      <details>
        <summary className="cursor-pointer text-xs text-muted">
          Raw input
        </summary>
        <div className="mt-2">
          <JsonView value={req.raw_input} />
        </div>
      </details>
      {req.options.length > 0 && (
        <div className="text-[11px] text-muted">
          Options offered to the agent:{" "}
          {req.options.map((o) => `${o.name} (${o.kind})`).join(", ")}
        </div>
      )}
    </div>
  );
}

/** What an approval node asks: a dry-run notice, the body, and the fields table. */
export function RequestBody({
  request,
}: {
  request: Pick<
    NonNullable<Approval["request"]>,
    "dry_run" | "body_md" | "fields"
  >;
}) {
  return (
    <div className="space-y-3">
      {request.dry_run && (
        <div className="flex items-center gap-2">
          <Badge tone="warn">DRY RUN</Badge>
        </div>
      )}
      {request.body_md ? (
        <Markdown>{request.body_md}</Markdown>
      ) : (
        <p className="m-0 text-xs text-muted">No details.</p>
      )}
      {request.fields.length > 0 && (
        <table className="w-full border-collapse text-xs">
          <tbody>
            {request.fields.map((f) => (
              <tr key={f.key} className="border-t border-line">
                <th className="w-1/3 py-1 pr-3 text-left font-normal text-muted">
                  {f.key}
                </th>
                <td className="break-words py-1">{f.value}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function editText(v: unknown): string {
  return typeof v === "string" ? v : JSON.stringify(v, null, 2);
}

/** The decided state: who decided, when, and with what comment. */
function Decision({ a }: { a: Approval }) {
  return (
    <Panel title="Decision">
      <div className="space-y-1 text-xs">
        <div className="flex items-center gap-2">
          <StatusBadge status={a.status} />
          <span>{describeDecision(a)}</span>
          <Time ts={a.decided_at} />
        </div>
        {a.comment && (
          <p className="m-0 whitespace-pre-wrap break-words">{a.comment}</p>
        )}
        {a.request?.edits && Object.keys(a.request.edits).length > 0 && (
          <div className="space-y-1.5" data-testid="approval-edits">
            <div className="font-medium">Edited before approving</div>
            {Object.entries(a.request.edits).map(([path, e]) => (
              <div key={path}>
                <div className="font-mono text-muted">{path}</div>
                <pre className="m-0 whitespace-pre-wrap break-words text-bad line-through">
                  {editText(e.before)}
                </pre>
                <pre className="m-0 whitespace-pre-wrap break-words">
                  {editText(e.after)}
                </pre>
              </div>
            ))}
          </div>
        )}
      </div>
    </Panel>
  );
}

/** One approval with its context and, while pending, the decision controls. */
export function ApprovalDetail({
  approval: a,
  comment,
  onComment,
  editDrafts,
  onEditDraft,
  busy,
  onDecide,
  onStale,
  backTo,
}: {
  approval: Approval;
  comment: string;
  onComment: (text: string) => void;
  /** Box text of the editable fields, by path (missing = the current value). */
  editDrafts: Readonly<Record<string, string>>;
  onEditDraft: (key: string, text: string) => void;
  busy: boolean;
  onDecide: (action: "approve" | "deny", cancelRun?: boolean) => void;
  /** Called every few seconds while a pending call is past its deadline, to refetch. */
  onStale: () => void;
  backTo: { pathname: string; search: string };
}) {
  const pending = a.status === "pending";
  const now = useNow();
  const left = pending ? timeLeft(a.expires_at, now) : null;
  const pastDeadline = left?.expired === true;

  useEffect(() => {
    if (!pastDeadline) return;
    onStale();
    const t = setInterval(onStale, STALE_POLL_MS);
    return () => clearInterval(t);
  }, [pastDeadline, onStale]);

  const isTool = a.type === "tool_call";
  return (
    <article className="mx-auto max-w-3xl space-y-4 p-4">
      <Link to={backTo} className="text-xs text-accent underline md:hidden">
        Back to list
      </Link>
      <header className="space-y-2">
        <h2 className="m-0 text-base font-semibold">{approvalTitle(a)}</h2>
        <div className="flex flex-wrap items-center gap-1.5">
          <TypeBadge type={a.type} />
          <StatusBadge status={a.status} />
          <Time ts={a.created_at} />
          {pending && a.expires_at && (
            <span className="text-xs text-muted">
              time left <Countdown expiresAt={a.expires_at} />
            </span>
          )}
        </div>
        <Context a={a} />
      </header>

      {pastDeadline && (
        <div
          role="status"
          className="rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn"
        >
          Past the approval timeout. Waiting for the daemon to mark it expired.
        </div>
      )}

      {a.request ? (
        <Panel title="Request">
          <RequestBody request={a.request} />
        </Panel>
      ) : isTool ? (
        <Panel title="Tool call">
          <ToolCall a={a} />
        </Panel>
      ) : (
        <Panel title="Proposal">
          {a.proposal_md ? (
            <Markdown>{a.proposal_md}</Markdown>
          ) : (
            <p className="m-0 text-xs text-muted">No proposal text.</p>
          )}
        </Panel>
      )}

      {pending ? (
        <Panel title="Decide">
          <div className="space-y-3">
            {a.request && a.request.editable.length > 0 && (
              <div className="space-y-1">
                <p className="m-0 text-xs text-muted">
                  You can edit these before approving. Denying ignores edits.
                </p>
                <EditFields
                  editable={a.request.editable}
                  drafts={{
                    ...initialDrafts(a.request.editable),
                    ...editDrafts,
                  }}
                  onChange={onEditDraft}
                />
              </div>
            )}
            <Field label="Comment (optional)">
              <TextArea
                rows={3}
                value={comment}
                onChange={(e) => onComment(e.target.value)}
                placeholder="Recorded with the decision"
              />
            </Field>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="primary"
                icon="check"
                disabled={busy}
                onClick={() => onDecide("approve")}
              >
                {isTool ? "Allow" : "Approve"} <Kbd>a</Kbd>
              </Button>
              <Button
                variant="danger"
                icon="close"
                disabled={busy}
                onClick={() => onDecide("deny")}
              >
                Deny <Kbd>d</Kbd>
              </Button>
              {isTool && (
                <Button
                  variant="danger"
                  icon="stop"
                  disabled={busy}
                  onClick={() => onDecide("deny", true)}
                >
                  Deny and cancel run <Kbd>c</Kbd>
                </Button>
              )}
            </div>
          </div>
        </Panel>
      ) : (
        <Decision a={a} />
      )}
    </article>
  );
}

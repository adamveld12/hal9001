import { useEffect, useState } from "react";
import { Link } from "react-router";
import type { Approval } from "../../api/approvals";
import { JsonView } from "../../components/JsonView";
import { Markdown } from "../../components/Markdown";
import { Time } from "../../components/Time";
import { Badge, Button, Kbd, StatusBadge, TextArea } from "../../components/ui";
import { cx } from "../../lib/cx";
import { shortId } from "../../lib/format";
import { PRIORITY_LABEL, PRIORITY_TONE } from "../inbox/model";
import { TypeBadge } from "./badges";
import {
  agentWritten,
  cardActions,
  conversationPath,
  decisionFor,
  detailDiffs,
  detailSummary,
  hasDetails,
  originText,
  type DecideAction,
  type DecisionInput,
} from "./card";
import { Countdown } from "./Countdown";
import { DiffView } from "./DiffView";
import { EditFields } from "./EditFields";
import { initialDrafts } from "./edits";
import { describeDecision } from "./queue";
import { clipReason } from "./reason";
import { timeLeft, useNow } from "./timeLeft";

const STALE_POLL_MS = 3000;

export interface ApprovalCardProps {
  approval: Approval;
  /** `full`: every panel (Inbox, Approvals page). `compact`: title, description, one-line details, actions (chat). */
  variant: "full" | "compact";
  /** Text of the `Additional steering` box. */
  steering: string;
  onSteering: (text: string) => void;
  /** Box text of the editable fields, by path (missing = the current value). */
  editDrafts?: Readonly<Record<string, string>>;
  onEditDraft?: (key: string, text: string) => void;
  busy: boolean;
  onDecide: (decision: DecisionInput) => void;
  /** Called every few seconds while a pending call is past its deadline, to refetch. */
  onStale?: () => void;
  /** Failure or outcome text of the last decision. */
  message?: { tone: "bad" | "info"; text: string } | null;
  /** Show the a / d / c shortcut hints (Inbox detail). */
  hotkeys?: boolean;
}

function SourceLine({ a }: { a: Approval }) {
  const cls = "text-accent underline";
  const path = conversationPath(a);
  return (
    <div
      className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted"
      data-testid="approval-source"
    >
      <span className="font-semibold uppercase tracking-wide">Source</span>
      <span className="text-fg">{originText(a.origin)}</span>
      {a.flow_id && (
        <Link to={`/flows/${a.flow_id}`} className={cls}>
          flow {a.flow_id}
        </Link>
      )}
      {a.node_id && <span>node {a.node_id}</span>}
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
      {path && (
        <Link to={path} className={cls}>
          Open conversation
        </Link>
      )}
    </div>
  );
}

function Description({ a }: { a: Approval }) {
  return (
    <section aria-label="Description" className="min-w-0 space-y-1.5">
      <div className="flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wide text-muted">
        Description
        {agentWritten(a) && <Badge tone="info">agent-written</Badge>}
      </div>
      <Markdown>{a.description}</Markdown>
    </section>
  );
}

/** The policy or emit reason of a pending tool call, monospace and wrapped, clipped with an expand control. */
function ReasonBlock({ reason }: { reason: string }) {
  const [open, setOpen] = useState(false);
  const clip = clipReason(reason);
  return (
    <div>
      <div className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-muted">
        Reason
      </div>
      <pre className="m-0 overflow-auto whitespace-pre-wrap break-words rounded-md border border-line bg-raised p-2 font-mono text-xs">
        {open ? reason : clip.text}
      </pre>
      {clip.clipped && (
        <button
          type="button"
          className="mt-1 text-xs text-accent underline"
          onClick={() => setOpen(!open)}
        >
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </div>
  );
}

function Details({ a }: { a: Approval }) {
  const d = a.details;
  const diffs = detailDiffs(a);
  const body = d.body_md.trim() !== "" ? d.body_md : (a.proposal_md ?? "");
  const raw = a.permission_request?.raw_input;
  return (
    <section aria-label="Details" className="min-w-0 space-y-2">
      <div className="flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wide text-muted">
        Details
        {d.tool && <Badge>{d.tool}</Badge>}
      </div>
      {d.command && (
        <pre className="m-0 overflow-auto whitespace-pre-wrap break-words rounded-md border border-line bg-raised p-2 font-mono text-xs">
          {d.command}
        </pre>
      )}
      {a.status === "pending" && a.permission_request?.reason && (
        <ReasonBlock reason={a.permission_request.reason} />
      )}
      {diffs.map((diff, i) => (
        <DiffView key={`${i}:${diff.path ?? ""}`} diff={diff} />
      ))}
      {d.fields.length > 0 && (
        <table className="w-full border-collapse text-xs">
          <tbody>
            {d.fields.map((f) => (
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
      {body.trim() !== "" && <Markdown>{body}</Markdown>}
      {raw !== undefined && (
        <details>
          <summary className="cursor-pointer text-xs text-muted">
            Raw input
          </summary>
          <div className="mt-2">
            <JsonView value={raw} />
          </div>
        </details>
      )}
      {!hasDetails(a) && <p className="m-0 text-xs text-muted">No details.</p>}
    </section>
  );
}

function editText(v: unknown): string {
  return typeof v === "string" ? v : JSON.stringify(v, null, 2);
}

/** The decided state: who decided, when, the steering text and any edits. */
function Decided({ a }: { a: Approval }) {
  const edits = a.edits ? Object.entries(a.edits) : [];
  return (
    <div className="space-y-1 text-xs" data-testid="approval-decision">
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge status={a.status} />
        <span>{describeDecision(a)}</span>
        <Time ts={a.decided_at} />
      </div>
      {a.comment && (
        <p className="m-0 whitespace-pre-wrap break-words">
          <span className="text-muted">Steering: </span>
          {a.comment}
        </p>
      )}
      {edits.length > 0 && (
        <div className="space-y-1.5" data-testid="approval-edits">
          <div className="font-medium">Edited before approving</div>
          {edits.map(([path, e]) => (
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
  );
}

function Actions({
  a,
  steering,
  onSteering,
  busy,
  onAct,
  hotkeys,
}: {
  a: Approval;
  steering: string;
  onSteering: (text: string) => void;
  busy: boolean;
  onAct: (action: DecideAction) => void;
  hotkeys: boolean;
}) {
  const acts = cardActions(a);
  const [open, setOpen] = useState(false);
  const boxShown = acts.steering && (open || steering !== "");
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="primary"
          icon="check"
          disabled={busy}
          onClick={() => onAct("approve")}
        >
          Approve {hotkeys && <Kbd>a</Kbd>}
        </Button>
        <Button
          variant="danger"
          icon="close"
          disabled={busy}
          onClick={() => onAct("deny")}
        >
          Deny {hotkeys && <Kbd>d</Kbd>}
        </Button>
        {acts.steering && (
          <Button
            variant="ghost"
            aria-expanded={boxShown}
            onClick={() => setOpen(!boxShown)}
          >
            Additional steering
          </Button>
        )}
        {acts.denyStop && (
          <Button
            variant="danger"
            icon="stop"
            disabled={busy}
            title="Deny and end the run."
            onClick={() => onAct("deny_stop")}
          >
            Deny + stop {hotkeys && <Kbd>c</Kbd>}
          </Button>
        )}
      </div>
      {boxShown && (
        <TextArea
          rows={3}
          aria-label="Additional steering"
          value={steering}
          onChange={(e) => onSteering(e.target.value)}
          placeholder="Tell the agent what to do instead. Sent with Deny or Deny + stop."
        />
      )}
    </div>
  );
}

/**
 * One approval, the same everywhere (Inbox, Approvals page, chat). Controlled:
 * the steering and edit drafts live in the host, which also sends the decision.
 */
export function ApprovalCard({
  approval: a,
  variant,
  steering,
  onSteering,
  editDrafts = {},
  onEditDraft,
  busy,
  onDecide,
  onStale,
  message,
  hotkeys = false,
}: ApprovalCardProps) {
  const compact = variant === "compact";
  const pending = a.status === "pending";
  const now = useNow();
  const left = pending ? timeLeft(a.expires_at, now) : null;
  const pastDeadline = left?.expired === true;

  useEffect(() => {
    if (!pastDeadline || !onStale) return;
    onStale();
    const t = setInterval(onStale, STALE_POLL_MS);
    return () => clearInterval(t);
  }, [pastDeadline, onStale]);

  const act = (action: DecideAction) => {
    const d = decisionFor(a, action, steering);
    if (d) onDecide(d);
  };
  const summary = compact ? detailSummary(a) : null;
  const path = conversationPath(a);

  return (
    <article
      aria-label={a.title}
      data-approval-id={a.id}
      className={cx(
        compact
          ? "space-y-2 rounded-md border border-warn/40 bg-warn/5 p-2 text-xs"
          : "mx-auto max-w-4xl space-y-4 p-4",
        compact && !pending && "border-line bg-panel",
      )}
    >
      <header className="space-y-1.5">
        <h2
          className={cx("m-0 font-semibold", compact ? "text-xs" : "text-base")}
        >
          {a.title}
        </h2>
        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          <TypeBadge type={a.type} gate={a.gate} />
          <StatusBadge status={a.status} />
          {a.priority !== "normal" && (
            <Badge tone={PRIORITY_TONE[a.priority]}>
              {PRIORITY_LABEL[a.priority]}
            </Badge>
          )}
          {a.details.dry_run && <Badge tone="warn">dry run</Badge>}
          {!compact && <Time ts={a.created_at} />}
          {pending && a.expires_at && (
            <span className="text-muted">
              time left <Countdown expiresAt={a.expires_at} />
            </span>
          )}
        </div>
      </header>

      {pastDeadline && (
        <div
          role="status"
          className="rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn"
        >
          Past the approval timeout. Waiting for the daemon to mark it expired.
        </div>
      )}

      {pending && a.held && (
        <div
          role="note"
          className="rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn"
        >
          {a.held_note ||
            "Held: the steering limit for this conversation was reached. The agent was told to stop and wait."}
        </div>
      )}

      {compact ? (
        <div className="space-y-1.5">
          <div className="whitespace-pre-wrap break-words">
            {agentWritten(a) && (
              <Badge tone="info" className="mr-1.5">
                agent-written
              </Badge>
            )}
            {a.description}
          </div>
          {summary && (
            <code
              className="block truncate rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-[11px]"
              title={summary}
            >
              {summary}
            </code>
          )}
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          <Description a={a} />
          <Details a={a} />
        </div>
      )}

      {!compact && <SourceLine a={a} />}

      {pending && !compact && a.editable.length > 0 && onEditDraft && (
        <div className="space-y-1">
          <p className="m-0 text-xs text-muted">
            You can edit these before approving. Denying ignores edits.
          </p>
          <EditFields
            editable={a.editable}
            drafts={{ ...initialDrafts(a.editable), ...editDrafts }}
            onChange={onEditDraft}
          />
        </div>
      )}

      {pending ? (
        <>
          {compact && a.editable.length > 0 && path && (
            <p className="m-0 text-muted">
              Editable before approving:{" "}
              <Link
                to={`/inbox/approval/${a.id}`}
                className="text-accent underline"
              >
                open in the Inbox
              </Link>
              .
            </p>
          )}
          <Actions
            a={a}
            steering={steering}
            onSteering={onSteering}
            busy={busy}
            onAct={act}
            hotkeys={hotkeys}
          />
        </>
      ) : (
        <Decided a={a} />
      )}

      {message && (
        <div
          role={message.tone === "bad" ? "alert" : "status"}
          className={cx(
            "text-xs",
            message.tone === "bad" ? "text-bad" : "text-muted",
          )}
        >
          {message.text}
        </div>
      )}

      {compact && (
        <div className="flex flex-wrap items-center gap-x-3 text-[11px] text-muted">
          <span>{originText(a.origin)}</span>
          {path && (
            <Link to={path} className="text-accent underline">
              Open conversation
            </Link>
          )}
        </div>
      )}
    </article>
  );
}

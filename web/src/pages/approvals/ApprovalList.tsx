import { Link } from "react-router";
import type { Approval } from "../../api/approvals";
import { Time } from "../../components/Time";
import { Badge, StatusBadge } from "../../components/ui";
import { Countdown } from "./Countdown";
import { approvalTitle } from "./queue";

export function TypeBadge({ type }: { type: Approval["type"] }) {
  return (
    <Badge tone={type === "tool_call" ? "run" : "info"}>
      {type === "tool_call"
        ? "tool call"
        : type === "request"
          ? "request"
          : "proposal"}
    </Badge>
  );
}

function Row({ approval: a }: { approval: Approval }) {
  const pending = a.status === "pending";
  return (
    <Link
      to={{ pathname: `/inbox/approval/${a.id}` }}
      className="block border-b border-line px-3 py-2 text-xs no-underline hover:bg-raised/60"
    >
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-medium">
          {approvalTitle(a)}
        </span>
        {pending && a.expires_at ? (
          <Countdown expiresAt={a.expires_at} />
        ) : (
          <Time ts={a.decided_at ?? a.created_at} />
        )}
      </div>
      <div className="mt-1 flex items-center gap-1.5 text-[11px] text-muted">
        <TypeBadge type={a.type} />
        {a.request?.dry_run && <Badge tone="warn">dry run</Badge>}
        {!pending && <StatusBadge status={a.status} />}
        {a.type === "tool_call" && a.permission_request && (
          <span>{a.permission_request.tool_kind}</span>
        )}
        <span className="truncate">
          {a.flow_id !== null ? `${a.flow_id} / ${a.node_id}` : "sensor assist"}
        </span>
        {pending && !a.expires_at && <Time ts={a.created_at} />}
      </div>
    </Link>
  );
}

export function ApprovalList({ items }: { items: readonly Approval[] }) {
  return (
    <div>
      {items.map((a) => (
        <Row key={a.id} approval={a} />
      ))}
    </div>
  );
}

import { Link } from "react-router";
import type { Approval } from "../../api/approvals";
import type { InboxItem, Priority } from "../../api/inbox";
import { Icon } from "../../components/icons";
import { Time } from "../../components/Time";
import { Badge } from "../../components/ui";
import { cx } from "../../lib/cx";
import { Countdown } from "../approvals/Countdown";
import { TypeBadge } from "../approvals/ApprovalList";
import {
  approvalOrigin,
  approvalTitle,
  isGateApproval,
} from "../approvals/queue";
import type { InboxRow } from "./merge";
import {
  groupByPriority,
  PRIORITY_LABEL,
  PRIORITY_TONE,
  rowDomId,
} from "./model";

export function PriorityBadge({ priority }: { priority: Priority }) {
  return (
    <Badge tone={PRIORITY_TONE[priority]}>{PRIORITY_LABEL[priority]}</Badge>
  );
}

function Row({
  item,
  open,
  cursor,
  selected,
  onToggle,
  search,
}: {
  item: InboxItem;
  open: boolean;
  cursor: boolean;
  selected: boolean;
  onToggle: (id: string) => void;
  search: string;
}) {
  return (
    <div
      className={cx(
        "flex items-stretch border-b border-line",
        open ? "bg-raised" : selected ? "bg-raised/40" : "hover:bg-raised/60",
        cursor && "outline-1 -outline-offset-1 outline-accent",
      )}
    >
      <label className="flex shrink-0 cursor-pointer items-start px-3 pt-2.5">
        <input
          type="checkbox"
          aria-label={`Select ${item.title}`}
          checked={selected}
          onChange={() => onToggle(item.id)}
        />
      </label>
      <Link
        id={rowDomId(`n:${item.id}`)}
        to={{ pathname: `/inbox/${item.id}`, search }}
        aria-current={open ? "true" : undefined}
        className="block min-w-0 flex-1 py-2 pr-3 text-xs no-underline"
      >
        <div className="flex items-center gap-2">
          <span
            aria-label={item.read ? "Read" : "Unread"}
            className={cx(
              "size-1.5 shrink-0 rounded-full",
              item.read ? "bg-transparent" : "bg-accent",
            )}
          />
          <span
            className={cx(
              "min-w-0 flex-1 truncate",
              item.read ? "text-muted" : "font-semibold",
            )}
          >
            {item.title}
          </span>
          {item.starred && (
            <Icon
              name="star"
              size={12}
              className="shrink-0 fill-current text-warn"
              aria-label="Starred"
            />
          )}
          <Time ts={item.created_at} />
        </div>
        <div className="mt-1 flex items-center gap-1.5 pl-3.5 text-[11px] text-muted">
          {item.priority !== "normal" && (
            <PriorityBadge priority={item.priority} />
          )}
          <span className="truncate">
            {item.flow_id} / {item.node_id}
          </span>
          {item.dry_run && <Badge tone="info">dry-run</Badge>}
          {item.parse_error && <Badge tone="warn">parse error</Badge>}
          {item.archived && <Badge>archived</Badge>}
        </div>
      </Link>
    </div>
  );
}

function ApprovalRow({
  approval: a,
  open,
  cursor,
  search,
}: {
  approval: Approval;
  open: boolean;
  cursor: boolean;
  search: string;
}) {
  return (
    <div
      className={cx(
        "flex items-stretch border-b border-line",
        open ? "bg-raised" : "hover:bg-raised/60",
        cursor && "outline-1 -outline-offset-1 outline-accent",
      )}
    >
      <Link
        id={rowDomId(`a:${a.id}`)}
        to={{ pathname: `/inbox/approval/${a.id}`, search }}
        aria-current={open ? "true" : undefined}
        className="block min-w-0 flex-1 py-2 pr-3 pl-3 text-xs no-underline"
      >
        <div className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate font-semibold">
            {approvalTitle(a)}
          </span>
          {a.expires_at ? (
            <Countdown expiresAt={a.expires_at} />
          ) : (
            <Time ts={a.created_at} />
          )}
        </div>
        <div className="mt-1 flex items-center gap-1.5 text-[11px] text-muted">
          <Badge tone="warn">needs action</Badge>
          <TypeBadge type={a.type} gate={isGateApproval(a)} />
          <span className="truncate">{approvalOrigin(a)}</span>
        </div>
      </Link>
    </div>
  );
}

/**
 * Rows in order. When ordered by priority, the leading approvals sit under a
 * "Needs action" heading and the notes under priority headings; otherwise the
 * list is flat.
 */
export function InboxList({
  rows,
  grouped,
  openKey,
  cursorKey,
  selected,
  onToggle,
  search,
}: {
  rows: readonly InboxRow[];
  grouped: boolean;
  openKey: string | undefined;
  cursorKey: string | null;
  selected: ReadonlySet<string>;
  onToggle: (id: string) => void;
  search: string;
}) {
  const render = (list: readonly InboxRow[]) =>
    list.map((r) =>
      r.kind === "note" ? (
        <Row
          key={r.key}
          item={r.item}
          open={r.key === openKey}
          cursor={r.key === cursorKey}
          selected={selected.has(r.item.id)}
          onToggle={onToggle}
          search={search}
        />
      ) : (
        <ApprovalRow
          key={r.key}
          approval={r.approval}
          open={r.key === openKey}
          cursor={r.key === cursorKey}
          search={search}
        />
      ),
    );
  if (!grouped) return <div>{render(rows)}</div>;
  const heading =
    "sticky top-0 z-10 m-0 border-b border-line bg-bg px-3 py-1 text-[11px] font-medium text-muted";
  const acts = rows.filter((r) => r.kind === "approval");
  const noteItems = rows.flatMap((r) => (r.kind === "note" ? [r.item] : []));
  const byId = new Map(rows.map((r) => [r.key, r]));
  return (
    <div>
      {acts.length > 0 && (
        <section>
          <h2 className={heading}>
            Needs action
            <span className="ml-1.5 tabular-nums">{acts.length}</span>
          </h2>
          {render(acts)}
        </section>
      )}
      {groupByPriority(noteItems).map((g) => (
        <section key={g.priority}>
          <h2 className={heading}>
            {PRIORITY_LABEL[g.priority]}
            <span className="ml-1.5 tabular-nums">{g.items.length}</span>
          </h2>
          {render(g.items.map((i) => byId.get(`n:${i.id}`)!))}
        </section>
      ))}
    </div>
  );
}

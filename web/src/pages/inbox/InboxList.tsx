import { Link } from "react-router";
import type { InboxItem, Priority } from "../../api/inbox";
import { Icon } from "../../components/icons";
import { Time } from "../../components/Time";
import { Badge } from "../../components/ui";
import { cx } from "../../lib/cx";
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
  search,
}: {
  item: InboxItem;
  open: boolean;
  cursor: boolean;
  search: string;
}) {
  return (
    <Link
      id={rowDomId(item.id)}
      to={{ pathname: `/inbox/${item.id}`, search }}
      aria-current={open ? "true" : undefined}
      className={cx(
        "block border-b border-line px-3 py-2 text-xs no-underline",
        open ? "bg-raised" : "hover:bg-raised/60",
        cursor && "outline-1 -outline-offset-1 outline-accent",
      )}
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
  );
}

/** Rows grouped under priority headings (when ordered by priority) or as a flat list. */
export function InboxList({
  items,
  grouped,
  openId,
  cursorId,
  search,
}: {
  items: readonly InboxItem[];
  grouped: boolean;
  openId: string | undefined;
  cursorId: string | null;
  search: string;
}) {
  const rows = (list: readonly InboxItem[]) =>
    list.map((item) => (
      <Row
        key={item.id}
        item={item}
        open={item.id === openId}
        cursor={item.id === cursorId}
        search={search}
      />
    ));
  if (!grouped) return <div>{rows(items)}</div>;
  return (
    <div>
      {groupByPriority(items).map((g) => (
        <section key={g.priority}>
          <h2 className="sticky top-0 z-10 m-0 border-b border-line bg-bg px-3 py-1 text-[11px] font-medium text-muted">
            {PRIORITY_LABEL[g.priority]}
            <span className="ml-1.5 tabular-nums">{g.items.length}</span>
          </h2>
          {rows(g.items)}
        </section>
      ))}
    </div>
  );
}

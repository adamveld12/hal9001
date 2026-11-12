import { Link } from "react-router";
import type { Conversation } from "../../api/conversations";
import { Badge } from "../../components/ui";
import { cx } from "../../lib/cx";
import { relativeTime } from "../../lib/format";
import { runRelation } from "./filters";
import { chatPath } from "./path";
import { ConversationStatusPill } from "./StatusPill";
import {
  flowNodeLabel,
  forkCountLabel,
  forkOriginLabel,
  isLive,
  sourceLabel,
} from "./status";

/** One conversation in the list. The row of the open one is marked; `search` keeps the filters when it is picked. */
export function ConversationRow({
  c,
  selected,
  search,
  flowRun = "",
}: {
  c: Conversation;
  selected: boolean;
  search: string;
  /** The run the list is filtered by: marks forks and continuation-run conversations. */
  flowRun?: string;
}) {
  const relation = runRelation(c, flowRun);
  const flowNode = flowNodeLabel(c);
  const forks = forkCountLabel(c.fork_count);
  return (
    <li>
      <Link
        to={{ pathname: chatPath(c.id), search }}
        aria-current={selected ? "true" : undefined}
        className={cx(
          "block space-y-1 border-b border-line px-3 py-2 hover:bg-raised/60",
          selected && "bg-raised",
          isLive(c.status) && "border-l-2 border-l-run pl-2.5",
        )}
      >
        <div className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-[13px] font-medium">
            {c.title}
          </span>
          <ConversationStatusPill status={c.status} />
        </div>
        {c.preview && (
          <div className="truncate text-[11px] text-muted">{c.preview}</div>
        )}
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted">
          <Badge tone={c.source === "flow" ? "info" : "muted"}>
            {sourceLabel(c.source)}
          </Badge>
          {flowNode && <span className="truncate font-mono">{flowNode}</span>}
          {c.forked_from && (
            <span title={forkOriginLabel(c.forked_from)}>
              <Badge>fork</Badge>
            </span>
          )}
          {relation === "continuation" && (
            <span title="Agent conversation of a run continued from this one">
              <Badge>continuation</Badge>
            </span>
          )}
          {forks && <span>{forks}</span>}
          <span>{c.runtime}</span>
          <time
            dateTime={c.updated_at}
            title={c.updated_at}
            className="ml-auto whitespace-nowrap"
          >
            {relativeTime(c.updated_at)}
          </time>
        </div>
      </Link>
    </li>
  );
}

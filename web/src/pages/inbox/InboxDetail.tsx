import { Link } from "react-router";
import type { InboxItem, InboxPatch } from "../../api/inbox";
import { CopyButton } from "../../components/CopyButton";
import { Icon } from "../../components/icons";
import { Markdown } from "../../components/Markdown";
import { Time } from "../../components/Time";
import { Badge, Button, Kbd, Panel } from "../../components/ui";
import { shortId } from "../../lib/format";
import { PriorityBadge } from "./InboxList";
import { safeHref, tracePath } from "./model";

function TraceLinks({ item }: { item: InboxItem }) {
  const cls = "text-accent underline";
  if (item.source === "mcp") {
    return (
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted">
        <span>MCP client{item.client ? ` ${item.client}` : ""}</span>
        {item.thread_key && <span>thread {item.thread_key}</span>}
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted">
      <Link to={`/flows/${item.flow_id}`} className={cls}>
        flow {item.flow_id}
      </Link>
      <span>node {item.node_id}</span>
      <Link to={`/runs/${item.flow_run_id}`} className={cls}>
        run {shortId(item.flow_run_id)}
      </Link>
      {item.agent_run_id && (
        <Link to={`/agent-runs/${item.agent_run_id}`} className={cls}>
          agent run {shortId(item.agent_run_id)}
        </Link>
      )}
      {item.event_id && (
        <Link to={`/events/${item.event_id}`} className={cls}>
          event {shortId(item.event_id)}
        </Link>
      )}
    </div>
  );
}

/** One note: summary, suggested reply, links, and the read / star / archive actions. */
export function InboxDetail({
  item,
  onPatch,
  onArchive,
  backTo,
}: {
  item: InboxItem;
  /** Read and star changes. */
  onPatch: (patch: InboxPatch) => void;
  /** Archive or unarchive; the page moves on to the next note. */
  onArchive: () => void;
  /** Mobile back link to the list. */
  backTo: { pathname: string; search: string };
}) {
  return (
    <article className="mx-auto max-w-3xl space-y-4 p-4">
      <Link to={backTo} className="text-xs text-accent underline md:hidden">
        Back to list
      </Link>
      <header className="space-y-2">
        <h2 className="m-0 text-base font-semibold">{item.title}</h2>
        <div className="flex flex-wrap items-center gap-1.5">
          <PriorityBadge priority={item.priority} />
          {item.dry_run && <Badge tone="info">dry-run</Badge>}
          {item.parse_error && <Badge tone="warn">parse error</Badge>}
          {item.archived && <Badge>archived</Badge>}
          <Time ts={item.created_at} />
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            icon="check"
            onClick={() => onPatch({ read: !item.read })}
            aria-pressed={item.read}
          >
            {item.read ? "Mark unread" : "Mark read"} <Kbd>u</Kbd>
          </Button>
          <Button
            onClick={() => onPatch({ starred: !item.starred })}
            aria-pressed={item.starred}
          >
            <Icon
              name="star"
              size={14}
              className={item.starred ? "fill-current text-warn" : undefined}
            />
            {item.starred ? "Unstar" : "Star"} <Kbd>s</Kbd>
          </Button>
          <Button icon="archive" onClick={onArchive}>
            {item.archived ? "Unarchive" : "Archive"} <Kbd>e</Kbd>
          </Button>
          {item.source !== "mcp" && (
            <Link
              to={tracePath(item)}
              className="ml-auto inline-flex h-7 items-center gap-1.5 rounded-md border border-line bg-panel px-2.5 text-xs font-medium no-underline hover:bg-raised"
            >
              <Icon name="external" size={14} />
              Open trace
            </Link>
          )}
        </div>
        <TraceLinks item={item} />
      </header>

      {item.parse_error ? (
        <Panel title="Raw agent message">
          <p className="mb-2 mt-0 text-xs text-warn">
            The agent's note could not be parsed, so this is its final message
            as written.
          </p>
          <pre className="m-0 whitespace-pre-wrap break-words font-mono text-xs">
            {item.summary_md}
          </pre>
        </Panel>
      ) : (
        <Markdown>{item.summary_md}</Markdown>
      )}

      {item.suggested_reply && (
        <Panel
          title="Suggested reply"
          actions={
            <CopyButton text={item.suggested_reply} label="Copy reply" />
          }
        >
          <pre className="m-0 whitespace-pre-wrap break-words font-sans text-[13px]">
            {item.suggested_reply}
          </pre>
        </Panel>
      )}

      {item.links.length > 0 && (
        <Panel title="Links">
          <ul className="m-0 list-none space-y-1 p-0 text-xs">
            {item.links.map((l, i) => {
              const href = safeHref(l.url);
              return (
                <li key={`${i}:${l.url}`} className="flex gap-2">
                  {href ? (
                    <a
                      href={href}
                      target="_blank"
                      rel="noreferrer noopener"
                      className="text-accent underline"
                    >
                      {l.label || l.url}
                    </a>
                  ) : (
                    <span>{l.label}</span>
                  )}
                  <span className="min-w-0 truncate text-muted">{l.url}</span>
                </li>
              );
            })}
          </ul>
        </Panel>
      )}
    </article>
  );
}

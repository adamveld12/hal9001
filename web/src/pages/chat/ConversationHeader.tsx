import type { ReactNode } from "react";
import { Link, useLocation } from "react-router";
import type {
  ConversationDetail,
  ConversationStatus,
} from "../../api/conversations";
import { Badge } from "../../components/ui";
import { chatPath } from "./path";
import { ConversationStatusPill } from "./StatusPill";
import { conversationLinks, sourceLabel } from "./status";

const LINK = "text-accent hover:underline";

/**
 * The right pane's header: title, status, where the conversation came from
 * (links to the flow run, node, agent run and flow, or to the run a chat
 * continues), the chats continued from it and the actions. `status` and
 * `model` are the live values of a chat's session when it has loaded.
 */
export function ConversationHeader({
  c,
  status = c.status,
  model = c.model,
  actions,
}: {
  c: ConversationDetail;
  status?: ConversationStatus;
  model?: string | null;
  actions?: ReactNode;
}) {
  const { search } = useLocation();
  const links = conversationLinks(c);
  return (
    <header className="space-y-2 border-b border-line px-4 py-3">
      <div className="flex items-center gap-2">
        <Link
          to={{ pathname: chatPath(), search }}
          className="text-xs text-accent lg:hidden"
        >
          ← List
        </Link>
        <h2 className="m-0 min-w-0 flex-1 truncate text-sm font-semibold">
          {c.title}
        </h2>
        <ConversationStatusPill status={status} />
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted">
        <Badge tone={c.source === "flow" ? "info" : "muted"}>
          {sourceLabel(c.source)}
        </Badge>
        <span>
          {c.runtime}
          {model ? ` · ${model}` : ""}
        </span>
        {c.fork_context && (
          <Badge>
            {c.fork_context === "resumed"
              ? "session resumed"
              : "context replayed"}
          </Badge>
        )}
        {links.map((l) => (
          <Link key={l.to} to={l.to} className={LINK}>
            {l.label}
          </Link>
        ))}
      </div>
      {c.forks.length > 0 && (
        <details className="text-xs">
          <summary className="cursor-pointer select-none text-muted">
            {c.forks.length === 1
              ? "1 chat continues this run"
              : `${c.forks.length} chats continue this run`}
          </summary>
          <ul className="m-0 mt-1 list-none space-y-0.5 p-0">
            {c.forks.map((f) => (
              <li key={f.id} className="flex items-center gap-2">
                <Link
                  to={{ pathname: chatPath(f.id), search }}
                  className={`${LINK} min-w-0 truncate`}
                >
                  {f.title}
                </Link>
                <ConversationStatusPill status={f.status} />
              </li>
            ))}
          </ul>
        </details>
      )}
      {actions && (
        <div className="flex flex-wrap items-center gap-2">{actions}</div>
      )}
    </header>
  );
}

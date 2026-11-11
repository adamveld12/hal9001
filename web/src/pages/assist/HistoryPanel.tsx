import { useAssistSessions } from "../../api/assist";
import { LoadingState } from "../../components/ui";
import { cx } from "../../lib/cx";
import { relativeTime } from "../../lib/format";
import { useAssist } from "./context";
import { recentSessions, sessionRowLabel } from "./history";
import { StatusPill } from "./StatusPill";

/** Recent chats of every kind; picking one shows it in the drawer. */
export function HistoryPanel({ onPicked }: { onPicked: () => void }) {
  const assist = useAssist();
  const sessions = useAssistSessions();
  const rows = recentSessions(sessions.data ?? []);

  return (
    <section
      aria-label="Chat history"
      className="max-h-72 overflow-auto border-b border-line bg-panel"
    >
      {sessions.isPending && <LoadingState label="Loading chats" />}
      {sessions.isError && (
        <div role="alert" className="p-3 text-xs text-bad">
          Could not load the chats: {sessions.error.message}
        </div>
      )}
      {sessions.isSuccess && rows.length === 0 && (
        <div className="p-3 text-xs text-muted">No chats yet.</div>
      )}
      <ul className="m-0 list-none p-1">
        {rows.map((s) => {
          const label = sessionRowLabel(s);
          const current = s.id === assist.sessionId;
          return (
            <li key={s.id}>
              <button
                type="button"
                aria-current={current ? "true" : undefined}
                className={cx(
                  "flex w-full flex-col gap-0.5 rounded-md px-2 py-1.5 text-left text-xs hover:bg-raised",
                  current && "bg-raised",
                )}
                onClick={() => {
                  assist.select(s);
                  onPicked();
                }}
              >
                <span className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate font-medium">
                    {label.title}
                  </span>
                  {(s.status === "running" ||
                    s.status === "awaiting_approval" ||
                    s.status === "failed") && <StatusPill status={s.status} />}
                </span>
                <span className="flex gap-2 text-[11px] text-muted">
                  <span className="truncate">{label.target}</span>
                  <span className="ml-auto shrink-0">
                    {relativeTime(s.updated_at)}
                  </span>
                </span>
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

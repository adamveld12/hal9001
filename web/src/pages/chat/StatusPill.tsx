import type { ConversationStatus } from "../../api/conversations";
import { Badge } from "../../components/ui";
import { statusLabel, statusTone } from "./status";

/** Status of a conversation. A running one shows a spinner and one that waits for an approval a pulsing dot, so live rows stand out. */
export function ConversationStatusPill({
  status,
}: {
  status: ConversationStatus;
}) {
  return (
    <Badge tone={statusTone(status)} className="shrink-0 gap-1.5">
      {status === "running" && (
        <span
          aria-hidden="true"
          className="size-2 animate-spin rounded-full border border-run/40 border-t-run"
        />
      )}
      {status === "awaiting_approval" && (
        <span
          aria-hidden="true"
          className="size-1.5 animate-pulse rounded-full bg-warn"
        />
      )}
      {statusLabel(status)}
    </Badge>
  );
}

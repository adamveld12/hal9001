import { Link } from "react-router";
import type { InboxInteraction } from "../../api/inboxActions";
import { Badge } from "../../components/ui";
import {
  approvalPath,
  statusChip,
  statusDetail,
  type StatusChip,
} from "./interactions";

function Chip({ chip, running }: { chip: StatusChip; running: boolean }) {
  return (
    <Badge tone={chip.tone}>
      {running && (
        <span className="mr-1 size-2.5 animate-spin rounded-full border-2 border-line border-t-info" />
      )}
      {chip.label}
    </Badge>
  );
}

/** What happened with the answers, actions and replies of a note, oldest first. */
export function InteractionStatus({
  interactions,
}: {
  interactions: readonly InboxInteraction[];
}) {
  if (interactions.length === 0) return null;
  return (
    <ul className="m-0 list-none space-y-1.5 p-0 text-xs" aria-live="polite">
      {interactions.map((i) => {
        const approval = approvalPath(i);
        const detail = statusDetail(i);
        return (
          <li key={i.id} className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{i.summary}</span>
            <Chip chip={statusChip(i)} running={i.status === "running"} />
            {approval && (
              <Link to={approval} className="text-accent underline">
                Review approval
              </Link>
            )}
            {detail && (
              <span
                className={
                  i.status === "failed"
                    ? "min-w-0 break-words text-bad"
                    : "min-w-0 break-words text-muted"
                }
              >
                {detail}
              </span>
            )}
          </li>
        );
      })}
    </ul>
  );
}

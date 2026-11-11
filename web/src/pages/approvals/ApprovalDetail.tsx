import { Link } from "react-router";
import type { Approval } from "../../api/approvals";
import { ApprovalCard } from "./ApprovalCard";
import type { DecisionInput } from "./card";

/** The Inbox detail pane for one approval: a back link over the full card. */
export function ApprovalDetail({
  approval,
  steering,
  onSteering,
  editDrafts,
  onEditDraft,
  busy,
  onDecide,
  onStale,
  backTo,
}: {
  approval: Approval;
  steering: string;
  onSteering: (text: string) => void;
  /** Box text of the editable fields, by path (missing = the current value). */
  editDrafts: Readonly<Record<string, string>>;
  onEditDraft: (key: string, text: string) => void;
  busy: boolean;
  onDecide: (decision: DecisionInput) => void;
  /** Called every few seconds while a pending call is past its deadline, to refetch. */
  onStale: () => void;
  backTo: { pathname: string; search: string };
}) {
  return (
    <div>
      <Link
        to={backTo}
        className="block px-4 pt-4 text-xs text-accent underline md:hidden"
      >
        Back to list
      </Link>
      <ApprovalCard
        approval={approval}
        variant="full"
        steering={steering}
        onSteering={onSteering}
        editDrafts={editDrafts}
        onEditDraft={onEditDraft}
        busy={busy}
        onDecide={onDecide}
        onStale={onStale}
        hotkeys
      />
    </div>
  );
}

import { useMemo } from "react";
import {
  useApproval,
  useConversationApprovals,
  type Approval,
} from "../../api/approvals";
import { errorMessage } from "../../api/client";
import { StatusBadge } from "../../components/ui";
import { unlinkedApprovals } from "./card";
import { ConnectedApprovalCard } from "./ConnectedApprovalCard";

/** What is left of a decided approval in a transcript: status, steering, stop. */
function DecisionChip({ a }: { a: Approval }) {
  return (
    <div
      className="flex flex-wrap items-center gap-2 text-xs"
      data-testid="approval-chip"
    >
      <StatusBadge status={a.status} />
      {a.cancel_run && <span className="text-muted">run stopped</span>}
      {a.comment && <span className="text-muted">Steering: {a.comment}</span>}
    </div>
  );
}

/**
 * The approval a transcript message points to (`approval_id`): a compact
 * card while pending, a decision chip afterwards. Shared by the chat drawer
 * and the Chat page.
 */
export function MessageApproval({ approvalId }: { approvalId: string }) {
  const approval = useApproval(approvalId);
  const a = approval.data;
  if (!a) {
    return approval.isError ? (
      <div role="alert" className="text-xs text-bad">
        {errorMessage(approval.error)}
      </div>
    ) : null;
  }
  return a.status === "pending" ? (
    <ConnectedApprovalCard approval={a} variant="compact" />
  ) : (
    <DecisionChip a={a} />
  );
}

/**
 * Pending approvals of one conversation that no message carries (a
 * `request_approval` call, a proposal), as compact cards at the end of the
 * transcript. `linkedIds` are the `approval_id`s of the messages.
 */
export function ConversationTailApprovals({
  conversationId,
  linkedIds,
}: {
  conversationId: string;
  linkedIds: ReadonlySet<string>;
}) {
  const { items } = useConversationApprovals(conversationId);
  const rest = useMemo(
    () => unlinkedApprovals(items, linkedIds),
    [items, linkedIds],
  );
  if (rest.length === 0) return null;
  return (
    <div className="space-y-2" data-pending-approval="">
      {rest.map((a) => (
        <ConnectedApprovalCard key={a.id} approval={a} variant="compact" />
      ))}
    </div>
  );
}

import type { Approval } from "../../api/approvals";
import type { PageContextEntity } from "../../lib/pageContext";
import { trimText } from "../../lib/contextSummary";

/** An approval as the assistant sees it: id, what it asks, where it came from. */
export function approvalEntity(a: Approval): PageContextEntity {
  const label =
    a.request?.title ??
    a.permission_request?.title ??
    (a.type === "proposal" ? "Proposal" : a.type);
  const from = a.flow_id ? `flow ${a.flow_id}` : a.runtime;
  return {
    kind: "approval",
    id: a.id,
    label: trimText(label, 80),
    detail: `${a.type}, ${a.status}, ${from}, created ${a.created_at}`,
  };
}

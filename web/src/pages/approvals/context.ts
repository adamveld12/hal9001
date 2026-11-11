import type { Approval } from "../../api/approvals";
import type { PageContextEntity } from "../../lib/pageContext";
import { originText } from "./card";
import { trimText } from "../../lib/contextSummary";

/** An approval as the assistant sees it: id, what it asks, where it came from. */
export function approvalEntity(a: Approval): PageContextEntity {
  const from = originText(a.origin);
  return {
    kind: "approval",
    id: a.id,
    label: trimText(a.title, 80),
    detail: `${a.type}, ${a.status}, ${from}, created ${a.created_at}`,
  };
}

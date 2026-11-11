import { useCallback, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  isAlreadyDecided,
  useDecideApproval,
  type Approval,
} from "../../api/approvals";
import { errorMessage } from "../../api/client";
import { qk } from "../../api/keys";
import { ApprovalCard } from "./ApprovalCard";
import type { DecisionInput } from "./card";
import { buildEdits, initialDrafts } from "./edits";
import { decisionNotice } from "./queue";

/**
 * An ApprovalCard that keeps its own steering and edit drafts and sends the
 * decision itself (Approvals page, chat). A decision updates every cached
 * approval list, so the Inbox and other cards follow at once.
 */
export function ConnectedApprovalCard({
  approval: a,
  variant,
}: {
  approval: Approval;
  variant: "full" | "compact";
}) {
  const qc = useQueryClient();
  const decide = useDecideApproval();
  const [steering, setSteering] = useState("");
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [notice, setNotice] = useState<string | null>(null);

  const onStale = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["approvals"] });
    void qc.invalidateQueries({ queryKey: qk("approval", a.id) });
  }, [qc, a.id]);

  const onDecide = (d: DecisionInput) => {
    if (decide.isPending) return;
    let edits: Record<string, unknown> | undefined;
    if (d.action === "approve" && a.editable.length > 0) {
      const built = buildEdits(a.editable, {
        ...initialDrafts(a.editable),
        ...drafts,
      });
      const bad = Object.keys(built.errors);
      if (bad.length > 0) {
        setNotice(`Fix the edit of ${bad.join(", ")}: it is not valid JSON.`);
        return;
      }
      edits = built.edits;
    }
    setNotice(null);
    decide.mutate(
      { id: a.id, ...d, edits },
      {
        onSuccess: (done) => {
          setNotice(decisionNotice(done));
          setSteering("");
          setDrafts({});
        },
      },
    );
  };

  const message = decide.isError
    ? {
        tone: "bad" as const,
        text: isAlreadyDecided(decide.error)
          ? "Already decided elsewhere."
          : errorMessage(decide.error),
      }
    : notice
      ? { tone: "info" as const, text: notice }
      : null;

  return (
    <ApprovalCard
      approval={a}
      variant={variant}
      steering={steering}
      onSteering={setSteering}
      editDrafts={drafts}
      onEditDraft={(k, t) => setDrafts((d) => ({ ...d, [k]: t }))}
      busy={decide.isPending}
      onDecide={onDecide}
      onStale={onStale}
      message={message}
    />
  );
}

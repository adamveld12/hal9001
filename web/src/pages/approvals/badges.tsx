import type { Approval } from "../../api/approvals";
import { Badge } from "../../components/ui";

/** `gate` marks a request that asks whether a node may run (`requires_approval`). */
export function TypeBadge({
  type,
  gate = false,
}: {
  type: Approval["type"];
  gate?: boolean;
}) {
  if (gate) return <Badge tone="warn">node gate</Badge>;
  return (
    <Badge tone={type === "tool_call" ? "run" : "info"}>
      {type === "tool_call"
        ? "tool call"
        : type === "request"
          ? "request"
          : "proposal"}
    </Badge>
  );
}

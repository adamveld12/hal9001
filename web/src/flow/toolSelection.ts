import type { ToolApprove } from "../api/tools";

/** An entry of an agent node's `tools`: a tool id, or an id with an approval override. */
export type NodeToolRef = string | { id: string; approve?: ToolApprove };

/** hal's own tools, always available to an agent. */
export const BUILTIN_TOOLS = [
  "gh_api",
  "pr_view",
  "pr_diff",
  "pr_list",
  "issue_view",
  "search",
  "now",
  "run_context",
  "node_input",
  "event",
] as const;

/** The approval choice shown for a selected tool: the tool's own setting, or an override. */
export type ApproveChoice = "default" | "never" | "always" | "custom";

export function refId(r: NodeToolRef): string {
  return typeof r === "string" ? r : r.id;
}

export function isSelected(refs: NodeToolRef[] | undefined, id: string) {
  return (refs ?? []).some((r) => refId(r) === id);
}

/** Add or remove a tool, keeping the other entries (and their overrides) as they are. Returns undefined when empty. */
export function toggleTool(
  refs: NodeToolRef[] | undefined,
  id: string,
  on: boolean,
): NodeToolRef[] | undefined {
  const rest = (refs ?? []).filter((r) => refId(r) !== id);
  const next = on ? [...rest, id] : rest;
  return next.length > 0 ? next : undefined;
}

export function approveChoice(
  refs: NodeToolRef[] | undefined,
  id: string,
): ApproveChoice {
  const ref = (refs ?? []).find((r) => refId(r) === id);
  if (ref === undefined || typeof ref === "string" || !ref.approve)
    return "default";
  return ref.approve === "never" || ref.approve === "always"
    ? ref.approve
    : "custom";
}

/** Set the approval override of a selected tool; "default" drops it (plain id). "custom" keeps what is there. */
export function setApproveChoice(
  refs: NodeToolRef[] | undefined,
  id: string,
  choice: ApproveChoice,
): NodeToolRef[] | undefined {
  if (choice === "custom") return refs;
  return (refs ?? []).map((r) => {
    if (refId(r) !== id) return r;
    return choice === "default" ? id : { id, approve: choice };
  });
}

/** Ids listed on the node that no tool in `available` defines. */
export function unknownTools(
  refs: NodeToolRef[] | undefined,
  available: string[],
): string[] {
  const known = new Set(available);
  return (refs ?? []).map(refId).filter((id) => !known.has(id));
}

/**
 * The reason text of a tool-call approval. A call to a tool that starts
 * something (an emit tool) puts the flow, node and port, the next nodes and the
 * payload in it over several lines, so it is shown as is, in a scrollable
 * monospace block. Nothing renders for an empty reason.
 */
export function ApprovalReason({
  reason,
}: {
  reason: string | null | undefined;
}) {
  const text = reason?.trimEnd();
  if (!text?.trim()) return null;
  return (
    <pre
      tabIndex={0}
      aria-label="Approval details"
      className="m-0 max-h-60 overflow-auto whitespace-pre-wrap break-words rounded-md border border-line bg-raised p-2 font-mono text-[11px]"
    >
      {text}
    </pre>
  );
}

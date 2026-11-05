import { Badge } from "../../components/ui";
import type { Tone } from "../../lib/status";
import {
  groupDiagnostics,
  sourceLine,
  type Diagnostic,
  type Severity,
} from "./diagnostics";

const TONE: Record<Severity, Tone> = {
  error: "bad",
  warning: "warn",
  info: "info",
};

function summary(counts: Record<Severity, number>): string {
  const parts: string[] = [];
  if (counts.error)
    parts.push(`${counts.error} error${counts.error > 1 ? "s" : ""}`);
  if (counts.warning) {
    parts.push(`${counts.warning} warning${counts.warning > 1 ? "s" : ""}`);
  }
  if (counts.info)
    parts.push(`${counts.info} note${counts.info > 1 ? "s" : ""}`);
  return parts.join(", ");
}

/**
 * Findings grouped by line. With `source` (the edited text) each located
 * finding shows the line it points at.
 */
export function DiagnosticsList({
  diagnostics,
  source,
  stale,
  emptyText,
}: {
  diagnostics: readonly Diagnostic[];
  source?: string;
  /** The findings describe earlier text than the editor holds. */
  stale?: boolean;
  /** Shown when there are no findings; the list renders nothing when omitted. */
  emptyText?: string;
}) {
  if (diagnostics.length === 0) {
    return emptyText ? (
      <div className="text-xs text-ok">{emptyText}</div>
    ) : null;
  }
  const { groups, counts } = groupDiagnostics(diagnostics);
  return (
    <div className="space-y-2" role="list" aria-label="Diagnostics">
      <div className="flex items-center gap-2 text-xs">
        <span className="font-medium">{summary(counts)}</span>
        {stale && <Badge tone="warn">outdated: edited since</Badge>}
      </div>
      {groups.map((g) => {
        const text =
          g.line !== null && source !== undefined
            ? sourceLine(source, g.line)
            : null;
        return (
          <div
            key={g.line ?? "none"}
            role="listitem"
            className="rounded-md border border-line bg-panel"
          >
            <div className="flex items-center gap-2 border-b border-line px-2 py-1 text-[11px] text-muted">
              {g.line === null ? "No line" : `Line ${g.line}`}
              {text !== null && (
                <code className="min-w-0 truncate font-mono text-fg">
                  {text.trim() === "" ? "(blank)" : text.trim()}
                </code>
              )}
            </div>
            <ul className="divide-y divide-line">
              {g.items.map((d, i) => (
                <li
                  key={`${d.code}-${d.path ?? ""}-${i}`}
                  className="flex items-start gap-2 px-2 py-1.5 text-xs"
                >
                  <Badge tone={TONE[d.severity]}>{d.severity}</Badge>
                  <div className="min-w-0">
                    <div className="break-words">{d.message}</div>
                    <div className="flex flex-wrap gap-x-2 font-mono text-[11px] text-muted">
                      <span>{d.code}</span>
                      {d.path && <span>{d.path}</span>}
                      {d.node_id && <span>node {d.node_id}</span>}
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          </div>
        );
      })}
    </div>
  );
}

import { useMemo } from "react";
import { cx } from "../../lib/cx";
import { collapseContext, lineDiff } from "./diff";

const LINE_STYLE = {
  add: "bg-ok/10 text-ok",
  del: "bg-bad/10 text-bad",
  eq: "text-muted",
} as const;

const MARK = { add: "+", del: "-", eq: " " } as const;

/** Unified-style line diff of one file edit; unchanged stretches are folded. */
export function DiffView({
  path,
  oldText,
  newText,
}: {
  path: string;
  oldText: string | null;
  newText: string;
}) {
  const lines = useMemo(
    () => collapseContext(lineDiff(oldText, newText)),
    [oldText, newText],
  );
  return (
    <div className="overflow-hidden rounded-md border border-line">
      <div className="border-b border-line bg-raised px-2 py-1 font-mono text-[11px]">
        {path}
        {oldText === null && <span className="ml-2 text-muted">new file</span>}
      </div>
      <pre className="m-0 max-h-96 overflow-auto p-0 font-mono text-[11px] leading-4">
        {lines.map((l, i) =>
          l.type === "gap" ? (
            <div key={i} className="bg-raised px-2 text-muted">
              ⋯ {l.count} unchanged lines
            </div>
          ) : (
            <div
              key={i}
              className={cx("whitespace-pre px-2", LINE_STYLE[l.type])}
            >
              {MARK[l.type]} {l.text}
            </div>
          ),
        )}
      </pre>
    </div>
  );
}

import { cx } from "../../lib/cx";
import { collapseContext, type DiffKind, type FileDiff } from "./diff";

const LINE: Record<DiffKind, string> = {
  add: "bg-ok/10 text-ok",
  del: "bg-bad/10 text-bad",
  ctx: "text-muted",
  hunk: "bg-info/10 text-info",
  skip: "bg-raised text-muted italic",
};

const SIGN: Record<DiffKind, string> = {
  add: "+",
  del: "-",
  ctx: " ",
  hunk: " ",
  skip: " ",
};

/** A unified-style view of one file's changes. */
export function DiffView({ diff }: { diff: FileDiff }) {
  const lines = collapseContext(diff.lines);
  return (
    <figure className="m-0 overflow-hidden rounded-md border border-line">
      {diff.path && (
        <figcaption className="border-b border-line bg-raised px-2 py-1 font-mono text-[11px]">
          {diff.path}
        </figcaption>
      )}
      <pre className="m-0 max-h-[28rem] overflow-auto font-mono text-[11px] leading-[1.45]">
        {lines.length === 0 ? (
          <span className="block px-2 py-1 text-muted">No changes.</span>
        ) : (
          lines.map((l, i) => (
            <span
              key={i}
              className={cx("block whitespace-pre px-2", LINE[l.kind])}
            >
              {l.kind === "skip" ? `… ${l.text}` : `${SIGN[l.kind]} ${l.text}`}
            </span>
          ))
        )}
      </pre>
    </figure>
  );
}

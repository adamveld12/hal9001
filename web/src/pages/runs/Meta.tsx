import type { ReactNode } from "react";
import { Link } from "react-router";

/** Label/value rows for run metadata; rows with a null value are omitted. */
export function Meta({
  rows,
}: {
  rows: readonly { label: string; value: ReactNode | null }[];
}) {
  return (
    <dl className="grid grid-cols-[7.5rem_1fr] gap-x-3 gap-y-1 text-xs">
      {rows
        .filter((r) => r.value !== null && r.value !== undefined)
        .map((r) => (
          <div key={r.label} className="contents">
            <dt className="text-muted">{r.label}</dt>
            <dd className="m-0 min-w-0 break-words">{r.value}</dd>
          </div>
        ))}
    </dl>
  );
}

export function TextLink({
  to,
  children,
}: {
  to: string;
  children: ReactNode;
}) {
  return (
    <Link to={to} className="text-accent hover:underline">
      {children}
    </Link>
  );
}

export function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono text-[11px]">{children}</span>;
}

export function SectionTitle({ children }: { children: ReactNode }) {
  return (
    <h3 className="m-0 mb-1.5 text-[11px] font-semibold uppercase tracking-wide text-muted">
      {children}
    </h3>
  );
}

import { relativeTime, formatTime } from "../lib/format";

/** Relative time with the absolute time as tooltip. */
export function Time({ ts }: { ts: string | null | undefined }) {
  if (!ts) return <span className="text-muted">–</span>;
  return (
    <time
      dateTime={ts}
      title={formatTime(ts)}
      className="whitespace-nowrap text-muted"
    >
      {relativeTime(ts)}
    </time>
  );
}

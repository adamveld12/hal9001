/** Absolute local time, e.g. "Nov 5, 14:03:22". */
export function formatTime(ts: string | null | undefined): string {
  if (!ts) return "–";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return ts;
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

/** "5s ago" / "3m ago" / "2h ago" / "4d ago"; "in 3m" for future times. */
export function relativeTime(
  ts: string | null | undefined,
  now: number = Date.now(),
): string {
  if (!ts) return "–";
  const t = new Date(ts).getTime();
  if (Number.isNaN(t)) return ts;
  const diff = Math.round((now - t) / 1000);
  const abs = Math.abs(diff);
  let text: string;
  if (abs < 5) return "just now";
  if (abs < 60) text = `${abs}s`;
  else if (abs < 3600) text = `${Math.floor(abs / 60)}m`;
  else if (abs < 86_400) text = `${Math.floor(abs / 3600)}h`;
  else text = `${Math.floor(abs / 86_400)}d`;
  return diff >= 0 ? `${text} ago` : `in ${text}`;
}

/** Milliseconds as "850ms", "12.4s", "3m 05s", "2h 10m". */
export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return "–";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(Math.round(s % 60)).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${String(m % 60).padStart(2, "0")}m`;
}

/** Duration between two timestamps; `end` defaults to now (for running work). */
export function durationBetween(
  start: string | null | undefined,
  end: string | null | undefined,
  now: number = Date.now(),
): string {
  if (!start) return "–";
  const s = new Date(start).getTime();
  const e = end ? new Date(end).getTime() : now;
  return formatDuration(e - s);
}

/** Bytes as "512 B", "4.0 KiB", "1.2 MiB". */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / 1024 / 1024).toFixed(1)} MiB`;
}

/** Last 8 characters of a ULID: the random part, so it differs between nearby ids. */
export function shortId(id: string): string {
  return id.length > 8 ? id.slice(-8) : id;
}

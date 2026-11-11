import type { AssistStatus } from "../../api/assist";
import { Badge } from "../../components/ui";
import type { Tone } from "../../lib/status";

const STATUS: Record<AssistStatus, { label: string; tone: Tone }> = {
  idle: { label: "idle", tone: "muted" },
  running: { label: "running", tone: "run" },
  awaiting_approval: { label: "awaiting approval", tone: "warn" },
  failed: { label: "failed", tone: "bad" },
  closed: { label: "ended", tone: "muted" },
};

export function StatusPill({ status }: { status: AssistStatus }) {
  const s = STATUS[status];
  return (
    <Badge tone={s.tone} className="gap-1.5">
      {status === "running" && (
        <span className="size-2 animate-spin rounded-full border border-run/40 border-t-run" />
      )}
      {s.label}
    </Badge>
  );
}

export type Tone = "ok" | "warn" | "bad" | "info" | "run" | "muted";

/** Colour tone for any status/decision enum string used by the API. */
export function statusTone(status: string | undefined | null): Tone {
  switch (status) {
    case "succeeded":
    case "done":
    case "emitted":
    case "approved":
    case "allow":
    case "success":
      return "ok";
    case "failed":
    case "error":
    case "denied":
    case "reject":
    case "timeout":
    case "killed":
    case "fail":
      return "bad";
    case "awaiting_approval":
    case "pending":
    case "interrupted":
    case "expired":
    case "challenge":
      return "warn";
    case "running":
      return "run";
    case "queued":
      return "info";
    case "cancelled":
      return "muted";
    default:
      return "muted";
  }
}

export const TONE_TEXT: Record<Tone, string> = {
  ok: "text-ok",
  warn: "text-warn",
  bad: "text-bad",
  info: "text-info",
  run: "text-run",
  muted: "text-muted",
};

export const TONE_BADGE: Record<Tone, string> = {
  ok: "text-ok bg-ok/10 border-ok/30",
  warn: "text-warn bg-warn/10 border-warn/30",
  bad: "text-bad bg-bad/10 border-bad/30",
  info: "text-info bg-info/10 border-info/30",
  run: "text-run bg-run/10 border-run/30",
  muted: "text-muted bg-raised border-line",
};

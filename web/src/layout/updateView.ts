import { ApiError } from "../api/client";
import type { UpdateStatus } from "../api/update";

export type BannerView =
  | { kind: "hidden" }
  | { kind: "available"; version: string; notesUrl: string; hint: boolean }
  | { kind: "pending"; version: string; running: number }
  | { kind: "installing"; phase: string }
  | { kind: "error"; message: string };

const PHASE_LABEL: Record<string, string> = {
  requested: "starting",
  downloading: "downloading",
  verifying: "verifying",
  installing: "installing",
};

/** What the app-wide banner shows. `dismissed` is the version the user closed it for. */
export function bannerView(
  s: UpdateStatus | undefined,
  dismissed: string | null,
): BannerView {
  if (!s || !s.enabled) return { kind: "hidden" };
  const version = s.latest?.version ?? "";
  if (s.state === "installing") {
    return {
      kind: "installing",
      phase: PHASE_LABEL[s.phase ?? "requested"] ?? "starting",
    };
  }
  if (s.state === "error" && s.error) {
    return { kind: "error", message: s.error };
  }
  if (s.state === "pending" || s.pending) {
    if (dismissed === version) return { kind: "hidden" };
    return { kind: "pending", version, running: s.busy.running_runs };
  }
  if (s.available && s.state === "available") {
    if (dismissed === version) return { kind: "hidden" };
    return {
      kind: "available",
      version,
      notesUrl: s.latest?.notes_url ?? "",
      hint: !s.installer_connected,
    };
  }
  return { kind: "hidden" };
}

/** The 409 problem's `release_url`, when the install was refused for lack of an installer. */
export function refusal(
  err: unknown,
): { detail: string; releaseUrl: string } | null {
  if (!(err instanceof ApiError) || err.status !== 409) return null;
  const p = err.problem as Record<string, unknown> | undefined;
  if (p?.current_state !== "no_installer") return null;
  return {
    detail: err.message,
    releaseUrl: typeof p.release_url === "string" ? p.release_url : "",
  };
}

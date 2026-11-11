import type { Schemas } from "../api/client";
import type { Tone } from "./status";

export type RuntimeDetection = Schemas["RuntimeDetection"];
export type RuntimeWarning = Schemas["RuntimeWarning"];
export type RuntimeStatus = RuntimeDetection["status"];
export type AdapterProgress = Schemas["RuntimeAdapterProgress"];

/** Badge text of a runtime's detection status. */
export function statusLabel(status: RuntimeStatus): string {
  switch (status) {
    case "ready":
      return "Configured";
    case "warning":
      return "Needs attention";
    case "unavailable":
      return "Not available";
  }
}

export function statusTone(status: RuntimeStatus): Tone {
  switch (status) {
    case "ready":
      return "ok";
    case "warning":
      return "warn";
    case "unavailable":
      return "bad";
  }
}

/** Most urgent first: what blocks the runtime, then what to sign in to, then the hints. */
const WARNING_ORDER = [
  "cli_missing",
  "node_missing",
  "node_old",
  "adapter_install_failed",
  "adapter_missing",
  "auth_missing",
  "providers_missing",
  "auth_unknown",
];

/** Warnings sorted by urgency; unknown codes go last, ties keep the server's order. */
export function orderWarnings(
  warnings: readonly RuntimeWarning[],
): RuntimeWarning[] {
  const rank = (w: RuntimeWarning) => {
    const i = WARNING_ORDER.indexOf(w.code);
    return i === -1 ? WARNING_ORDER.length : i;
  };
  return warnings
    .map((w, i) => ({ w, i }))
    .sort((a, b) => rank(a.w) - rank(b.w) || a.i - b.i)
    .map(({ w }) => w);
}

/** Short reason shown in dropdowns next to a runtime that is not ready. */
export function statusHint(detection: RuntimeDetection): string | null {
  switch (detection.status) {
    case "ready":
      return null;
    case "unavailable":
      return "Not set up";
    case "warning": {
      const codes = detection.warnings.map((w) => w.code);
      if (codes.includes("auth_missing")) return "Sign-in needed";
      if (codes.includes("providers_missing")) return "No providers";
      return "Needs attention";
    }
  }
}

/** Dropdown option text: "codex (Not set up)", "claude (Sign-in needed)", plain name when ready. */
export function runtimeLabel(
  name: string,
  detection: RuntimeDetection | undefined,
): string {
  const hint = detection ? statusHint(detection) : null;
  return hint ? `${name} (${hint})` : name;
}

export type RuntimeOption = {
  name: string;
  label: string;
  status: RuntimeStatus | undefined;
};

export function runtimeOption(r: {
  name: string;
  detection?: RuntimeDetection;
}): RuntimeOption {
  return {
    name: r.name,
    label: runtimeLabel(r.name, r.detection),
    status: r.detection?.status,
  };
}

/**
 * Options for a runtime dropdown. A current value that is not a configured
 * runtime (a hand-edited file) stays selectable and is listed first.
 */
export function runtimeOptions(
  options: readonly RuntimeOption[],
  current: string,
): RuntimeOption[] {
  if (current === "" || options.some((o) => o.name === current))
    return [...options];
  return [
    { name: current, label: `${current} (unknown)`, status: undefined },
    ...options,
  ];
}

// ---------------------------------------------------------------- adapter install

/** Last lines kept per runtime for the live install log. */
export const PROGRESS_LINES = 5;

export type ProgressState = {
  phase: AdapterProgress["phase"];
  lines: string[];
  error?: string;
};

export type ProgressByRuntime = Record<string, ProgressState>;

/** Folds one `runtime.adapter` event into the per-runtime progress. A new `installing` after a finish starts a fresh log. */
export function reduceProgress(
  state: ProgressByRuntime,
  e: AdapterProgress,
): ProgressByRuntime {
  const prev = state[e.name];
  const fresh = !prev || prev.phase !== "installing";
  const lines = fresh ? [] : [...prev.lines];
  if (e.line) lines.push(e.line);
  const next: ProgressState = {
    phase: e.phase,
    lines: lines.slice(-PROGRESS_LINES),
  };
  if (e.error) next.error = e.error;
  return { ...state, [e.name]: next };
}

export type AdapterAction =
  | { kind: "none" }
  | { kind: "install"; label: string }
  | { kind: "install_disabled"; reason: string }
  | { kind: "installing"; lines: string[] }
  | { kind: "remove" };

/** Why the adapter cannot be installed, from the Node check; null when Node is fine. */
export function nodeBlocker(node: RuntimeDetection["node"]): string | null {
  if (!node.found) return "Node 22 or newer is required and was not found.";
  if (!node.ok)
    return `Node ${node.version ?? "(unknown version)"} is too old; Node 22 or newer is required.`;
  return null;
}

/**
 * Which adapter button a runtime card shows:
 * installing (live log) > remove (managed and installed) > install (not
 * installed and installable; disabled with a reason without Node 22+) > none.
 * After a failed run the install button reads "Retry install".
 */
export function adapterAction(
  d: RuntimeDetection,
  progress: ProgressState | undefined,
): AdapterAction {
  const a = d.adapter;
  if (a.installing || progress?.phase === "installing") {
    return { kind: "installing", lines: progress?.lines ?? [] };
  }
  if (a.source === "managed" && a.installed) return { kind: "remove" };
  if (!a.installable || a.installed) return { kind: "none" };
  const blocker = nodeBlocker(d.node);
  if (blocker) return { kind: "install_disabled", reason: blocker };
  const failed =
    progress?.phase === "failed" ||
    d.warnings.some((w) => w.code === "adapter_install_failed");
  return {
    kind: "install",
    label: failed ? "Retry install" : "Install adapter",
  };
}

/** Text of the "Adapter" row. */
export function adapterSummary(a: RuntimeDetection["adapter"]): string {
  const v = a.version ? ` · v${a.version.replace(/^v/, "")}` : "";
  switch (a.source) {
    case "builtin":
      return "Built in";
    case "managed":
      return a.installed ? `Installed by hal${v}` : "Not installed";
    case "custom":
      return "Custom command";
    case "none":
      return "Not installed";
  }
}

export function authSummary(auth: RuntimeDetection["auth"]): {
  text: string;
  tone: Tone;
} {
  switch (auth.status) {
    case "ok":
      return { text: "Signed in", tone: "ok" };
    case "missing":
      return { text: "Not signed in", tone: "warn" };
    case "unknown":
      return { text: "Unknown", tone: "muted" };
  }
}

export function nodeSummary(node: RuntimeDetection["node"]): {
  text: string;
  tone: Tone;
} {
  if (!node.found) return { text: "Not found", tone: "bad" };
  if (!node.ok)
    return { text: `${node.version ?? "?"} (too old)`, tone: "warn" };
  return { text: node.version ?? "Found", tone: "ok" };
}

// Pure helpers behind the live preview: which steps to send, how to present
// the result, how to label samples, and the "Convert to code node" mapping.

import { relativeTime, shortId } from "../../lib/format";
import type { CommandNode } from "../model";
import { transformPorts } from "./ports";
import type {
  FlowSample,
  TransformNode,
  TransformPreview,
  TransformStep,
} from "./types";
import { validateStep } from "./validateSteps";

/**
 * The steps before the first one the client already knows is incomplete:
 * previewing those still shows something useful while a later step is being
 * filled in. `blockedAt` is the index of the incomplete step, if any.
 */
export function previewableSteps(steps: readonly TransformStep[]): {
  steps: TransformStep[];
  blockedAt: number | null;
} {
  for (let i = 0; i < steps.length; i++) {
    if (validateStep(steps[i], i, steps).some((d) => d.severity === "error")) {
      return { steps: steps.slice(0, i), blockedAt: i };
    }
  }
  return { steps: [...steps], blockedAt: null };
}

export interface PortOutputs {
  port: string;
  payloads: unknown[];
}

/** Outputs grouped by port: the node's ports in order first, then any other port. */
export function groupOutputs(
  outputs: TransformPreview["outputs"],
  portOrder: readonly string[],
): PortOutputs[] {
  const byPort = new Map<string, unknown[]>();
  for (const o of outputs) {
    const list = byPort.get(o.port) ?? [];
    list.push(o.payload);
    byPort.set(o.port, list);
  }
  const ordered = [
    ...portOrder.filter((p) => byPort.has(p)),
    ...[...byPort.keys()].filter((p) => !portOrder.includes(p)),
  ];
  return ordered.map((port) => ({
    port,
    payloads: byPort.get(port) ?? [],
  }));
}

/** Item counts along the steps: the input (1) then what each step leaves. */
export function countTrail(
  results: TransformPreview["step_results"],
): number[] {
  return [1, ...results.map((r) => r.outputs_count)];
}

/** Compact one-line JSON for sample labels. */
export function snippet(value: unknown, max = 60): string {
  const text = JSON.stringify(value) ?? "";
  return text.length > max ? `${text.slice(0, max)}…` : text;
}

/** Where a sample came from, when it happened and what it starts with. */
export function sampleLabel(s: FlowSample, now: number = Date.now()): string {
  const from =
    s.source === "event"
      ? `Event ${s.event_id ? shortId(s.event_id) : ""}${s.sensor_id ? ` (${s.sensor_id})` : ""}`
      : `${s.from_node_id ?? "Upstream"} output, run ${s.node_run_id ? shortId(s.node_run_id) : ""}`;
  return `${from.trim()} · ${relativeTime(s.created_at, now)} · ${snippet(s.payload)}`;
}

export type ParsedInput =
  { ok: true; value: unknown } | { ok: false; error: string };

/** Pasted JSON. Empty text is not an input yet, so it has no error to show. */
export function parseInput(text: string): ParsedInput | null {
  if (text.trim() === "") return null;
  try {
    return { ok: true, value: JSON.parse(text) };
  } catch (e) {
    return {
      ok: false,
      error: e instanceof Error ? e.message : "Not valid JSON",
    };
  }
}

/**
 * The jq command node a transform becomes: same id, position and edges. The
 * declared ports are the transform's ports (without the implicit `error`), left
 * out when `out` is the only one.
 */
export function toCommandNode(node: TransformNode, jq: string): CommandNode {
  const ports = transformPorts(node.steps);
  const out: CommandNode = {
    id: node.id,
    type: "command",
    lang: "jq",
    script: jq,
  };
  if (!(ports.length === 1 && ports[0] === "out")) out.ports = ports;
  if (node.timeout !== undefined) out.timeout = node.timeout;
  if (node.ui) out.ui = node.ui;
  return out;
}

/** Stable ids for samples, so a selection survives a refetch that adds newer ones. */
export function sampleKeys(samples: readonly FlowSample[]): string[] {
  const seen = new Map<string, number>();
  return samples.map((s, i) => {
    const base = s.event_id ?? s.node_run_id ?? `sample-${i}`;
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    return `${base}#${n}`;
  });
}

/**
 * How many leading steps an earlier preview answer still describes: steps up
 * to the first one that was edited, and none when the input changed. Counts
 * and diagnostics beyond that point are stale.
 */
export function freshStepCount(
  answered: { steps: readonly TransformStep[]; input: unknown } | undefined,
  steps: readonly TransformStep[],
  input: unknown,
): number {
  if (!answered || answered.input !== input) return 0;
  let n = 0;
  while (
    n < answered.steps.length &&
    n < steps.length &&
    JSON.stringify(answered.steps[n]) === JSON.stringify(steps[n])
  ) {
    n++;
  }
  return n;
}

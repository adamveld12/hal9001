// What the sensor, flow and tool pages tell the assistant about themselves
// (see lib/pageContext.ts). Names, ids, counts and statuses only: never
// environment values, secrets or script bodies. Pure, so the shape is tested.

import type { PageContextInput } from "../../lib/pageContext";

/** Rows of a list that are named to the assistant. */
export const MAX_LISTED = 20;
/** Validation messages that are quoted. */
export const MAX_MESSAGES = 5;

interface Finding {
  severity: string;
  message: string;
}

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

function errorsFirst(findings: readonly Finding[]): Finding[] {
  return [
    ...findings.filter((d) => d.severity === "error"),
    ...findings.filter((d) => d.severity !== "error"),
  ];
}

/** Up to MAX_MESSAGES findings, errors first, as facts. */
function findingFacts(
  findings: readonly Finding[],
  label: string,
): { label: string; value: string }[] {
  return errorsFirst(findings)
    .slice(0, MAX_MESSAGES)
    .map((d) => ({ label, value: `${d.severity}: ${d.message}` }));
}

export interface SensorRow {
  id: string;
  sensor: { description?: string } | null;
  status: { state: string; paused: boolean };
}

export function sensorsListContext(
  items: readonly SensorRow[],
): PageContextInput {
  const count = (state: string) =>
    items.filter((s) => s.status.state === state).length;
  return {
    title: "Sensors",
    summary: `The list of ${plural(items.length, "sensor")}.`,
    facts: [
      { label: "sensors", value: String(items.length) },
      { label: "failing", value: String(count("failing")) },
      { label: "invalid", value: String(count("invalid")) },
      {
        label: "paused",
        value: String(items.filter((s) => s.status.paused).length),
      },
    ],
    entities: items.slice(0, MAX_LISTED).map((s) => ({
      kind: "sensor",
      id: s.id,
      label: s.sensor?.description,
      detail: s.status.paused ? `${s.status.state}, paused` : s.status.state,
    })),
  };
}

export function sensorContext(p: {
  id: string;
  description?: string;
  state: string;
  paused: boolean;
  dirty: boolean;
  /** Findings of the draft shown in the editor (the agent's, or the last validation). */
  diagnostics: readonly Finding[];
  /** A chat is editing this sensor's draft. */
  drafting: boolean;
}): PageContextInput {
  return {
    title: `Sensor ${p.id}`,
    summary: "The editor of one sensor.",
    facts: [
      { label: "sensor", value: p.id },
      { label: "status", value: p.state },
      { label: "paused", value: p.paused ? "yes" : "no" },
      { label: "unsaved changes", value: p.dirty ? "yes" : "no" },
      { label: "AI draft open", value: p.drafting ? "yes" : "no" },
      { label: "problems", value: String(p.diagnostics.length) },
      ...findingFacts(p.diagnostics, "problem"),
    ],
    entities: [{ kind: "sensor", id: p.id, label: p.description }],
  };
}

export function newDraftContext(
  noun: "sensor" | "flow" | "tool",
  p: { dirty: boolean; diagnostics: readonly Finding[] },
): PageContextInput {
  return {
    title: `New ${noun} (draft, not saved)`,
    summary: `The editor of a ${noun} that does not exist yet.`,
    facts: [
      { label: "unsaved changes", value: p.dirty ? "yes" : "no" },
      { label: "problems", value: String(p.diagnostics.length) },
      ...findingFacts(p.diagnostics, "problem"),
    ],
  };
}

export interface FlowRow {
  id: string;
  description?: string;
  enabled: boolean | null;
  dry_run: boolean | null;
  node_count: number;
}

export function flowsListContext(
  items: readonly FlowRow[],
  view: "flows" | "runs" | "queue",
  queueTotal: number | undefined,
): PageContextInput {
  return {
    title:
      view === "queue"
        ? "Flows: queue"
        : view === "runs"
          ? "Flows: runs"
          : "Flows",
    summary: `The ${view} tab of the flows page; ${plural(items.length, "flow")} exist.`,
    facts: [
      { label: "tab", value: view },
      { label: "flows", value: String(items.length) },
      {
        label: "enabled",
        value: String(items.filter((f) => f.enabled === true).length),
      },
      ...(queueTotal === undefined
        ? []
        : [{ label: "queued runs", value: String(queueTotal) }]),
    ],
    entities: items.slice(0, MAX_LISTED).map((f) => ({
      kind: "flow",
      id: f.id,
      label: f.description,
      detail:
        f.enabled === null
          ? "unparseable"
          : `${f.enabled ? "enabled" : "disabled"}${f.dry_run ? ", dry run" : ""}, ${plural(f.node_count, "node")}`,
    })),
  };
}

export function flowContext(p: {
  id: string;
  description?: string;
  enabled: boolean;
  /** Omitted while the canvas shows a draft the page does not parse itself. */
  nodeCount?: number;
  view: string;
  selected: { id: string; type: string } | null;
  diagnostics: readonly Finding[];
  dirty: boolean;
  /** The canvas shows an AI draft instead of the saved flow. */
  drafting: boolean;
}): PageContextInput {
  return {
    title: `Flow ${p.id}`,
    summary: p.drafting
      ? "The canvas of one flow, showing an AI draft."
      : "The editor of one flow.",
    facts: [
      { label: "flow", value: p.id },
      ...(p.description
        ? [{ label: "description", value: p.description }]
        : []),
      { label: "enabled", value: p.enabled ? "yes" : "no" },
      ...(p.nodeCount === undefined
        ? []
        : [{ label: "nodes", value: String(p.nodeCount) }]),
      { label: "tab", value: p.view },
      { label: "unsaved changes", value: p.dirty ? "yes" : "no" },
      { label: "AI draft open", value: p.drafting ? "yes" : "no" },
      ...(p.selected
        ? [
            {
              label: "selected node",
              value: `${p.selected.id} (${p.selected.type})`,
            },
          ]
        : []),
      { label: "problems", value: String(p.diagnostics.length) },
      ...findingFacts(p.diagnostics, "problem"),
    ],
    entities: [
      { kind: "flow", id: p.id },
      ...(p.selected
        ? [{ kind: "node", id: p.selected.id, detail: p.selected.type }]
        : []),
    ],
  };
}

export interface ToolRow {
  id: string;
  tool: { description: string; approve?: unknown } | null;
  diagnostics: readonly Finding[];
}

/** `never`, `rules` (an approve block) or `always`, as the tools list shows it. */
export function approvalLabel(approve: unknown): "never" | "rules" | "always" {
  if (approve === "never") return "never";
  return typeof approve === "object" && approve !== null ? "rules" : "always";
}

export function toolsListContext(items: readonly ToolRow[]): PageContextInput {
  const invalid = items.filter((t) =>
    t.diagnostics.some((d) => d.severity === "error"),
  ).length;
  return {
    title: "Tools",
    summary: `The list of ${plural(items.length, "tool")}.`,
    facts: [
      { label: "tools", value: String(items.length) },
      { label: "invalid", value: String(invalid) },
    ],
    entities: items.slice(0, MAX_LISTED).map((t) => ({
      kind: "tool",
      id: t.id,
      label: t.tool?.description,
      detail: t.tool
        ? `approval ${approvalLabel(t.tool.approve)}`
        : "unparseable",
    })),
  };
}

export function toolContext(p: {
  id: string;
  description?: string;
  paramNames: readonly string[];
  approve: unknown;
  dirty: boolean;
  diagnostics: readonly Finding[];
  drafting: boolean;
}): PageContextInput {
  return {
    title: `Tool ${p.id}`,
    summary: "The editor of one tool.",
    facts: [
      { label: "tool", value: p.id },
      ...(p.description
        ? [{ label: "description", value: p.description }]
        : []),
      {
        label: "parameters",
        value: p.paramNames.length === 0 ? "none" : p.paramNames.join(", "),
      },
      { label: "approval", value: approvalLabel(p.approve) },
      { label: "unsaved changes", value: p.dirty ? "yes" : "no" },
      { label: "AI draft open", value: p.drafting ? "yes" : "no" },
      { label: "problems", value: String(p.diagnostics.length) },
      ...findingFacts(p.diagnostics, "problem"),
    ],
    entities: [{ kind: "tool", id: p.id, label: p.description }],
  };
}

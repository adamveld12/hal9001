// Agent transcript: merging paged/live events by seq, and folding the raw
// `session/update` records into display items (messages, thoughts, tool calls,
// plans). Pure; payload shapes follow ACP `session/update` (camelCase), with
// snake_case accepted too.

import type { Schemas } from "../../api/client";
import type { Tone } from "../../lib/status";

type AgentEvent = Schemas["AgentEvent"];

/**
 * Merge `incoming` into `prev` (both ascending by seq) without duplicates.
 * An event seen twice keeps the later copy. Returns `prev` itself when nothing
 * changes, so callers can skip a re-render.
 */
export function mergeEvents(
  prev: AgentEvent[],
  incoming: readonly AgentEvent[],
): AgentEvent[] {
  if (incoming.length === 0) return prev;
  const lastSeq = prev.length > 0 ? prev[prev.length - 1].seq : 0;
  let ordered = true;
  for (let i = 0; i < incoming.length; i++) {
    const before = i === 0 ? lastSeq : incoming[i - 1].seq;
    if (incoming[i].seq <= before) {
      ordered = false;
      break;
    }
  }
  if (ordered) return [...prev, ...incoming];

  const bySeq = new Map<number, AgentEvent>();
  for (const e of prev) bySeq.set(e.seq, e);
  for (const e of incoming) bySeq.set(e.seq, e);
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq);
}

/**
 * Highest seq n such that events 1..n are all present (0 when seq 1 is
 * missing). Refetching after this seq repairs a gap left by an event that
 * became visible out of order.
 */
export function lastContiguousSeq(events: readonly AgentEvent[]): number {
  let expected = 1;
  for (const e of events) {
    if (e.seq !== expected) break;
    expected++;
  }
  return expected - 1;
}

// ---------------------------------------------------------------- payloads

type Obj = Record<string, unknown>;

function asObj(v: unknown): Obj | null {
  return v !== null && typeof v === "object" && !Array.isArray(v)
    ? (v as Obj)
    : null;
}

function pick(o: Obj | null, ...keys: string[]): unknown {
  if (!o) return undefined;
  for (const k of keys) if (o[k] !== undefined) return o[k];
  return undefined;
}

function asStr(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

/** Text of a message/thought chunk; other content types become a marker. */
export function chunkText(payload: unknown): string {
  const c = pick(asObj(payload), "content", "text");
  if (typeof c === "string") return c;
  const o = asObj(c);
  if (!o) return "";
  const text = asStr(o.text);
  if (text !== undefined) return text;
  return `[${asStr(o.type) ?? "content"}]`;
}

export interface PlanEntry {
  content: string;
  priority: string | null;
  status: string | null;
}

export function planEntries(payload: unknown): PlanEntry[] {
  const raw = pick(asObj(payload), "entries");
  if (!Array.isArray(raw)) return [];
  return raw.map((e) => {
    const o = asObj(e);
    return {
      content: asStr(o?.content) ?? "",
      priority: asStr(o?.priority) ?? null,
      status: asStr(o?.status) ?? null,
    };
  });
}

export type ToolContent =
  | { type: "text"; text: string }
  | { type: "diff"; path: string; oldText: string | null; newText: string }
  | { type: "terminal"; terminalId: string }
  | { type: "json"; value: unknown };

export function toolContent(raw: unknown): ToolContent[] {
  if (!Array.isArray(raw)) return [];
  return raw.map((item): ToolContent => {
    const o = asObj(item);
    switch (asStr(o?.type)) {
      case "content": {
        const inner = asObj(o?.content);
        const text = asStr(inner?.text);
        return text !== undefined
          ? { type: "text", text }
          : { type: "json", value: o?.content };
      }
      case "diff":
        return {
          type: "diff",
          path: asStr(o?.path) ?? "",
          oldText: asStr(pick(o, "oldText", "old_text")) ?? null,
          newText: asStr(pick(o, "newText", "new_text")) ?? "",
        };
      case "terminal":
        return {
          type: "terminal",
          terminalId: asStr(pick(o, "terminalId", "terminal_id")) ?? "",
        };
      default:
        return { type: "json", value: item };
    }
  });
}

function locationPaths(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  const out: string[] = [];
  for (const l of raw) {
    const o = asObj(l);
    const path = asStr(o?.path);
    if (path === undefined) continue;
    const line = o?.line;
    out.push(typeof line === "number" ? `${path}:${line}` : path);
  }
  return out;
}

// ---------------------------------------------------------------- items

interface ItemBase {
  /** seq of the first event; stable while the item grows. */
  key: number;
  ts: string;
}

export interface TextItem extends ItemBase {
  type: "message" | "thought" | "user";
  text: string;
}

export interface ToolItem extends ItemBase {
  type: "tool";
  toolCallId: string | null;
  title: string;
  toolKind: string | null;
  status: string | null;
  input: unknown;
  output: unknown;
  content: ToolContent[];
  locations: string[];
  /** seq of the latest event folded into this item. */
  lastSeq: number;
}

export interface PlanItem extends ItemBase {
  type: "plan";
  entries: PlanEntry[];
}

export interface OtherItem extends ItemBase {
  type: "other";
  kind: string;
  payload: unknown;
}

export type TranscriptItem = TextItem | ToolItem | PlanItem | OtherItem;

const TEXT_KINDS: Record<string, TextItem["type"]> = {
  message: "message",
  thought: "thought",
  user_message: "user",
};

function applyToolFields(item: ToolItem, p: Obj | null, seq: number): void {
  const title = asStr(pick(p, "title"));
  if (title !== undefined) item.title = title;
  const kind = asStr(pick(p, "kind", "tool_kind"));
  if (kind !== undefined) item.toolKind = kind;
  const status = asStr(pick(p, "status"));
  if (status !== undefined) item.status = status;
  const input = pick(p, "rawInput", "raw_input");
  if (input !== undefined) item.input = input;
  const output = pick(p, "rawOutput", "raw_output");
  if (output !== undefined) item.output = output;
  const content = pick(p, "content");
  if (Array.isArray(content)) item.content = toolContent(content);
  const locations = pick(p, "locations");
  if (Array.isArray(locations)) item.locations = locationPaths(locations);
  item.lastSeq = seq;
}

/**
 * Fold events (ascending by seq) into display items. Adjacent chunks of the
 * same kind join into one message; `tool_call_update`s fold into their
 * `tool_call` (an update replaces the fields it carries, as ACP defines); a
 * plan replaces the plan directly before it.
 */
export function buildTranscript(
  events: readonly AgentEvent[],
): TranscriptItem[] {
  const items: TranscriptItem[] = [];
  const tools = new Map<string, ToolItem>();

  for (const ev of events) {
    const p = asObj(ev.payload);
    const last = items[items.length - 1];

    const textType = TEXT_KINDS[ev.kind];
    if (textType) {
      const text = chunkText(ev.payload);
      if (last?.type === textType) last.text += text;
      else items.push({ type: textType, key: ev.seq, ts: ev.ts, text });
      continue;
    }

    if (ev.kind === "tool_call" || ev.kind === "tool_call_update") {
      const id =
        ev.tool_call_id || asStr(pick(p, "toolCallId", "tool_call_id")) || null;
      let tool = id ? tools.get(id) : undefined;
      if (!tool) {
        tool = {
          type: "tool",
          key: ev.seq,
          ts: ev.ts,
          toolCallId: id ?? null,
          title: id ? `Tool call ${id}` : "Tool call",
          toolKind: null,
          status: null,
          input: undefined,
          output: undefined,
          content: [],
          locations: [],
          lastSeq: ev.seq,
        };
        items.push(tool);
        if (id) tools.set(id, tool);
      }
      applyToolFields(tool, p, ev.seq);
      continue;
    }

    if (ev.kind === "plan") {
      const entries = planEntries(ev.payload);
      if (last?.type === "plan") last.entries = entries;
      else items.push({ type: "plan", key: ev.seq, ts: ev.ts, entries });
      continue;
    }

    items.push({
      type: "other",
      key: ev.seq,
      ts: ev.ts,
      kind: ev.kind,
      payload: ev.payload,
    });
  }
  return items;
}

/** Badge tone for an ACP tool call status. */
export function toolTone(status: string | null): Tone {
  switch (status) {
    case "completed":
      return "ok";
    case "failed":
      return "bad";
    case "in_progress":
      return "run";
    case "pending":
      return "info";
    default:
      return "muted";
  }
}

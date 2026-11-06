// Keeps the flow canvas and the assist session's draft (flow.yaml) in step. The
// decisions are the ones draftSync.ts makes for text editors, applied to the
// canvas: the "text" compared is the normalized flow, not the YAML, so that a
// draft the canvas would draw the same way (comments, key order, missing
// positions) is not an update and a round trip never echoes. The YAML <-> flow
// conversion is the daemon's (POST /api/flows/validate), so the state here is
// fed with already parsed flows. Pure; useFlowDraftSync.ts runs it.

import { splitFlow } from "../../flow/editorState";
import { flowToGraph, graphToFlow } from "../../flow/graph";
import type { Flow } from "../../flow/model";
import {
  initSync,
  keepMine,
  pushed,
  reconcileDraft,
  shouldPush,
  takeAi,
  type DraftView,
  type SyncState,
} from "./draftSync";

/**
 * The flow as the canvas serializes it right after loading it: every node has
 * a position (nodes without one get the auto layout of flowToGraph), defaults
 * are dropped.
 */
export function normalizeFlow(flow: Flow): Flow {
  return graphToFlow(splitFlow(flow), flowToGraph(flow));
}

function canonical(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(canonical);
  if (v !== null && typeof v === "object") {
    return Object.fromEntries(
      Object.entries(v)
        .filter(([, x]) => x !== undefined)
        .toSorted(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
        .map(([k, x]) => [k, canonical(x)]),
    );
  }
  return v;
}

/** What two flows must share to look the same on the canvas, independent of key order. */
export function flowKey(flow: Flow): string {
  return JSON.stringify(canonical(normalizeFlow(flow)));
}

export interface FlowSyncState {
  sync: SyncState;
  /** The last draft flow that parsed: what the canvas shows. Null until the first draft arrives. */
  shown: Flow | null;
  /** Counts the times the canvas must load `shown` anew. */
  rev: number;
  /** The agent's flow that waits for the user's decision. */
  pendingFlow: Flow | null;
  /** The newest draft does not parse; the canvas keeps the last good flow. */
  invalid: boolean;
}

/** State of a canvas that has just been bound to a session. It holds nothing the draft could overwrite. */
export function initFlowSync(): FlowSyncState {
  return {
    sync: initSync("", false),
    shown: null,
    rev: 0,
    pendingFlow: null,
    invalid: false,
  };
}

/** `editor`: the canvas's flow, null while it is not mounted. */
function editorKey(editor: Flow | null): string {
  return editor === null ? "" : flowKey(editor);
}

/**
 * A draft arrived from the server (fetch or stream). `parsed` is its flow, null
 * when the YAML does not parse; `editor` is what the canvas holds now.
 */
export function applyDraft(
  state: FlowSyncState,
  draft: DraftView,
  parsed: Flow | null,
  editor: Flow | null,
): FlowSyncState {
  const { sync } = state;
  if (parsed === null) {
    // The canvas keeps its last good flow. The broken draft is adopted so the
    // user's next edit replaces it, unless an agent update still waits.
    if (sync.pending !== null) return { ...state, invalid: true };
    return {
      ...state,
      invalid: true,
      sync: { ...sync, syncedHash: draft.hash },
    };
  }

  const text = editorKey(editor);
  const r = reconcileDraft(
    sync,
    { hash: draft.hash, yaml: flowKey(parsed), updated_by: draft.updated_by },
    text,
  );
  const loads = r.text !== text;
  let pendingFlow: Flow | null = null;
  if (r.sync.pending !== null) {
    pendingFlow = r.sync.pending === sync.pending ? state.pendingFlow : parsed;
  }
  return {
    sync: r.sync,
    shown: loads || state.shown === null ? parsed : state.shown,
    rev: loads ? state.rev + 1 : state.rev,
    pendingFlow,
    invalid: false,
  };
}

/** The agent's update the user was asked about, as long as the canvas differs from it. */
export function pendingUpdate(
  state: FlowSyncState,
  editor: Flow | null,
): Flow | null {
  const p = state.sync.pending;
  return p !== null && p.yaml !== editorKey(editor) ? state.pendingFlow : null;
}

/** "Take AI version": the canvas loads the waiting flow. */
export function takeAiVersion(
  state: FlowSyncState,
  editor: Flow | null,
): FlowSyncState {
  if (state.sync.pending === null || state.pendingFlow === null) return state;
  const r = takeAi(state.sync, editorKey(editor));
  return {
    sync: r.sync,
    shown: state.pendingFlow,
    rev: state.rev + 1,
    pendingFlow: null,
    invalid: false,
  };
}

/** "Keep mine": the next push replaces the agent's update. */
export function keepMineVersion(state: FlowSyncState): FlowSyncState {
  return { ...state, sync: keepMine(state.sync), pendingFlow: null };
}

/** Whether the canvas holds edits that are not in the draft yet. Waits while an agent update is undecided. */
export function needsPush(state: FlowSyncState, editor: Flow | null): boolean {
  return editor !== null && shouldPush(state.sync, flowKey(editor));
}

/** The canvas's `sent` flow was written to the draft; `draft` is the server's answer. */
export function afterPush(
  state: FlowSyncState,
  draft: { hash: string },
  sent: Flow,
): FlowSyncState {
  return {
    ...state,
    sync: pushed(draft, flowKey(sent)),
    pendingFlow: null,
    invalid: false,
  };
}

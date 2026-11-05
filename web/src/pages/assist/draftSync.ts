// Decisions for keeping the sensor editor and the assist session's draft in
// step. The editor text belongs to the user: an agent update replaces it only
// when the user has not typed since the last sync; otherwise it waits as
// `pending` for "Take AI version / Keep mine". User edits are pushed to the
// draft with `syncedHash` as the expected hash. Pure; useDraftSync.ts applies
// the results.

import type { AssistDraft } from "../../api/assist";
import type { Checked } from "../files/diagnostics";
import { lineDiff } from "../approvals/diff";

export interface SyncState {
  /** Hash of the server draft the editor was last reconciled with; null before the first draft arrives. */
  syncedHash: string | null;
  /** Text of that draft. The editor differs from it when edits are waiting to be pushed. Null: the editor held unsynced edits when the session was bound. */
  syncedText: string | null;
  /** An agent update the user has not decided on yet. */
  pending: AssistDraft | null;
}

export interface AiChange {
  before: string;
  after: string;
}

export interface Reconciled {
  sync: SyncState;
  text: string;
  /** Set when the editor text was replaced by an AI draft. */
  aiChange: AiChange | null;
}

/** Sync state for an editor that has just been bound to a session. */
export function initSync(editorText: string, editorDirty: boolean): SyncState {
  return {
    syncedHash: null,
    syncedText: editorDirty ? null : editorText,
    pending: null,
  };
}

function adopt(draft: AssistDraft): SyncState {
  return { syncedHash: draft.hash, syncedText: draft.yaml, pending: null };
}

/** What to do with a draft that arrived from the server (fetch or stream) while the editor holds `text`. */
export function reconcileDraft(
  sync: SyncState,
  draft: AssistDraft,
  text: string,
): Reconciled {
  const keep = (s: SyncState): Reconciled => ({
    sync: s,
    text,
    aiChange: null,
  });
  const take = (): Reconciled => ({
    sync: adopt(draft),
    text: draft.yaml,
    aiChange:
      draft.updated_by === "agent" ? { before: text, after: draft.yaml } : null,
  });

  if (sync.syncedHash === draft.hash && sync.pending === null)
    return keep(sync);
  if (sync.pending?.hash === draft.hash) return keep(sync);
  // Already the same text: nothing to decide.
  if (draft.yaml === text) return keep(adopt(draft));

  const clean = sync.syncedText !== null && text === sync.syncedText;
  // First draft after binding: a clean editor shows the session's draft; edits wait for a decision.
  if (sync.syncedHash === null) {
    return clean ? take() : keep({ ...sync, pending: draft });
  }
  // The user's own change coming back (this tab's push or message): the editor text stays and is pushed again if it moved on.
  if (draft.updated_by === "user") return keep(adopt(draft));
  return clean ? take() : keep({ ...sync, pending: draft });
}

/** Whether the editor holds edits to push to the draft. Waits while an agent update is undecided. */
export function shouldPush(sync: SyncState, text: string): boolean {
  return (
    sync.syncedHash !== null &&
    sync.pending === null &&
    text !== sync.syncedText
  );
}

/** A push succeeded: the server draft is `sentText` with the response's hash. Supersedes an undecided agent update. */
export function pushed(draft: AssistDraft, sentText: string): SyncState {
  return { syncedHash: draft.hash, syncedText: sentText, pending: null };
}

/** "Take AI version": the editor takes the pending draft. */
export function takeAi(sync: SyncState, text: string): Reconciled {
  const p = sync.pending;
  if (!p) return { sync, text, aiChange: null };
  return {
    sync: adopt(p),
    text: p.yaml,
    aiChange: { before: text, after: p.yaml },
  };
}

/** "Keep mine": rebase onto the pending draft's hash so the next push overwrites it. */
export function keepMine(sync: SyncState): SyncState {
  const p = sync.pending;
  return p ? adopt(p) : sync;
}

/** 1-based line numbers of `after` that are new or changed relative to `before`. */
export function changedLines(before: string, after: string): number[] {
  const out: number[] = [];
  let line = 0;
  for (const l of lineDiff(before, after)) {
    if (l.kind === "ctx") line++;
    else if (l.kind === "add") out.push(++line);
  }
  return out;
}

/**
 * The diagnostics list that describes `text`: the first candidate checked
 * against exactly this text, else the first candidate (which the display marks
 * stale).
 */
export function pickChecked(
  text: string,
  ...candidates: (Checked | null)[]
): Checked | null {
  const present = candidates.filter((c): c is Checked => c !== null);
  return present.find((c) => c.text === text) ?? present[0] ?? null;
}

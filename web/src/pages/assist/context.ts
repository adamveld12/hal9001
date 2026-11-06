import { createContext, useContext } from "react";
import type { AssistTargetKind } from "../../api/assist";

/** What the open editor offers the drawer: its current text and a way to push pending edits to the draft. */
export interface EditorBridge {
  /**
   * The editor's current text, sent to start a session or with a message so the agent works on it.
   * Undefined: the editor has nothing beyond the saved file, so the daemon uses the file.
   */
  getText: () => string | undefined | Promise<string | undefined>;
  /** Resolves once edits waiting to be pushed to the draft have been sent. */
  flush: () => Promise<void>;
  /** True while an agent update waits for the user's decision, so the editor shows something other than the draft. */
  undecided?: () => boolean;
}

export interface AssistPage {
  /** What the page edits: the drawer lists, starts and titles sessions of this kind. */
  kind: AssistTargetKind;
  /** The `assist` query parameter: null when the drawer is closed, "new" before a session exists, else a session id. */
  param: string | null;
  /** The open session's id, if the drawer has one. */
  sessionId: string | null;
  /** Sensor or flow the page edits; null on the page for a new one. */
  targetId: string | null;
  open: () => void;
  close: () => void;
  /** Point the drawer at a session (after creating or resuming one). */
  bind: (sessionId: string) => void;
  /** Called by whichever editor is mounted (null on unmount). */
  registerEditor: (editor: EditorBridge | null) => void;
  /** The mounted editor, if any. */
  editor: () => EditorBridge | null;
}

export const AssistPageContext = createContext<AssistPage | null>(null);

export function useAssistPage(): AssistPage {
  const ctx = useContext(AssistPageContext);
  if (!ctx) throw new Error("useAssistPage outside a page with the assistant");
  return ctx;
}

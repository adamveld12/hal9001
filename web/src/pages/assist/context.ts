import { createContext, useContext } from "react";

/** What the open editor offers the drawer: its current text and a way to push pending edits to the draft. */
export interface EditorBridge {
  getText: () => string;
  /** Resolves once edits waiting to be pushed to the draft have been sent. */
  flush: () => Promise<void>;
}

export interface AssistPage {
  /** The `assist` query parameter: null when the drawer is closed, "new" before a session exists, else a session id. */
  param: string | null;
  /** The open session's id, if the drawer has one. */
  sessionId: string | null;
  /** Sensor the page edits; null on the new-sensor page. */
  sensorId: string | null;
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
  if (!ctx) throw new Error("useAssistPage outside the sensors page");
  return ctx;
}

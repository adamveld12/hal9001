import { createContext, useContext } from "react";
import type { AssistSession } from "../../api/assist";
import type { AssistPageContext } from "../../lib/pageContext";
import type { DrawerTarget, Seed } from "./drawerState";

/**
 * What the open editor offers the drawer. The drawer feeds an editor's text to
 * the agent only when the editor is for the drawer's own target.
 */
export interface EditorBridge {
  /** The sensor, flow or tool the editor shows; id null on the page for a new one. */
  kind: DrawerTarget["kind"];
  id: string | null;
  /**
   * The editor's current text, sent to start a session or with a message so the agent works on it.
   * Undefined: the editor has nothing beyond the saved file, so the daemon uses the file.
   */
  getText: () => string | undefined | Promise<string | undefined>;
  /** Resolves once edits waiting to be pushed to the draft have been sent. */
  flush: () => Promise<void>;
  /** True while an agent update waits for the user's decision, so the editor shows something other than the draft. */
  undecided?: () => boolean;
  /** True while the editor holds changes that leaving the page would drop. */
  dirty?: () => boolean;
  /**
   * Asked before the drawer moves away from the editor's target or closes
   * while the editor is shown only for it (a new-sensor pane); false keeps it.
   */
  confirmLeave?: () => boolean;
}

export interface Assist {
  open: boolean;
  /** The session the drawer shows; null shows the start view. */
  sessionId: string | null;
  /** What the shown session (or the start view) is about. */
  target: DrawerTarget;
  /** Composer text from a starter prompt or an entry point. */
  seed: Seed | null;
  /** A failure from something the assistant triggered (a draft it started, a link that did not resolve). */
  notice: string | null;
  /** An editor for the drawer's target is mounted, so the session's draft is shown there. */
  editorActive: boolean;
  openDrawer: () => void;
  close: () => void;
  toggle: () => void;
  /** Show the start view for a target (or its session when it is shown already), with optional composer text. */
  openTarget: (target: DrawerTarget, seed?: string) => void;
  /** A fresh general chat; the chat that was shown keeps running. */
  newChat: () => void;
  select: (session: Pick<AssistSession, "id" | "target">) => void;
  /** Show a session by id (a link from an agent run or an approval). */
  openSession: (id: string) => void;
  /** Point the drawer at a session that was just created. `target` defaults to the current one. */
  bind: (sessionId: string, target?: DrawerTarget) => void;
  /** The target moved on (a new sensor was saved); the session stays. */
  retarget: (target: DrawerTarget) => void;
  seedText: (text: string) => void;
  /** The composer took the seed; it is not applied again when the composer remounts. */
  consumeSeed: () => void;
  dismissNotice: () => void;
  /** Called by whichever editor is mounted (null on unmount). */
  registerEditor: (editor: EditorBridge | null) => void;
  /** The mounted editor when it is for the drawer's target, else null. */
  editor: () => EditorBridge | null;
  /** The page context for a turn: the page's published description, capped, with the route. */
  pageContext: () => AssistPageContext;
  /** Show the drawer's chat on the Chat page (`/chat/<session id>`) and close the drawer; the chat is the same session. */
  openFullPage: () => void;
  /** Leave the Chat page: the drawer shows `session` (null: its start view) and the page the user came from returns. */
  returnToDrawer: (
    session: Pick<AssistSession, "id" | "target"> | null,
  ) => void;
  /**
   * The Chat page reports the chat it shows (null: none). While that page is
   * open the assistant's navigate_ui and start_draft requests are taken for it.
   */
  registerChatPage: (sessionId: string | null) => void;
}

export const AssistContext = createContext<Assist | null>(null);

export function useAssist(): Assist {
  const ctx = useContext(AssistContext);
  if (!ctx) throw new Error("useAssist outside AssistProvider");
  return ctx;
}

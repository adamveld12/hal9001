// What happens to the text of the message box around a send, and across the
// message box that replaces it. Used by Composer.tsx.

/**
 * The box after a send failed: the message that was sent comes back, followed
 * by what was typed while it was on its way.
 */
export function afterFailedSend(sent: string, typed: string): string {
  return typed === "" ? sent : `${sent}\n${typed}`;
}

let carried = "";

/**
 * Keep text from a message box that is about to go away (the start view, once
 * its first message was accepted), for the next one that mounts.
 */
export function carryDraft(text: string): void {
  carried = text;
}

/** The text kept by `carryDraft`, once; "" when there is none. */
export function takeCarriedDraft(): string {
  const text = carried;
  carried = "";
  return text;
}

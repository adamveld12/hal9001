/**
 * Focus helpers for the assist drawer. A focused button that is disabled or
 * unmounted drops focus to <body> and nothing restores it, so keystrokes would
 * go to the page (and its global shortcuts) instead of the message box.
 */

/** Whether focus has fallen to the page: nothing, or <body>, is focused. */
export function focusLost(
  active: Element | null,
  body: Element | null,
): boolean {
  return active === null || active === body;
}

/** Selector of the assist drawer; its presence suspends the global shortcuts. */
export const ASSIST_DRAWER_SELECTOR = "[aria-label='AI assistant']";

/** Whether the assist drawer is open in `doc`. */
export function assistDrawerOpen(
  doc: Pick<Document, "querySelector">,
): boolean {
  return doc.querySelector(ASSIST_DRAWER_SELECTOR) !== null;
}

/**
 * Props for a drawer button that must not take focus when clicked with the
 * mouse. Keyboard activation (Tab, Enter, Space) is unaffected.
 */
export const noFocusOnClick = {
  onMouseDown: (e: { preventDefault: () => void }) => e.preventDefault(),
};

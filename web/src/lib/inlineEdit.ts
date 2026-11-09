// State machine of the inline editors (item name, description): click to edit,
// Enter to save, Esc to cancel. The component only renders this state.

/** File names are ids: letters, digits, - and _, starting with a letter or digit. */
export const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;

/**
 * The reason `next` cannot be the new name of the item called `current`, or
 * null. `taken` are the names of the other items of the same kind.
 */
export function validateName(
  next: string,
  current: string,
  taken: readonly string[],
  noun: string,
): string | null {
  if (next === "") return "A name is required.";
  if (!SLUG.test(next)) {
    return "Use letters, digits, - and _ (max 64), starting with a letter or digit.";
  }
  if (next !== current && taken.includes(next)) {
    return `A ${noun} named ${next} already exists.`;
  }
  return null;
}

export interface InlineEditState {
  editing: boolean;
  draft: string;
  /** Validation or server message for the current draft. */
  error: string | null;
  saving: boolean;
}

export const IDLE: InlineEditState = {
  editing: false,
  draft: "",
  error: null,
  saving: false,
};

export function startEditing(current: string): InlineEditState {
  return { editing: true, draft: current, error: null, saving: false };
}

export function typed(s: InlineEditState, draft: string): InlineEditState {
  return { ...s, draft, error: null };
}

/** What the component must do after a key press. */
export type KeyOutcome =
  | { state: InlineEditState; action: "none" }
  | { state: InlineEditState; action: "save"; value: string };

/**
 * Enter validates the draft: an invalid one stays open with the message, an
 * unchanged one closes without saving, anything else asks the component to
 * save. Esc discards the draft. Keys are ignored while a save is running.
 */
export function pressKey(
  s: InlineEditState,
  key: string,
  current: string,
  validate: (value: string) => string | null,
): KeyOutcome {
  if (!s.editing || s.saving) return { state: s, action: "none" };
  if (key === "Escape") return { state: IDLE, action: "none" };
  if (key !== "Enter") return { state: s, action: "none" };
  const value = s.draft.trim();
  if (value === current) return { state: IDLE, action: "none" };
  const error = validate(value);
  if (error !== null) return { state: { ...s, error }, action: "none" };
  return { state: { ...s, draft: value, saving: true }, action: "save", value };
}

/** The save failed: stay open and show why. */
export function saveFailed(
  s: InlineEditState,
  message: string,
): InlineEditState {
  return { ...s, saving: false, error: message };
}

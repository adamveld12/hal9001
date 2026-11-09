import { useRef, useState } from "react";
import { errorMessage } from "../api/client";
import {
  IDLE,
  pressKey,
  saveFailed,
  startEditing,
  typed,
} from "../lib/inlineEdit";
import { cx } from "../lib/cx";

/**
 * A value that is edited in place: click to edit, Enter to save, Esc to cancel.
 * `validate` returns a message for a value that cannot be saved (shown under
 * the field); `onSave` may reject, and its message is shown the same way.
 */
export function InlineEdit({
  value,
  label,
  placeholder,
  validate,
  onSave,
  mono,
  muted,
  disabledReason,
  className,
}: {
  value: string;
  /** Accessible name of the field, e.g. "Sensor name". */
  label: string;
  /** Shown instead of an empty value. */
  placeholder?: string;
  validate: (value: string) => string | null;
  onSave: (value: string) => Promise<unknown>;
  mono?: boolean;
  muted?: boolean;
  /** Set to explain why the value cannot be edited right now. */
  disabledReason?: string | null;
  className?: string;
}) {
  const [state, setState] = useState(IDLE);
  // Escape and blur both close the editor; this avoids saving after either.
  const closed = useRef(false);

  const text = cx(
    mono && "font-mono",
    muted ? "text-xs text-muted" : "text-sm",
  );

  if (!state.editing) {
    const empty = value === "";
    return (
      <button
        type="button"
        aria-label={`${label}: ${empty ? "empty" : value}. Click to edit`}
        title={disabledReason ?? "Click to edit"}
        disabled={disabledReason != null}
        onClick={() => {
          closed.current = false;
          setState(startEditing(value));
        }}
        className={cx(
          "m-0 rounded-md border border-transparent px-1 py-0.5 text-left enabled:hover:border-line enabled:hover:bg-raised disabled:cursor-default",
          text,
          !muted && "font-semibold",
          empty && "text-muted italic",
          className,
        )}
      >
        {empty ? (placeholder ?? "") : value}
      </button>
    );
  }

  const save = (v: string) => {
    onSave(v).then(
      () => setState(IDLE),
      (e: unknown) => setState((s) => saveFailed(s, errorMessage(e))),
    );
  };

  return (
    <span className={cx("inline-flex flex-col gap-1", className)}>
      <input
        // eslint-disable-next-line jsx-a11y/no-autofocus
        autoFocus
        aria-label={label}
        aria-invalid={state.error !== null}
        value={state.draft}
        disabled={state.saving}
        onChange={(e) => setState((s) => typed(s, e.target.value))}
        onKeyDown={(e) => {
          const out = pressKey(state, e.key, value, validate);
          if (out.action === "none" && out.state === state) return;
          e.preventDefault();
          if (e.key === "Escape") closed.current = true;
          setState(out.state);
          if (out.action === "save") save(out.value);
        }}
        onBlur={() => {
          if (!closed.current && !state.saving) setState(IDLE);
        }}
        className={cx(
          "h-7 min-w-64 rounded-md border bg-panel px-2",
          text,
          state.error ? "border-bad" : "border-line",
        )}
      />
      {state.error && (
        <span role="alert" className="text-xs font-normal text-bad">
          {state.error}
        </span>
      )}
    </span>
  );
}

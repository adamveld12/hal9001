import { cx } from "../../lib/cx";

/** An on/off switch with an accessible name; the visible text is up to the caller. */
export function AuthSwitch({
  checked,
  label,
  disabled,
  onChange,
}: {
  checked: boolean;
  /** Accessible name, e.g. "Passkeys". */
  label: string;
  disabled?: boolean;
  onChange: (next: boolean) => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cx(
        "relative h-4 w-7 shrink-0 rounded-full border border-line transition-colors",
        "focus-visible:outline-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50",
        checked ? "bg-accent" : "bg-raised",
      )}
    >
      <span
        aria-hidden="true"
        className={cx(
          "absolute top-0.5 size-2.5 rounded-full transition-all",
          checked ? "left-3.5 bg-panel" : "left-0.5 bg-muted",
        )}
      />
    </button>
  );
}

import { useEffect, useRef, useState, type ReactNode } from "react";
import { cx } from "../lib/cx";
import { loadRerunSeen, saveRerunSeen } from "../lib/runNow";
import { Button, Spinner } from "./ui";

/** Closes a popover on an outside click or Escape. */
function usePopover() {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  return { open, setOpen, root };
}

/**
 * "Run now" with a caret menu holding the "Re-run items already seen" option.
 * The option is remembered in localStorage under `storageKey` (a sensor id, or
 * `flow:<id>` for a flow).
 */
export function RunNowButton({
  storageKey,
  pending,
  disabled,
  title,
  onRun,
}: {
  storageKey: string;
  pending: boolean;
  disabled?: boolean;
  /** Tooltip; the reason when disabled. */
  title?: string;
  onRun: (rerunSeen: boolean) => void;
}) {
  const [rerun, setRerun] = useState(() => loadRerunSeen(storageKey));
  const [prevKey, setPrevKey] = useState(storageKey);
  if (prevKey !== storageKey) {
    setPrevKey(storageKey);
    setRerun(loadRerunSeen(storageKey));
  }
  const { open, setOpen, root } = usePopover();
  const off = disabled || pending;
  return (
    <span ref={root} className="relative inline-flex">
      <Button
        icon={pending ? undefined : "play"}
        className="rounded-r-none"
        disabled={off}
        title={title}
        onClick={() => onRun(rerun)}
      >
        {pending ? <Spinner label="Running" /> : "Run now"}
      </Button>
      <Button
        className="-ml-px rounded-l-none px-1.5"
        aria-label="Run now options"
        aria-haspopup="true"
        aria-expanded={open}
        disabled={off}
        onClick={() => setOpen((o) => !o)}
      >
        <span aria-hidden="true">▾</span>
      </Button>
      {open && (
        <div
          role="group"
          aria-label="Run now options"
          className="absolute right-0 top-full z-20 mt-1 w-max rounded-md border border-line bg-panel p-2 text-xs shadow-lg"
        >
          <label className="flex cursor-pointer items-center gap-2">
            <input
              type="checkbox"
              checked={rerun}
              onChange={(e) => {
                setRerun(e.target.checked);
                saveRerunSeen(storageKey, e.target.checked);
              }}
            />
            Re-run items already seen
          </label>
        </div>
      )}
    </span>
  );
}

/** A "⋯" button opening a menu of actions. Items close the menu when clicked. */
export function OverflowMenu({
  label = "More actions",
  disabled,
  children,
}: {
  label?: string;
  disabled?: boolean;
  children: (close: () => void) => ReactNode;
}) {
  const { open, setOpen, root } = usePopover();
  return (
    <span ref={root} className="relative inline-flex">
      <Button
        aria-label={label}
        aria-haspopup="true"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => setOpen((o) => !o)}
      >
        <span aria-hidden="true">⋯</span>
      </Button>
      {open && (
        <div
          role="menu"
          className="absolute right-0 top-full z-20 mt-1 w-max min-w-40 rounded-md border border-line bg-panel py-1 text-xs shadow-lg"
        >
          {children(() => setOpen(false))}
        </div>
      )}
    </span>
  );
}

export function MenuItem({
  danger,
  onClick,
  children,
}: {
  danger?: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      role="menuitem"
      className={cx(
        "block w-full px-3 py-1.5 text-left hover:bg-raised",
        danger && "text-bad",
      )}
      onClick={onClick}
    >
      {children}
    </button>
  );
}

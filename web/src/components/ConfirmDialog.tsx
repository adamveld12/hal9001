import { useEffect, useId, useRef, type ReactNode } from "react";
import { Button } from "./ui";

/** A modal confirmation. `open` controls it; Escape and Cancel call `onCancel`. */
export function ConfirmDialog({
  open,
  title,
  children,
  confirmLabel,
  busy,
  confirmDisabled,
  confirmVariant = "danger",
  testId,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  title: string;
  children: ReactNode;
  confirmLabel: string;
  busy?: boolean;
  /** Disables only the confirm button (Cancel stays available). */
  confirmDisabled?: boolean;
  /** Style of the confirm button; destructive by default. */
  confirmVariant?: "danger" | "primary";
  /** `data-testid` of the dialog element. */
  testId?: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      data-testid={testId}
      onCancel={(e) => {
        e.preventDefault();
        onCancel();
      }}
      // Escape cancels the dialog only; the Ask AI drawer behind it listens on the window and would close too.
      onKeyDown={(e) => e.key === "Escape" && e.stopPropagation()}
      className="m-auto w-[28rem] max-w-[90vw] rounded-md border border-line bg-panel p-4 text-fg backdrop:bg-black/50"
    >
      <h2 id={titleId} className="m-0 text-sm font-semibold">
        {title}
      </h2>
      <div className="mt-2 text-xs text-muted">{children}</div>
      <div className="mt-4 flex justify-end gap-2">
        <Button onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button
          variant={confirmVariant}
          onClick={onConfirm}
          disabled={busy || confirmDisabled}
        >
          {confirmLabel}
        </Button>
      </div>
    </dialog>
  );
}

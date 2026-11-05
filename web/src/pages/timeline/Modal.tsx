import { useEffect, type ReactNode } from "react";
import { Button } from "../../components/ui";

/** Minimal modal dialog: backdrop click and Escape close it. */
export function Modal({
  title,
  onClose,
  children,
  footer,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/40 p-4 pt-[12vh]"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="w-full max-w-lg rounded-md border border-line bg-panel shadow-lg"
      >
        <header className="flex items-center border-b border-line px-3 py-2 text-xs font-semibold">
          {title}
          <Button
            variant="ghost"
            icon="close"
            className="ml-auto"
            aria-label="Close"
            onClick={onClose}
          />
        </header>
        <div className="space-y-3 p-3">{children}</div>
        {footer && (
          <footer className="flex justify-end gap-2 border-t border-line px-3 py-2">
            {footer}
          </footer>
        )}
      </div>
    </div>
  );
}

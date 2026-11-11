import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { assistDrawerOpen, isTypingTarget } from "../../lib/focus";
import { planRun } from "../../lib/commands/run";
import { CommandRegistry } from "../../lib/commands/registry";
import { loadRecent, pushRecent, saveRecent } from "../../lib/commands/recent";
import { isModKey, modKeyOf, resolveKey } from "../../lib/commands/shortcuts";
import type { CommandConfirm, RunContext } from "../../lib/commands/types";
import { cx } from "../../lib/cx";
import { CommandPalette } from "./CommandPalette";
import { CommandsContext, type Commands, type ToastTone } from "./context";

/** How long `g` waits for its second key. */
const SEQUENCE_MS = 1200;
const TOAST_MS = 5000;

/** The palette's own shortcut: Cmd+P (macOS) or Ctrl+P instead of the browser's print dialog. */
const PALETTE_KEY = "p";

interface Pending {
  id: string;
  ctx: RunContext;
  confirm: CommandConfirm;
}

/**
 * Owns the command registry, the palette, and every keyboard shortcut: a
 * command's `shortcut` is all it takes for a key to run it. Mounted by the app
 * shell, inside the assist provider.
 */
export function CommandProvider({ children }: { children: ReactNode }) {
  const [registry] = useState(() => new CommandRegistry());
  const [open, setOpen] = useState(false);
  const [recent, setRecent] = useState(loadRecent);
  const [pending, setPending] = useState<Pending | null>(null);
  const [toast, setToast] = useState<{
    text: string;
    tone: ToastTone;
    n: number;
  } | null>(null);
  const armed = useRef<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const latest = useRef({ open, recent });
  useEffect(() => {
    latest.current = { open, recent };
  });

  const openPalette = useCallback(() => setOpen(true), []);
  const closePalette = useCallback(() => setOpen(false), []);
  const notify = useCallback(
    (text: string, tone: ToastTone = "info") =>
      setToast((t) => ({ text, tone, n: (t?.n ?? 0) + 1 })),
    [],
  );
  useEffect(() => {
    if (toast === null) return;
    const t = setTimeout(() => setToast(null), TOAST_MS);
    return () => clearTimeout(t);
  }, [toast]);

  const run = useCallback(
    (id: string, ctx: RunContext) => {
      const c = registry.find(id);
      if (planRun(c).kind === "skip" || !c) return;
      const next = pushRecent(latest.current.recent, id);
      latest.current.recent = next;
      setRecent(next);
      saveRecent(next);
      c.run(ctx);
    },
    [registry],
  );

  const execute = useCallback(
    (id: string, ctx: RunContext) => {
      const plan = planRun(registry.find(id));
      if (plan.kind === "confirm")
        setPending({ id, ctx, confirm: plan.confirm });
      else if (plan.kind === "run") run(id, ctx);
    },
    [registry, run],
  );

  // Shortcuts. Cmd/Ctrl combinations work everywhere, also while typing, and
  // listen in the capture phase so an editor does not take the key first.
  // Plain keys (`g i`, `[`) are ignored while typing, while the Ask AI drawer
  // or the palette is open.
  useEffect(() => {
    const onModKey = (e: KeyboardEvent) => {
      if (isModKey(e, PALETTE_KEY)) {
        e.preventDefault();
        e.stopPropagation();
        setOpen((o) => !o);
        return;
      }
      const hit = registry
        .commands()
        .find(
          (c) =>
            c.shortcut !== undefined &&
            modKeyOf(c.shortcut) !== null &&
            isModKey(e, modKeyOf(c.shortcut) ?? ""),
        );
      if (!hit) return;
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
      execute(hit.id, { source: "shortcut" });
    };
    window.addEventListener("keydown", onModKey, true);

    const disarm = () => {
      armed.current = null;
      clearTimeout(timer.current);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (latest.current.open || assistDrawerOpen(document)) return;
      if (isTypingTarget(e.target)) return;
      const r = resolveKey(registry.commands(), armed.current, e.key);
      disarm();
      if (r.kind === "pending") {
        armed.current = r.first;
        timer.current = setTimeout(disarm, SEQUENCE_MS);
      } else if (r.kind === "run") {
        e.preventDefault();
        execute(r.command.id, { source: "shortcut" });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onModKey, true);
      window.removeEventListener("keydown", onKey);
      disarm();
    };
  }, [registry, execute]);

  const value = useMemo<Commands>(
    () => ({
      registry,
      open,
      openPalette,
      closePalette,
      recent,
      execute,
      notify,
    }),
    [registry, open, openPalette, closePalette, recent, execute, notify],
  );

  return (
    <CommandsContext.Provider value={value}>
      {children}
      {open && <CommandPalette />}
      <ConfirmDialog
        open={pending !== null}
        title={pending?.confirm.title ?? ""}
        confirmLabel={pending?.confirm.confirmLabel ?? "Confirm"}
        testId="command-confirm"
        onCancel={() => setPending(null)}
        onConfirm={() => {
          if (pending) run(pending.id, pending.ctx);
          setPending(null);
        }}
      >
        {pending?.confirm.message}
      </ConfirmDialog>
      {toast && (
        <div
          role="status"
          className={cx(
            "fixed bottom-4 left-1/2 z-50 max-w-[90vw] -translate-x-1/2 rounded-md border bg-panel px-3 py-2 text-xs shadow-lg",
            toast.tone === "error" ? "border-bad/50 text-bad" : "border-line",
          )}
        >
          {toast.text}
        </div>
      )}
    </CommandsContext.Provider>
  );
}

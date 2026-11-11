import { createContext, useContext, useEffect } from "react";
import type { CommandRegistry } from "../../lib/commands/registry";
import type { Command, RunContext } from "../../lib/commands/types";

export type ToastTone = "info" | "error";

export interface Commands {
  registry: CommandRegistry;
  /** Whether the palette is open. */
  open: boolean;
  openPalette: () => void;
  closePalette: () => void;
  /** Command ids used, most recent first. */
  recent: readonly string[];
  /**
   * Run a command by id the way a shortcut or the palette does: skipped when
   * disabled, behind its confirmation when it has one, remembered as recent.
   */
  execute: (id: string, ctx: RunContext) => void;
  /** A short message at the bottom of the window, for a command's outcome. */
  notify: (text: string, tone?: ToastTone) => void;
}

export const CommandsContext = createContext<Commands | null>(null);

export function useCommands(): Commands {
  const ctx = useContext(CommandsContext);
  if (!ctx) throw new Error("useCommands outside CommandProvider");
  return ctx;
}

/**
 * Register `commands` under `owner` while the caller is mounted. Call it every
 * render with the current commands: the registry takes the newest `run`
 * closures and only tells the palette when something visible changed.
 */
export function useRegisterCommands(
  owner: string,
  commands: readonly Command[],
): void {
  const { registry } = useCommands();
  useEffect(() => {
    registry.set(owner, commands);
  });
  useEffect(() => () => registry.remove(owner), [registry, owner]);
}

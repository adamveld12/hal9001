import type { Command, CommandConfirm } from "./types";

export type RunPlan =
  /** Unknown or disabled: nothing happens. */
  | { kind: "skip" }
  /** Dangerous: ask first. */
  | { kind: "confirm"; confirm: CommandConfirm }
  | { kind: "run" };

/** What running `command` from the palette or a shortcut does first. */
export function planRun(command: Command | undefined): RunPlan {
  if (!command || command.disabled !== undefined) return { kind: "skip" };
  if (command.confirm) return { kind: "confirm", confirm: command.confirm };
  return { kind: "run" };
}

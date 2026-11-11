// The command model shared by the palette and the keyboard shortcuts. A
// command is registered once (see registry.ts); the palette lists it and the
// shortcut handler runs it, so the two cannot diverge. Pure types: no React.

import type { IconName } from "../../components/icons";

/** Palette sections, in display order. */
export const GROUP_ORDER = [
  "Go to",
  "Create",
  "Chat",
  "Actions",
  "Flows",
  "Sensors",
  "Tools",
  "Prompts",
  "Conversations",
] as const;

export type CommandGroup = (typeof GROUP_ORDER)[number];

/** Groups of jump-to-item commands: capped when searching. */
export const ITEM_GROUPS: readonly CommandGroup[] = [
  "Flows",
  "Sensors",
  "Tools",
  "Prompts",
  "Conversations",
];

/** Asked before a dangerous command runs, from the palette or a shortcut. */
export interface CommandConfirm {
  title: string;
  /** What the command affects, e.g. "Stops 3 running flows". */
  message: string;
  confirmLabel: string;
}

export type RunSource = "palette" | "shortcut";

export interface RunContext {
  /** Text typed after the colon ("ask ai: hello" runs Ask AI with "hello"). Only for `acceptsText` commands. */
  arg?: string;
  source: RunSource;
}

export interface Command {
  /** Unique and stable: recents are stored by id. */
  id: string;
  title: string;
  group: CommandGroup;
  icon?: IconName;
  /** Dimmed text after the title (the section of a page, the kind of an item). */
  subtitle?: string;
  /** Extra words that match the command but are not shown. */
  keywords?: readonly string[];
  /**
   * The keyboard shortcut: keys separated by spaces are a sequence ("g i"),
   * `mod+<key>` is Cmd (macOS) or Ctrl with the key ("mod+k").
   */
  shortcut?: string;
  /** Why the command cannot run now. Set: it is listed disabled with this reason. */
  disabled?: string;
  confirm?: CommandConfirm;
  /** Takes the text typed after a colon as its argument. */
  acceptsText?: boolean;
  /** Listed only once something is typed (jump-to-item commands). */
  queryOnly?: boolean;
  run: (ctx: RunContext) => void;
}

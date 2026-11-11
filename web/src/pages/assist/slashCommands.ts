export interface SlashCommand {
  name: string;
  description: string;
}

/** Typed in the composer instead of a message. Both start a fresh general chat. */
export const SLASH_COMMANDS: readonly SlashCommand[] = [
  { name: "/new", description: "Start a new chat" },
  {
    name: "/clear",
    description: "Start a new chat (the current one keeps its history)",
  },
];

/** The command `text` is, when it is exactly one (surrounding space ignored); null for a message. */
export function parseSlashCommand(text: string): SlashCommand | null {
  const t = text.trim();
  return SLASH_COMMANDS.find((c) => c.name === t) ?? null;
}

/** Commands to hint at while the first line starts with `/` and has no space yet. */
export function slashHints(text: string): readonly SlashCommand[] {
  if (!text.startsWith("/") || /\s/.test(text)) return [];
  return SLASH_COMMANDS.filter((c) => c.name.startsWith(text.toLowerCase()));
}

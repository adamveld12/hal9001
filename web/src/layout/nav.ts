// The sidebar's pages. The sidebar renders this list and the command
// registry derives its "Go to" commands and `g <key>` shortcuts from it, so a
// page added here appears in both.

import type { IconName } from "../components/icons";
import type { Command } from "../lib/commands/types";

export interface NavItem {
  to: string;
  label: string;
  icon: IconName;
  /** second key of the `g <key>` go-to shortcut, if any */
  key?: string;
}

export interface NavSection {
  label: string;
  items: NavItem[];
}

export const NAV_SECTIONS: NavSection[] = [
  {
    label: "Notify",
    items: [
      { to: "/inbox", label: "Inbox", icon: "inbox", key: "i" },
      { to: "/approvals", label: "Approvals", icon: "approvals", key: "a" },
    ],
  },
  {
    label: "Automate",
    items: [
      { to: "/sensors", label: "Sensors", icon: "sensors", key: "s" },
      { to: "/flows", label: "Flows", icon: "flows", key: "f" },
      { to: "/prompts", label: "Prompts", icon: "prompts", key: "p" },
      { to: "/chat", label: "Chat", icon: "chat", key: "h" },
    ],
  },
  {
    label: "Observe",
    items: [
      { to: "/timeline", label: "Timeline", icon: "timeline", key: "t" },
      { to: "/agent-runs", label: "Agent runs", icon: "agent", key: "r" },
    ],
  },
  {
    label: "Configure",
    items: [
      {
        to: "/settings/general",
        label: "General",
        icon: "settings",
        key: ",",
      },
      {
        to: "/settings/context",
        label: "Context",
        icon: "prompts",
        key: "c",
      },
      {
        to: "/settings/environment",
        label: "Environment",
        icon: "env",
        key: "e",
      },
      {
        to: "/settings/runtimes",
        label: "Runtimes",
        icon: "command",
        key: "m",
      },
      { to: "/tools", label: "Tools", icon: "command", key: "o" },
      { to: "/sandboxes", label: "Sandboxes", icon: "box", key: "b" },
      { to: "/settings/auth", label: "Auth", icon: "lock", key: "u" },
      { to: "/settings/mcp", label: "MCP", icon: "code", key: "c" },
      { to: "/settings/updates", label: "Updates", icon: "refresh" },
    ],
  },
];

const SETTINGS = "/settings/";

/** One "Go to" command per page of the sidebar, with the page's `g <key>` shortcut. */
export function navCommands(
  sections: readonly NavSection[],
  go: (to: string) => void,
): Command[] {
  return sections.flatMap((section) =>
    section.items.map((item): Command => {
      const settings = item.to.startsWith(SETTINGS);
      return {
        id: `go:${item.to}`,
        title: settings
          ? `Go to Settings: ${item.label}`
          : `Go to ${item.label}`,
        group: "Go to",
        icon: item.icon,
        subtitle: section.label,
        keywords: settings ? ["settings", item.label] : [item.label],
        shortcut: item.key ? `g ${item.key}` : undefined,
        run: () => go(item.to),
      };
    }),
  );
}

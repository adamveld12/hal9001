/**
 * Page title for a route, for a page that publishes no context of its own.
 * Keep in step with the routes in App.tsx and the nav labels in
 * layout/AppShell.tsx.
 */
const TITLES: readonly (readonly [RegExp, string])[] = [
  [/^\/inbox\/approval\/[^/]+$/, "Approval"],
  [/^\/inbox\/[^/]+$/, "Inbox item"],
  [/^\/inbox$/, "Inbox"],
  [/^\/approvals$/, "Approvals"],
  [/^\/timeline$/, "Timeline"],
  [/^\/events\/[^/]+$/, "Event"],
  [/^\/runs\/[^/]+$/, "Flow run"],
  [/^\/agent-runs$/, "Agent runs"],
  [/^\/agent-runs\/[^/]+$/, "Agent run"],
  [/^\/flows$/, "Flows"],
  [/^\/flows\/[^/]+\/versions\/[^/]+$/, "Flow version"],
  [/^\/flows\/[^/]+$/, "Flow"],
  [/^\/sensors$/, "Sensors"],
  [/^\/sensors\/[^/]+$/, "Sensor"],
  [/^\/tools$/, "Tools"],
  [/^\/tools\/[^/]+$/, "Tool"],
  [/^\/prompts$/, "Prompts"],
  [/^\/prompts\/[^/]+$/, "Prompt"],
  [/^\/settings\/general$/, "General settings"],
  [/^\/settings\/environment$/, "Environment"],
  [/^\/settings\/context$/, "Context"],
  [/^\/settings\/runtimes$/, "Runtimes"],
  [/^\/settings\/auth$/, "Auth"],
  [/^\/settings\/mcp$/, "MCP"],
  [/^\/settings\/tailscale$/, "Tailscale"],
  [/^\/settings\/updates$/, "Updates"],
  [/^\/sandboxes$/, "Sandboxes"],
  [/^\/sandboxes\/[^/]+$/, "Sandbox"],
];

/** The title for `pathname` (no query), or "hal9001" for a path the app does not know. */
export function defaultTitle(pathname: string): string {
  const path = pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
  return TITLES.find(([re]) => re.test(path))?.[1] ?? "hal9001";
}

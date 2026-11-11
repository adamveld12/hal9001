import { Fragment, useCallback, useState } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router";
import { useFaviconActivity } from "./faviconActivity";
import { eyeLabel, eyeThrobs } from "./halEye";
import { useStatus } from "../api/status";
import { useStreamState } from "../api/streamHooks";
import { GithubMark, Icon } from "../components/icons";
import { ExternalLink } from "../components/ExternalLink";
import { Badge, Kbd } from "../components/ui";
import { cx } from "../lib/cx";
import { isDark, setTheme, useTheme } from "../lib/theme";
import { AssistDrawer } from "../pages/assist/AssistDrawer";
import { AssistProvider } from "../pages/assist/AssistProvider";
import { useAssist } from "../pages/assist/context";
import { isChatPath } from "../pages/chat/path";
import { useTrackOrigin } from "../pages/chat/useTrackOrigin";
import { CommandProvider } from "./commands/CommandProvider";
import { useAppCommands } from "./commands/useAppCommands";
import { KillSwitch } from "./KillSwitch";
import { NAV_SECTIONS } from "./nav";
import { MAC } from "./platform";
import { PageContextProvider } from "./PageContextProvider";
import { UpdateBanner } from "./UpdateBanner";

/** The modifier of the Ask AI shortcut on this platform. */
const MOD_KEY = MAC ? "⌘" : "Ctrl";

const INBOX_BADGE_TITLE = "Unread notes plus pending approvals";

/** The second keys of the `g <key>` shortcuts, in sidebar order; a key a page lost to an earlier one is left out. */
const GO_KEYS = [
  ...new Set(
    NAV_SECTIONS.flatMap((s) => s.items).flatMap((n) => (n.key ? [n.key] : [])),
  ),
];

const NAV_COLLAPSED_ITEM = "hal9001.nav.collapsed";

/** Whether the left nav is an icon rail; persisted. The `[` shortcut is the "Toggle sidebar" command. */
function useNavCollapsed(): [boolean, () => void] {
  const [collapsed, setCollapsed] = useState(() => {
    try {
      return localStorage.getItem(NAV_COLLAPSED_ITEM) === "1";
    } catch {
      return false;
    }
  });
  const toggle = useCallback(() => {
    setCollapsed((c) => {
      try {
        localStorage.setItem(NAV_COLLAPSED_ITEM, c ? "0" : "1");
      } catch {
        // not persisted
      }
      return !c;
    });
  }, []);
  return [collapsed, toggle];
}

function Count({
  n,
  tone,
}: {
  n: number | undefined;
  tone: "bad" | "info" | "warn";
}) {
  if (!n) return null;
  return (
    <Badge tone={tone} className="ml-auto tabular-nums">
      {n > 99 ? "99+" : n}
    </Badge>
  );
}

function Shell() {
  useTrackOrigin();
  const assist = useAssist();
  const onChatPage = isChatPath(useLocation().pathname);
  const [collapsed, toggle] = useNavCollapsed();
  const { askAi } = useAppCommands({ collapsed, toggle });
  const status = useStatus();
  const stream = useStreamState();
  const theme = useTheme();
  const s = status.data;
  useFaviconActivity(s?.running_flow_runs);
  const inboxCount =
    s === undefined ? undefined : s.unread_inbox + s.pending_approvals;
  const running = (s?.running_flow_runs ?? 0) + (s?.running_agent_runs ?? 0);
  const queued = s?.queue_total ?? 0;

  return (
    <div className="flex h-full">
      <nav
        aria-label="Main"
        className={cx(
          "flex shrink-0 flex-col border-r border-line bg-panel",
          collapsed ? "w-14" : "w-52",
        )}
      >
        <div
          className={cx(
            "flex h-12 items-center gap-2 border-b border-line font-semibold tracking-wide",
            collapsed ? "justify-center" : "px-4",
          )}
        >
          <span
            className={cx(
              "size-2.5 shrink-0 rounded-full bg-bad shadow-[0_0_8px_var(--bad)]",
              eyeThrobs(s?.running_flow_runs) && "hal-eye-throb",
            )}
            title={eyeLabel(s?.running_flow_runs)}
            aria-label={eyeLabel(s?.running_flow_runs)}
            role="img"
          />
          {!collapsed && "hal9001"}
        </div>
        <div className="flex-1 overflow-y-auto p-2">
          {NAV_SECTIONS.map((section, si) => (
            <div
              key={section.label}
              role="group"
              aria-label={section.label}
              className={si > 0 ? (collapsed ? "mt-2" : "mt-3") : undefined}
            >
              {collapsed ? (
                si > 0 && <hr className="mx-1 mb-2 border-t border-line" />
              ) : (
                <div className="mb-1 px-2 text-[10px] font-medium uppercase tracking-wider text-muted">
                  {section.label}
                </div>
              )}
              <ul className="space-y-0.5">
                {section.items.map((n) => {
                  const count =
                    n.to === "/inbox"
                      ? inboxCount
                      : n.to === "/approvals"
                        ? s?.pending_approvals
                        : undefined;
                  const tone = n.to === "/inbox" ? "info" : "warn";
                  return (
                    <li key={n.to}>
                      <NavLink
                        to={n.to}
                        title={
                          n.to === "/inbox"
                            ? `Inbox (g i): ${INBOX_BADGE_TITLE}`
                            : n.key
                              ? `${n.label} (g ${n.key})`
                              : n.label
                        }
                        aria-label={collapsed ? n.label : undefined}
                        className={({ isActive }) =>
                          cx(
                            "relative flex items-center gap-2 rounded-md py-1.5 text-[13px]",
                            collapsed ? "justify-center px-0" : "px-2",
                            isActive
                              ? "bg-raised font-medium"
                              : "text-muted hover:bg-raised hover:text-fg",
                          )
                        }
                      >
                        <Icon name={n.icon} size={15} />
                        {!collapsed && n.label}
                        {collapsed ? (
                          count ? (
                            <span
                              className={cx(
                                "absolute right-1 top-0.5 min-w-4 rounded-full px-1 text-center text-[10px] leading-4 tabular-nums text-white",
                                tone === "info" ? "bg-info" : "bg-warn",
                              )}
                            >
                              {count > 99 ? "99+" : count}
                            </span>
                          ) : null
                        ) : (
                          <Count n={count} tone={tone} />
                        )}
                      </NavLink>
                    </li>
                  );
                })}
              </ul>
            </div>
          ))}
        </div>
        <div
          className={cx(
            "flex items-center border-t border-line p-2 text-[11px] text-muted",
            collapsed ? "justify-center" : "gap-2",
          )}
        >
          <button
            type="button"
            aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            aria-expanded={!collapsed}
            title={`${collapsed ? "Expand" : "Collapse"} sidebar ([)`}
            className="rounded-md p-1.5 hover:bg-raised hover:text-fg"
            onClick={toggle}
          >
            <Icon name="sidebar" size={15} />
          </button>
          {!collapsed && (
            <div className="space-y-1">
              <div>
                <Kbd>g</Kbd> then{" "}
                {GO_KEYS.map((k) => (
                  <Fragment key={k}>
                    <Kbd>{k}</Kbd>{" "}
                  </Fragment>
                ))}
                to jump
              </div>
              <div>
                <Kbd>{MOD_KEY} P</Kbd> command palette
              </div>
            </div>
          )}
        </div>
      </nav>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-12 shrink-0 items-center gap-4 border-b border-line bg-panel px-4">
          <KillSwitch status={s} />
          <div
            className="flex items-center gap-3 text-xs text-muted"
            aria-label="Activity"
          >
            <span title="Running flow and agent runs">
              <span className="font-medium tabular-nums text-fg">
                {running}
              </span>{" "}
              running
            </span>
            <Link
              to="/flows?view=queue"
              title="Queued flow and agent runs: open the queue"
              className="hover:text-fg"
            >
              <span className="font-medium tabular-nums text-fg">{queued}</span>{" "}
              queued
            </Link>
          </div>
          <div className="ml-auto flex items-center gap-3">
            <button
              type="button"
              aria-pressed={assist.open && !onChatPage}
              title={
                onChatPage
                  ? `Message box (${MOD_KEY} K)`
                  : `Ask AI (${MOD_KEY} K)`
              }
              className={cx(
                "inline-flex h-7 items-center gap-1.5 rounded-md border px-2 text-xs font-medium",
                assist.open && !onChatPage
                  ? "border-accent/50 bg-accent/10 text-accent"
                  : "border-line hover:bg-raised",
              )}
              onClick={() => askAi({ shortcut: true })}
            >
              <Icon name="agent" size={14} />
              Ask AI
              <Kbd>{MOD_KEY} K</Kbd>
            </button>
            <NavLink
              to="/approvals"
              className="inline-flex items-center gap-1.5 text-xs"
              title="Pending approvals"
            >
              <Icon name="approvals" size={14} />
              Approvals
              <Badge
                tone={s?.pending_approvals ? "warn" : "muted"}
                className="tabular-nums"
              >
                {s?.pending_approvals ?? 0}
              </Badge>
            </NavLink>
            <NavLink
              to="/inbox"
              className="inline-flex items-center gap-1.5 text-xs"
              title={INBOX_BADGE_TITLE}
            >
              <Icon name="inbox" size={14} />
              Inbox
              <Badge
                tone={inboxCount ? "info" : "muted"}
                className="tabular-nums"
              >
                {inboxCount ?? 0}
              </Badge>
            </NavLink>
            <span
              className="inline-flex items-center gap-1.5 text-[11px] text-muted"
              title={
                stream === "open"
                  ? "Live updates connected"
                  : "Live updates reconnecting"
              }
            >
              <span
                className={cx(
                  "size-1.5 rounded-full",
                  stream === "open" ? "bg-ok" : "bg-warn",
                )}
              />
              {stream === "open" ? "live" : "offline"}
            </span>
            <button
              type="button"
              aria-label="Toggle theme"
              title="Toggle light/dark"
              className="rounded-md p-1.5 text-muted hover:bg-raised hover:text-fg"
              onClick={() => setTheme(isDark(theme) ? "light" : "dark")}
            >
              <Icon name={isDark(theme) ? "sun" : "moon"} size={15} />
            </button>
            <ExternalLink
              href="https://github.com/adamveld12/hal9001"
              aria-label="hal9001 on GitHub"
              title="hal9001 on GitHub"
              className="rounded-md p-1.5 text-muted hover:bg-raised hover:text-fg"
            >
              <GithubMark size={15} />
            </ExternalLink>
          </div>
        </header>
        {status.isError && (
          <div
            role="alert"
            className="border-b border-bad/40 bg-bad/10 px-4 py-1.5 text-xs text-bad"
          >
            Cannot reach the daemon: {status.error.message}
          </div>
        )}
        <div className="flex min-h-0 flex-1">
          <main className="min-h-0 min-w-0 flex-1 overflow-auto">
            <UpdateBanner />
            <Outlet />
          </main>
          {assist.open && !onChatPage && <AssistDrawer />}
        </div>
      </div>
    </div>
  );
}

export function AppShell() {
  return (
    <PageContextProvider>
      <AssistProvider>
        <CommandProvider>
          <Shell />
        </CommandProvider>
      </AssistProvider>
    </PageContextProvider>
  );
}

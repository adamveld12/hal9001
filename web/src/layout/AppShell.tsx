import { useCallback, useEffect, useRef, useState } from "react";
import { Link, NavLink, Outlet, useNavigate } from "react-router";
import { eyeLabel, eyeThrobs } from "./halEye";
import { useStatus } from "../api/status";
import { useStreamState } from "../api/streamHooks";
import { Icon, type IconName } from "../components/icons";
import { Badge, Kbd } from "../components/ui";
import { cx } from "../lib/cx";
import { isDark, setTheme, useTheme } from "../lib/theme";
import { KillSwitch } from "./KillSwitch";

interface NavItem {
  to: string;
  label: string;
  icon: IconName;
  /** second key of the `g <key>` go-to shortcut, if any */
  key?: string;
}

interface NavSection {
  label: string;
  items: NavItem[];
}

const INBOX_BADGE_TITLE = "Unread notes plus pending approvals";

const NAV_SECTIONS: NavSection[] = [
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
    ],
  },
];

const NAV: NavItem[] = NAV_SECTIONS.flatMap((s) => s.items);

/** `g` then a letter jumps to a page (Linear style); ignored while typing. */
function useGoToShortcuts() {
  const navigate = useNavigate();
  const armed = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      const t = e.target;
      if (
        t instanceof HTMLElement &&
        (t.isContentEditable ||
          t.closest(".cm-editor") ||
          t.closest("[aria-label='AI assistant']") ||
          /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))
      ) {
        return;
      }
      if (armed.current) {
        clearTimeout(armed.current);
        armed.current = undefined;
        const item = NAV.find((n) => n.key === e.key);
        if (item) {
          e.preventDefault();
          void navigate(item.to);
        }
      } else if (e.key === "g") {
        armed.current = setTimeout(() => (armed.current = undefined), 1200);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [navigate]);
}
const NAV_COLLAPSED_ITEM = "hal9001.nav.collapsed";

/** Whether the left nav is an icon rail; persisted, toggled with `[` (ignored while typing). */
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
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "[" || e.metaKey || e.ctrlKey || e.altKey) return;
      const t = e.target;
      if (
        t instanceof HTMLElement &&
        (t.isContentEditable ||
          t.closest(".cm-editor") ||
          t.closest("[aria-label='AI assistant']") ||
          /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))
      ) {
        return;
      }
      e.preventDefault();
      toggle();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [toggle]);
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

export function AppShell() {
  useGoToShortcuts();
  const [collapsed, toggle] = useNavCollapsed();
  const status = useStatus();
  const stream = useStreamState();
  const theme = useTheme();
  const s = status.data;
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
            <span>
              <Kbd>g</Kbd> then <Kbd>i</Kbd> <Kbd>a</Kbd> <Kbd>s</Kbd>{" "}
              <Kbd>f</Kbd> <Kbd>p</Kbd> <Kbd>t</Kbd> <Kbd>r</Kbd> <Kbd>,</Kbd>{" "}
              <Kbd>e</Kbd> <Kbd>m</Kbd> <Kbd>b</Kbd> <Kbd>u</Kbd> to jump
            </span>
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
        <main className="min-h-0 flex-1 overflow-auto">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

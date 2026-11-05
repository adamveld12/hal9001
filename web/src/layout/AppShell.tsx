import { useEffect, useRef } from "react";
import { NavLink, Outlet, useNavigate } from "react-router";
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
  /** second key of the `g <key>` go-to shortcut */
  key: string;
}

const NAV: NavItem[] = [
  { to: "/timeline", label: "Timeline", icon: "timeline", key: "t" },
  { to: "/inbox", label: "Inbox", icon: "inbox", key: "i" },
  { to: "/approvals", label: "Approvals", icon: "approvals", key: "a" },
  { to: "/flows", label: "Flows", icon: "flows", key: "f" },
  { to: "/sensors", label: "Sensors", icon: "sensors", key: "s" },
  { to: "/prompts", label: "Prompts", icon: "prompts", key: "p" },
  { to: "/settings", label: "Settings", icon: "settings", key: "," },
];

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
  const status = useStatus();
  const stream = useStreamState();
  const theme = useTheme();
  const s = status.data;
  const running = (s?.running_flow_runs ?? 0) + (s?.running_agent_runs ?? 0);
  const queued = (s?.queued_flow_runs ?? 0) + (s?.queued_agent_runs ?? 0);

  return (
    <div className="flex h-full">
      <nav
        aria-label="Main"
        className="flex w-52 shrink-0 flex-col border-r border-line bg-panel"
      >
        <div className="flex h-12 items-center gap-2 border-b border-line px-4 font-semibold tracking-wide">
          <span className="size-2.5 rounded-full bg-bad shadow-[0_0_8px_var(--bad)]" />
          hal9001
        </div>
        <ul className="flex-1 space-y-0.5 p-2">
          {NAV.map((n) => (
            <li key={n.to}>
              <NavLink
                to={n.to}
                title={`Go to ${n.label} (g ${n.key})`}
                className={({ isActive }) =>
                  cx(
                    "flex items-center gap-2 rounded-md px-2 py-1.5 text-[13px]",
                    isActive
                      ? "bg-raised font-medium"
                      : "text-muted hover:bg-raised hover:text-fg",
                  )
                }
              >
                <Icon name={n.icon} size={15} />
                {n.label}
                {n.to === "/inbox" && <Count n={s?.unread_inbox} tone="info" />}
                {n.to === "/approvals" && (
                  <Count n={s?.pending_approvals} tone="warn" />
                )}
              </NavLink>
            </li>
          ))}
        </ul>
        <div className="border-t border-line p-3 text-[11px] text-muted">
          <Kbd>g</Kbd> then <Kbd>t</Kbd> <Kbd>i</Kbd> <Kbd>a</Kbd> <Kbd>f</Kbd>{" "}
          <Kbd>s</Kbd> <Kbd>p</Kbd> to jump
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
            <span title="Queued flow and agent runs">
              <span className="font-medium tabular-nums text-fg">{queued}</span>{" "}
              queued
            </span>
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
              title="Unread inbox items"
            >
              <Icon name="inbox" size={14} />
              Inbox
              <Badge
                tone={s?.unread_inbox ? "info" : "muted"}
                className="tabular-nums"
              >
                {s?.unread_inbox ?? 0}
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

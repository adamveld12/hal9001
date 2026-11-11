// The app's own commands: create, chat and actions. Each builder takes the
// state it depends on and the callbacks that do the work, and returns commands
// with their disabled reasons and confirmations decided; the hook that calls
// them is useAppCommands.ts. Pure.

import type { SensorSummary } from "../../api/sensors";
import type { Status } from "../../api/status";
import type { UpdateStatus } from "../../api/update";
import type { Command } from "../../lib/commands/types";

export const plural = (n: number, one: string, many = `${one}s`): string =>
  `${n} ${n === 1 ? one : many}`;

export interface CreateDeps {
  go: (path: string) => void;
  /** Show the drawer's start view for a new flow or sensor and send `text` as the first message. */
  draftWithAi: (kind: "flow" | "sensor", text: string | undefined) => void;
}

export function createCommands({ go, draftWithAi }: CreateDeps): Command[] {
  return [
    {
      id: "create:flow",
      title: "New flow",
      group: "Create",
      icon: "plus",
      subtitle: "Blank, not saved until you name it",
      keywords: ["create flow"],
      run: () => go("/flows?new=1"),
    },
    {
      id: "create:sensor",
      title: "New sensor",
      group: "Create",
      icon: "plus",
      subtitle: "Blank, not saved until you create it",
      keywords: ["create sensor"],
      run: () => go("/sensors?new=1"),
    },
    {
      id: "create:flow-ai",
      title: "New flow with AI…",
      group: "Create",
      icon: "agent",
      acceptsText: true,
      keywords: ["create flow ai draft"],
      run: ({ arg }) => draftWithAi("flow", arg),
    },
    {
      id: "create:sensor-ai",
      title: "New sensor with AI…",
      group: "Create",
      icon: "agent",
      acceptsText: true,
      keywords: ["create sensor ai draft"],
      run: ({ arg }) => draftWithAi("sensor", arg),
    },
  ];
}

export interface ChatDeps {
  onChatPage: boolean;
  /** Ask AI from the palette or the shortcut; `text` is sent as a message. */
  askAi: (opts: { text?: string; shortcut: boolean }) => void;
  newChat: () => void;
  openChatPage: () => void;
}

export function chatCommands(d: ChatDeps): Command[] {
  return [
    {
      id: "chat:ask",
      title: "Ask AI…",
      group: "Chat",
      icon: "agent",
      shortcut: "mod+k",
      acceptsText: true,
      keywords: ["assistant", "chat"],
      run: ({ arg, source }) =>
        d.askAi({ text: arg, shortcut: source === "shortcut" }),
    },
    {
      id: "chat:new",
      title: "New chat",
      group: "Chat",
      icon: "chat",
      run: () => d.newChat(),
    },
    {
      id: "chat:page",
      title: "Open full chat page",
      group: "Chat",
      icon: "chat",
      disabled: d.onChatPage ? "Already on the chat page" : undefined,
      keywords: ["conversations"],
      run: () => d.openChatPage(),
    },
  ];
}

export interface KillSwitchDeps {
  stopRunning: () => void;
  release: () => void;
}

/** "Stop all running flows" (the kill switch that also kills running work) and releasing it. */
export function killSwitchCommands(
  status: Status | undefined,
  d: KillSwitchDeps,
): Command[] {
  const paused = status?.kill_switch.paused === true;
  const flows = status?.running_flow_runs ?? 0;
  const agents = status?.running_agent_runs ?? 0;
  let stopDisabled: string | undefined;
  if (status === undefined) stopDisabled = "Status is not loaded yet";
  else if (paused) stopDisabled = "The kill switch is already engaged";
  else if (flows === 0) stopDisabled = "No flows are running";
  const affects =
    `Stops ${plural(flows, "running flow")}` +
    (agents > 0 ? ` and ${plural(agents, "running agent run")}` : "");
  return [
    {
      id: "action:kill-switch",
      title: "Stop all running flows",
      group: "Actions",
      icon: "stop",
      subtitle: "Kill switch",
      keywords: ["kill", "pause", "halt", "emergency"],
      disabled: stopDisabled,
      confirm: {
        title: "Stop all running flows?",
        message: `${affects}. This engages the kill switch: sensors stop polling and no new nodes start until you resume.`,
        confirmLabel: "Stop all",
      },
      run: d.stopRunning,
    },
    {
      id: "action:kill-switch-release",
      title: "Resume after kill switch",
      group: "Actions",
      icon: "play",
      subtitle: "Release the kill switch",
      keywords: ["unpause", "release"],
      disabled: !paused
        ? status === undefined
          ? "Status is not loaded yet"
          : "The kill switch is not engaged"
        : undefined,
      run: d.release,
    },
  ];
}

export interface SensorBulkDeps {
  pause: (ids: string[]) => void;
  resume: (ids: string[]) => void;
}

/** Pause or resume the scheduled polls of every sensor that is not (or is) paused. */
export function sensorBulkCommands(
  sensors: readonly SensorSummary[] | undefined,
  d: SensorBulkDeps,
): Command[] {
  const pausable = (sensors ?? [])
    .filter((s) => !s.status.paused && s.status.state !== "invalid")
    .map((s) => s.id);
  const paused = (sensors ?? [])
    .filter((s) => s.status.paused)
    .map((s) => s.id);
  let pauseDisabled: string | undefined;
  if (sensors === undefined) pauseDisabled = "Sensors are not loaded yet";
  else if (pausable.length === 0) {
    pauseDisabled =
      sensors.length === 0 ? "There are no sensors" : "No sensor is polling";
  }
  let resumeDisabled: string | undefined;
  if (sensors === undefined) resumeDisabled = "Sensors are not loaded yet";
  else if (paused.length === 0) resumeDisabled = "No sensor is paused";
  return [
    {
      id: "action:sensors-pause",
      title: "Pause all sensors",
      group: "Actions",
      icon: "pause",
      keywords: ["stop polling"],
      disabled: pauseDisabled,
      confirm: {
        title: "Pause all sensors?",
        message: `Stops the scheduled polls of ${plural(pausable.length, "sensor")}, so no new events arrive from them. Run now and event injection still work.`,
        confirmLabel: "Pause all",
      },
      run: () => d.pause(pausable),
    },
    {
      id: "action:sensors-resume",
      title: "Resume all sensors",
      group: "Actions",
      icon: "play",
      keywords: ["start polling", "unpause"],
      disabled: resumeDisabled,
      run: () => d.resume(paused),
    },
  ];
}

export interface ShellActionDeps {
  collapsed: boolean;
  dark: boolean;
  toggleSidebar: () => void;
  toggleTheme: () => void;
  update: UpdateStatus | undefined;
  checkForUpdates: () => void;
}

export function shellActionCommands(d: ShellActionDeps): Command[] {
  const updatesOff = d.update?.enabled === false;
  return [
    {
      id: "action:sidebar",
      title: "Toggle sidebar",
      group: "Actions",
      icon: "sidebar",
      subtitle: d.collapsed ? "Collapsed" : "Expanded",
      shortcut: "[",
      keywords: ["collapse", "expand", "navigation"],
      run: d.toggleSidebar,
    },
    {
      id: "action:theme",
      title: "Toggle theme",
      group: "Actions",
      icon: d.dark ? "sun" : "moon",
      subtitle: d.dark ? "Dark, switch to light" : "Light, switch to dark",
      keywords: ["dark", "light", "appearance"],
      run: d.toggleTheme,
    },
    {
      id: "action:updates",
      title: "Check for updates",
      group: "Actions",
      icon: "refresh",
      keywords: ["upgrade", "version", "release"],
      disabled: updatesOff
        ? `Updates are off${d.update?.disabled_reason ? `: ${d.update.disabled_reason}` : ""}`
        : undefined,
      run: d.checkForUpdates,
    },
  ];
}

/** What the toast says after an update check. */
export function updateCheckMessage(s: UpdateStatus): {
  text: string;
  tone: "info" | "error";
} {
  if (!s.enabled) {
    return {
      text: `Updates are off${s.disabled_reason ? `: ${s.disabled_reason}` : ""}`,
      tone: "info",
    };
  }
  if (s.state === "error" && s.error) {
    return { text: `Update check failed: ${s.error}`, tone: "error" };
  }
  if (s.available) {
    return {
      text: `Update available: HAL9001 ${s.latest?.version ?? ""}`.trim(),
      tone: "info",
    };
  }
  return {
    text: `HAL9001 ${s.current.version} is up to date`,
    tone: "info",
  };
}

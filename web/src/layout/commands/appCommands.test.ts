import { describe, expect, it } from "vitest";
import type { SensorSummary } from "../../api/sensors";
import type { Status } from "../../api/status";
import type { UpdateStatus } from "../../api/update";
import { planRun } from "../../lib/commands/run";
import { search } from "../../lib/commands/search";
import type { Command } from "../../lib/commands/types";
import {
  chatCommands,
  createCommands,
  killSwitchCommands,
  sensorBulkCommands,
  shellActionCommands,
  updateCheckMessage,
} from "./appCommands";

const status = (over: Partial<Status> = {}, paused = false): Status =>
  ({
    kill_switch: {
      paused,
      kill_running: false,
      since: null,
      by: null,
      reason: null,
    },
    running_flow_runs: 3,
    running_agent_runs: 0,
    queued_flow_runs: 0,
    queued_agent_runs: 0,
    queue_total: 0,
    pending_approvals: 0,
    unread_inbox: 0,
    ...over,
  }) as Status;

const sensor = (id: string, paused: boolean, state = "idle"): SensorSummary =>
  ({ id, status: { paused, state } }) as unknown as SensorSummary;

const noop = () => undefined;
const byId = (list: Command[], id: string): Command => {
  const c = list.find((x) => x.id === id);
  if (!c) throw new Error(`no command ${id}`);
  return c;
};

describe("killSwitchCommands", () => {
  const deps = { stopRunning: noop, release: noop };

  it("confirms, and says what it stops", () => {
    const stop = byId(killSwitchCommands(status(), deps), "action:kill-switch");
    expect(stop.disabled).toBeUndefined();
    expect(stop.confirm?.message).toContain("Stops 3 running flows");
    expect(planRun(stop).kind).toBe("confirm");
  });

  it("counts running agent runs too and says one flow in the singular", () => {
    const stop = byId(
      killSwitchCommands(
        status({ running_flow_runs: 1, running_agent_runs: 2 }),
        deps,
      ),
      "action:kill-switch",
    );
    expect(stop.confirm?.message).toContain(
      "Stops 1 running flow and 2 running agent runs",
    );
  });

  it("is disabled with a reason when there is nothing to stop", () => {
    const list = (s: Status | undefined) => killSwitchCommands(s, deps);
    expect(
      byId(list(status({ running_flow_runs: 0 })), "action:kill-switch")
        .disabled,
    ).toBe("No flows are running");
    expect(byId(list(status({}, true)), "action:kill-switch").disabled).toBe(
      "The kill switch is already engaged",
    );
    expect(byId(list(undefined), "action:kill-switch").disabled).toBe(
      "Status is not loaded yet",
    );
    expect(
      planRun(
        byId(list(status({ running_flow_runs: 0 })), "action:kill-switch"),
      ).kind,
    ).toBe("skip");
  });

  it("offers the release only while the kill switch is engaged", () => {
    const id = "action:kill-switch-release";
    expect(byId(killSwitchCommands(status(), deps), id).disabled).toBe(
      "The kill switch is not engaged",
    );
    const engaged = byId(killSwitchCommands(status({}, true), deps), id);
    expect(engaged.disabled).toBeUndefined();
    expect(planRun(engaged).kind).toBe("run");
  });
});

describe("sensorBulkCommands", () => {
  it("pauses only sensors that poll, behind a confirmation that names how many", () => {
    const paused: string[][] = [];
    const list = sensorBulkCommands(
      [sensor("a", false), sensor("b", false), sensor("c", true)],
      { pause: (ids) => paused.push(ids), resume: noop },
    );
    const pause = byId(list, "action:sensors-pause");
    expect(pause.confirm?.message).toContain("2 sensors");
    expect(planRun(pause).kind).toBe("confirm");
    pause.run({ source: "palette" });
    expect(paused).toEqual([["a", "b"]]);
  });

  it("resumes the paused ones without asking", () => {
    const resumed: string[][] = [];
    const list = sensorBulkCommands([sensor("a", false), sensor("c", true)], {
      pause: noop,
      resume: (ids) => resumed.push(ids),
    });
    const resume = byId(list, "action:sensors-resume");
    expect(planRun(resume).kind).toBe("run");
    resume.run({ source: "palette" });
    expect(resumed).toEqual([["c"]]);
  });

  it("explains why a command is unavailable", () => {
    const d = { pause: noop, resume: noop };
    const none = sensorBulkCommands([], d);
    expect(byId(none, "action:sensors-pause").disabled).toBe(
      "There are no sensors",
    );
    expect(byId(none, "action:sensors-resume").disabled).toBe(
      "No sensor is paused",
    );
    const allPaused = sensorBulkCommands([sensor("a", true)], d);
    expect(byId(allPaused, "action:sensors-pause").disabled).toBe(
      "No sensor is polling",
    );
    expect(
      byId(sensorBulkCommands(undefined, d), "action:sensors-pause").disabled,
    ).toBe("Sensors are not loaded yet");
  });
});

describe("create and chat commands", () => {
  it("opens blank editors, and drafts with the typed text", () => {
    const went: string[] = [];
    const drafts: [string, string | undefined][] = [];
    const list = createCommands({
      go: (p) => went.push(p),
      draftWithAi: (k, t) => drafts.push([k, t]),
    });
    byId(list, "create:flow").run({ source: "palette" });
    byId(list, "create:sensor").run({ source: "palette" });
    expect(went).toEqual(["/flows?new=1", "/sensors?new=1"]);

    // "new sensor with ai: poll my rss feed" in the palette
    const hit = search("new sensor with ai: poll my rss feed", list, []);
    const top = hit.flat[0]?.command;
    expect(top?.id).toBe("create:sensor-ai");
    top?.run({ arg: hit.arg, source: "palette" });
    byId(list, "create:flow-ai").run({ source: "palette" });
    expect(drafts).toEqual([
      ["sensor", "poll my rss feed"],
      ["flow", undefined],
    ]);
  });

  it("sends the text after 'ask ai:' as the message, and ⌘K toggles", () => {
    const asked: { text?: string; shortcut: boolean }[] = [];
    const list = chatCommands({
      onChatPage: false,
      askAi: (o) => asked.push(o),
      newChat: noop,
      openChatPage: noop,
    });
    const hit = search("ask ai: hello", list, []);
    expect(hit.flat[0]?.command.id).toBe("chat:ask");
    hit.flat[0]?.command.run({ arg: hit.arg, source: "palette" });
    byId(list, "chat:ask").run({ source: "shortcut" });
    expect(asked).toEqual([
      { text: "hello", shortcut: false },
      { text: undefined, shortcut: true },
    ]);
    expect(byId(list, "chat:ask").shortcut).toBe("mod+k");
  });

  it("disables the full chat page on the chat page", () => {
    const on = chatCommands({
      onChatPage: true,
      askAi: noop,
      newChat: noop,
      openChatPage: noop,
    });
    expect(byId(on, "chat:page").disabled).toBe("Already on the chat page");
  });
});

describe("shell actions", () => {
  const update = (over: Partial<UpdateStatus>): UpdateStatus =>
    ({
      enabled: true,
      disabled_reason: null,
      current: { version: "1.2.0" },
      available: false,
      latest: null,
      state: "up_to_date",
      error: null,
      ...over,
    }) as unknown as UpdateStatus;

  const base = {
    collapsed: false,
    dark: false,
    toggleSidebar: noop,
    toggleTheme: noop,
    checkForUpdates: noop,
  };

  it("keeps the sidebar toggle on `[`", () => {
    const list = shellActionCommands({ ...base, update: update({}) });
    expect(byId(list, "action:sidebar").shortcut).toBe("[");
  });

  it("disables the update check when updates are off, with the reason", () => {
    const list = shellActionCommands({
      ...base,
      update: update({ enabled: false, disabled_reason: "dev build" }),
    });
    expect(byId(list, "action:updates").disabled).toBe(
      "Updates are off: dev build",
    );
  });

  it("reports an update check", () => {
    expect(updateCheckMessage(update({})).text).toBe(
      "HAL9001 1.2.0 is up to date",
    );
    expect(
      updateCheckMessage(update({ state: "error", error: "offline" })),
    ).toEqual({
      text: "Update check failed: offline",
      tone: "error",
    });
  });
});

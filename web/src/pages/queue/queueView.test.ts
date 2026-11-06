import { describe, expect, it } from "vitest";
import type { Queue, QueueItem } from "../../api/queue";
import {
  clearConfirmText,
  pruneSelection,
  removeNote,
  toggleAll,
  toggleSelected,
  waitingText,
} from "./queueView";

const limits: Queue["limits"] = {
  max_agent_runs: 2,
  max_command_runs: 8,
  agent_runs_in_use: 2,
  command_runs_in_use: 3,
  flows: [{ flow_id: "triage", max_concurrent: 1, running: 1 }],
};

function item(
  id: string,
  waiting_for: QueueItem["waiting_for"],
  flow = "triage",
): QueueItem {
  return {
    flow_run: { id, flow_id: flow } as QueueItem["flow_run"],
    position: 1,
    waiting_for,
    queued_at: "2026-11-05T12:00:00Z",
    waiting_node_ids: [],
  };
}

describe("waitingText", () => {
  it("names the pool and how full it is", () => {
    expect(waitingText(item("a", "agent_slot"), limits)).toBe(
      "waiting for an agent slot (2 of 2 in use)",
    );
    expect(waitingText(item("a", "command_slot"), limits)).toBe(
      "waiting for a command slot (3 of 8 in use)",
    );
    expect(waitingText(item("a", "flow_slot"), limits)).toBe(
      "waiting for a slot in triage (1 of 1 running)",
    );
    expect(waitingText(item("a", "flow_slot", "other"), limits)).toBe(
      "waiting for a flow slot",
    );
    expect(waitingText(item("a", "kill_switch"), limits)).toContain(
      "kill switch",
    );
  });
});

describe("selection", () => {
  const items = [item("a", "flow_slot"), item("b", "flow_slot")];

  it("toggles one id and all ids", () => {
    expect([...toggleSelected(new Set(["a"]), "b")]).toEqual(["a", "b"]);
    expect([...toggleSelected(new Set(["a"]), "a")]).toEqual([]);
    expect([...toggleAll(new Set(["a"]), items)]).toEqual(["a", "b"]);
    expect([...toggleAll(new Set(["a", "b"]), items)]).toEqual([]);
    expect([...toggleAll(new Set(), [])]).toEqual([]);
  });

  it("drops runs that left the queue", () => {
    expect([...pruneSelection(new Set(["a", "gone"]), items)]).toEqual(["a"]);
  });
});

describe("texts", () => {
  it("says runs are cancelled and kept in history", () => {
    expect(clearConfirmText(3, "")).toBe(
      "Remove 3 queued runs? They are cancelled and kept in history. Runs that already started are not affected.",
    );
    expect(clearConfirmText(1, "triage")).toContain("1 queued run of triage");
    expect(clearConfirmText(1, "triage")).toContain("It is cancelled");
  });

  it("reports runs that could not be removed", () => {
    expect(removeNote({ removed: ["a", "b"], failed: [] })).toBe(
      "Removed 2 runs from the queue.",
    );
    expect(
      removeNote({
        removed: ["a"],
        failed: [{ id: "b", message: "already running a node" }],
      }),
    ).toBe(
      "Removed 1 run from the queue. 1 could not be removed (already running a node).",
    );
  });
});

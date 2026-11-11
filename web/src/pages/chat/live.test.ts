import { describe, expect, it } from "vitest";
import type { StreamEvent } from "../../api/streamContext";
import { concernsConversation } from "./live";

const ev = (type: string, data: unknown) =>
  ({ type, id: "1", ts: "t", data }) as StreamEvent;

const run = { agentRunId: "A2", nodeRunId: "N1" };

describe("concernsConversation", () => {
  it("follows events appended to the head run and its approvals", () => {
    expect(
      concernsConversation(
        ev("agent_event.appended", { agent_run_id: "A2" }),
        run,
      ),
    ).toBe(true);
    expect(
      concernsConversation(ev("approval.created", { agent_run_id: "A2" }), run),
    ).toBe(true);
    expect(
      concernsConversation(ev("approval.updated", { agent_run_id: "A2" }), run),
    ).toBe(true);
  });

  it("ignores other runs", () => {
    expect(
      concernsConversation(
        ev("agent_event.appended", { agent_run_id: "A9" }),
        run,
      ),
    ).toBe(false);
    expect(
      concernsConversation(ev("approval.updated", { agent_run_id: null }), run),
    ).toBe(false);
  });

  it("follows the head run's status and a new phase of the same node run", () => {
    expect(
      concernsConversation(
        ev("agent_run.updated", { id: "A2", node_run_id: "N1" }),
        run,
      ),
    ).toBe(true);
    expect(
      concernsConversation(
        ev("agent_run.created", { id: "A3", node_run_id: "N1" }),
        run,
      ),
    ).toBe(true);
    expect(
      concernsConversation(
        ev("agent_run.updated", { id: "A9", node_run_id: "N9" }),
        run,
      ),
    ).toBe(false);
  });

  it("does nothing without a run or for other event types", () => {
    expect(
      concernsConversation(ev("agent_event.appended", { agent_run_id: "A2" }), {
        agentRunId: null,
        nodeRunId: null,
      }),
    ).toBe(false);
    expect(concernsConversation(ev("inbox.created", {}), run)).toBe(false);
  });
});

import { describe, expect, it } from "vitest";
import type { Conversation } from "../../api/conversations";
import {
  canContinue,
  conversationLinks,
  flowNodeLabel,
  forkCountLabel,
  forkOriginLabel,
  isLive,
  statusLabel,
  statusTone,
} from "./status";

describe("status helpers", () => {
  it("treats running and awaiting approval as live", () => {
    expect(isLive("running")).toBe(true);
    expect(isLive("awaiting_approval")).toBe(true);
    expect(isLive("idle")).toBe(false);
    expect(isLive("succeeded")).toBe(false);
  });

  it("labels and colours every status", () => {
    expect(statusLabel("awaiting_approval")).toBe("awaiting approval");
    expect(statusLabel("closed")).toBe("ended");
    expect(statusTone("running")).toBe("run");
    expect(statusTone("awaiting_approval")).toBe("warn");
    expect(statusTone("failed")).toBe("bad");
  });

  it("names the flow and node of a flow run", () => {
    expect(flowNodeLabel({ flow_id: "triage", node_id: "ask" })).toBe(
      "triage / ask",
    );
    expect(flowNodeLabel({ flow_id: "triage", node_id: null })).toBe("triage");
    expect(flowNodeLabel({ flow_id: null, node_id: null })).toBeNull();
  });

  it("allows Continue in chat for a finished flow conversation only", () => {
    const flow = (over: Partial<Conversation>) =>
      ({ source: "flow", continuable: true, running: false, ...over }) as Pick<
        Conversation,
        "source" | "continuable" | "running"
      >;
    expect(canContinue(flow({}))).toBe(true);
    expect(canContinue(flow({ running: true }))).toBe(false);
    expect(canContinue(flow({ continuable: false }))).toBe(false);
    expect(canContinue(flow({ source: "chat" }))).toBe(false);
  });

  it("words forks", () => {
    expect(forkCountLabel(0)).toBe("");
    expect(forkCountLabel(1)).toBe("1 fork");
    expect(forkCountLabel(4)).toBe("4 forks");
    expect(
      forkOriginLabel({
        agent_run_id: "01JC7Z3K9Q8V5T2M4N6P8R0S1T",
        flow_id: null,
        flow_run_id: null,
        node_id: null,
      }),
    ).toBe("Continued from run 6P8R0S1T");
  });
});

describe("conversationLinks", () => {
  const base = {
    flow_id: null,
    flow_run_id: null,
    node_id: null,
    agent_run_id: null,
    forked_from: null,
  };

  it("links a flow conversation to its run, node, agent run and flow", () => {
    expect(
      conversationLinks({
        ...base,
        flow_id: "triage",
        flow_run_id: "R1",
        node_id: "ask",
        agent_run_id: "A1",
      } as Conversation),
    ).toEqual([
      { label: "Flow run", to: "/runs/R1" },
      { label: "Node ask", to: "/runs/R1?node=ask" },
      { label: "Agent run", to: "/agent-runs/A1" },
      { label: "Flow triage", to: "/flows/triage" },
    ]);
  });

  it("links a fork to the run it continues first and takes the flow run from it", () => {
    const links = conversationLinks({
      ...base,
      flow_id: "triage",
      forked_from: {
        agent_run_id: "01JC7Z3K9Q8V5T2M4N6P8R0S1T",
        flow_id: "triage",
        flow_run_id: "R1",
        node_id: "ask",
      },
    } as Conversation);
    expect(links[0]).toEqual({
      label: "Continued from run 6P8R0S1T",
      to: "/agent-runs/01JC7Z3K9Q8V5T2M4N6P8R0S1T",
    });
    expect(links.map((l) => l.to)).toContain("/runs/R1?node=ask");
  });

  it("has no links for a chat that came from nothing", () => {
    expect(conversationLinks(base as Conversation)).toEqual([]);
  });
});

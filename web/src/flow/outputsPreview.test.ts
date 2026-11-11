import { describe, expect, it } from "vitest";
import { outputsSection } from "./outputsPreview";
import type { AgentNode, Flow } from "./model";

function agent(over: Partial<AgentNode> = {}): AgentNode {
  return {
    id: "a",
    type: "agent",
    runtime: "claude",
    mode: "readonly",
    instructions: "x",
    ports: {
      reviewed: { description: "Reviewed, no action." },
      needs_human: { description: "Needs a human." },
    },
    ...over,
  };
}

function flowOf(
  a: AgentNode,
  extra: Flow["nodes"],
  edges: Flow["edges"],
): Flow {
  return {
    id: "f",
    enabled: true,
    subscribe: [],
    nodes: [a, ...extra],
    edges,
  };
}

const inbox = (id: string, title: string) =>
  ({ id, type: "inbox", title }) as const;

describe("outputsSection", () => {
  it("lists two ports to inbox notes", () => {
    const f = flowOf(
      agent(),
      [
        inbox("i1", "Review: {{.input.id}}"),
        inbox("i2", "Needs your review: {{.input.id}}"),
      ],
      [
        { from: "a", port: "reviewed", to: "i1" },
        { from: "a", port: "needs_human", to: "i2" },
      ],
    );
    expect(outputsSection(f, f.nodes[0] as AgentNode)).toBe(
      [
        "## Outputs",
        "Finish by calling exactly one of these tools:",
        "- emit_reviewed: Reviewed, no action.",
        '  Leads to: inbox note "Review: ..."',
        "- emit_needs_human: Needs a human.",
        '  Leads to: inbox note "Needs your review: ..."',
        "Do not put the output in your final message; it is ignored.",
      ].join("\n"),
    );
  });

  it("uses the many header", () => {
    const a = agent({ ports: { r: { description: "d" } }, emit: "many" });
    const f = flowOf(a, [inbox("i", "T")], [{ from: "a", port: "r", to: "i" }]);
    expect(outputsSection(f, a)).toContain(
      "Finish by calling one or more of these tools (each call is a separate output):",
    );
  });

  it("describes approval and command targets, defaults the description, skips ports without edge", () => {
    const a = agent({
      ports: { go: {}, ask: { description: " Ask. " }, idle: {} },
    });
    const f = flowOf(
      a,
      [
        { id: "c", type: "command", lang: "bash", script: "cat" },
        {
          id: "ap",
          type: "approval",
          title: "Ok {{.input.x}}?",
          description: "d",
        },
      ],
      [
        { from: "a", port: "go", to: "c" },
        { from: "a", port: "ask", to: "ap" },
      ],
    );
    const s = outputsSection(f, a) ?? "";
    expect(s).toContain("- emit_go: Finish through the port go.");
    expect(s).toContain("  Leads to: command node");
    expect(s).toContain("- emit_ask: Ask.");
    expect(s).toContain('  Leads to: approval "Ok ...?"');
    expect(s).not.toContain("idle");
  });

  it("is undefined when no port has an edge", () => {
    const a = agent();
    expect(outputsSection(flowOf(a, [], []), a)).toBeUndefined();
  });
});

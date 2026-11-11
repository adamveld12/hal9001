import { describe, expect, it } from "vitest";
import {
  capPageContext,
  MAX_PAGE_CONTEXT_BYTES,
  contextBytes,
} from "../../lib/pageContext";
import {
  approvalLabel,
  flowContext,
  flowsListContext,
  MAX_LISTED,
  MAX_MESSAGES,
  sensorContext,
  sensorsListContext,
  toolContext,
} from "./pageContexts";

describe("sensorsListContext", () => {
  const rows = Array.from({ length: 40 }, (_, i) => ({
    id: `s${i}`,
    sensor: null,
    status: { state: i === 0 ? "failing" : "idle", paused: i === 1 },
  }));

  it("counts everything but names only the first rows", () => {
    const c = sensorsListContext(rows);
    expect(c.facts).toContainEqual({ label: "sensors", value: "40" });
    expect(c.facts).toContainEqual({ label: "failing", value: "1" });
    expect(c.entities).toHaveLength(MAX_LISTED);
    expect(c.entities?.[0]).toMatchObject({ id: "s0", detail: "failing" });
    expect(c.entities?.[1]?.detail).toBe("idle, paused");
  });

  it("fits the cap when sent", () => {
    const ctx = capPageContext("/sensors", sensorsListContext(rows));
    expect(contextBytes(ctx)).toBeLessThanOrEqual(MAX_PAGE_CONTEXT_BYTES);
  });
});

describe("sensorContext", () => {
  it("quotes a few problems, errors first, and flags unsaved changes", () => {
    const diagnostics = [
      ...Array.from({ length: 4 }, (_, i) => ({
        severity: "warning",
        message: `w${i}`,
      })),
      ...Array.from({ length: 4 }, (_, i) => ({
        severity: "error",
        message: `e${i}`,
      })),
    ];
    const c = sensorContext({
      id: "gh",
      state: "idle",
      paused: false,
      dirty: true,
      diagnostics,
      drafting: false,
    });
    const quoted = c.facts?.filter((f) => f.label === "problem") ?? [];
    expect(quoted).toHaveLength(MAX_MESSAGES);
    expect(quoted[0]?.value).toBe("error: e0");
    expect(c.facts).toContainEqual({ label: "problems", value: "8" });
    expect(c.facts).toContainEqual({ label: "unsaved changes", value: "yes" });
  });
});

describe("flow and tool contexts", () => {
  it("names the selected node of a flow", () => {
    const c = flowContext({
      id: "triage",
      enabled: true,
      nodeCount: 3,
      view: "visual",
      selected: { id: "ask", type: "agent" },
      diagnostics: [],
      dirty: false,
      drafting: false,
    });
    expect(c.facts).toContainEqual({
      label: "selected node",
      value: "ask (agent)",
    });
    expect(c.entities).toContainEqual({
      kind: "node",
      id: "ask",
      detail: "agent",
    });
  });

  it("lists parameter names, never their definitions, and the approval mode", () => {
    const c = toolContext({
      id: "fetch",
      paramNames: ["url", "limit"],
      approve: { rules: true },
      dirty: false,
      diagnostics: [],
      drafting: false,
    });
    expect(c.facts).toContainEqual({
      label: "parameters",
      value: "url, limit",
    });
    expect(c.facts).toContainEqual({ label: "approval", value: "rules" });
  });

  it("labels approval modes", () => {
    expect(approvalLabel("never")).toBe("never");
    expect(approvalLabel(undefined)).toBe("always");
    expect(approvalLabel({ tools: ["x"] })).toBe("rules");
  });

  it("describes the flows tab and the queue size", () => {
    const c = flowsListContext(
      [{ id: "a", enabled: true, dry_run: false, node_count: 2 }],
      "queue",
      4,
    );
    expect(c.facts).toContainEqual({ label: "queued runs", value: "4" });
    expect(c.entities?.[0]?.detail).toBe("enabled, 2 nodes");
  });
});

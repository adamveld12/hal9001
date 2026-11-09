import { describe, expect, it } from "vitest";
import type {
  AssistFlowTestNode,
  AssistFlowTestResult,
} from "../../api/assist";
import { ApiError } from "../../api/client";
import type { Flow } from "../../flow/model";
import {
  buildFlowTestRequest,
  describeTestFailure,
  INITIAL_TEST_INPUT,
  overlayFromTestResult,
  type TestInputState,
} from "./flowTest";

const flow: Flow = {
  id: "triage",
  enabled: false,
  subscribe: ["gh"],
  nodes: [
    {
      id: "filter",
      type: "command",
      lang: "jq",
      script: ".",
      ui: { x: 0, y: 0 },
    },
    {
      id: "ask",
      type: "agent",
      runtime: "claude",
      mode: "readonly",
      prompt: "p",
      ui: { x: 300, y: 0 },
    },
    { id: "inbox", type: "inbox", title: "t", ui: { x: 600, y: 0 } },
  ],
  edges: [
    { from: "filter", to: "ask" },
    { from: "filter", to: "inbox", port: "urgent" },
  ],
};

const tnode = (over: Partial<AssistFlowTestNode>): AssistFlowTestNode => ({
  node_id: "filter",
  node_run_id: "01",
  status: "done",
  decision: "emitted",
  reason: null,
  error: null,
  ports: ["urgent"],
  outputs: [{ port: "urgent", payload: { n: 1 } }],
  would_create: null,
  stdout: null,
  stderr: null,
  exit_code: 0,
  duration_ms: 3,
  ...over,
});

const result = (
  over: Partial<AssistFlowTestResult> = {},
): AssistFlowTestResult => ({
  kind: "flow",
  flow_run_id: "run1",
  status: "succeeded",
  input: {},
  nodes: [
    tnode({}),
    tnode({
      node_id: "inbox",
      node_run_id: "02",
      ports: [],
      outputs: [],
      would_create: {
        title: "t",
        priority: "urgent",
        summary_md: "",
        suggested_reply: "",
        links: [],
      },
    }),
  ],
  edges_taken: [{ from: "filter", to: "inbox", port: "urgent" }],
  ...over,
});

describe("buildFlowTestRequest", () => {
  it("passes the chosen sample, event or pasted JSON", () => {
    expect(
      buildFlowTestRequest({ ...INITIAL_TEST_INPUT, sampleIndex: "2" }),
    ).toEqual({ ok: true, body: { sample_index: 2 } });
    expect(
      buildFlowTestRequest({
        ...INITIAL_TEST_INPUT,
        source: "event",
        eventId: "e1",
      }),
    ).toEqual({ ok: true, body: { event_id: "e1" } });
    expect(
      buildFlowTestRequest({
        ...INITIAL_TEST_INPUT,
        source: "json",
        json: '{"a":[1]}',
      }),
    ).toEqual({ ok: true, body: { input: { a: [1] } } });
  });

  it("says what is missing or malformed", () => {
    expect(buildFlowTestRequest(INITIAL_TEST_INPUT)).toMatchObject({
      ok: false,
    });
    expect(
      buildFlowTestRequest({ ...INITIAL_TEST_INPUT, source: "event" }),
    ).toMatchObject({ ok: false });
    const bad = buildFlowTestRequest({
      ...INITIAL_TEST_INPUT,
      source: "json",
      json: "{",
    });
    expect(bad.ok).toBe(false);
    if (!bad.ok) expect(bad.error).toMatch(/Invalid JSON/);
  });

  it("runs a draft sensor and feeds one of its items to the flow", () => {
    const picked: TestInputState = {
      ...INITIAL_TEST_INPUT,
      source: "sensor",
      fromSensor: "gh",
    };
    expect(buildFlowTestRequest(picked)).toEqual({
      ok: true,
      body: { from_sensor: "gh", item: 0 },
    });
    expect(buildFlowTestRequest({ ...picked, item: "3" })).toEqual({
      ok: true,
      body: { from_sensor: "gh", item: 3 },
    });
  });

  it("asks for a draft sensor and a whole item number", () => {
    const picked: TestInputState = {
      ...INITIAL_TEST_INPUT,
      source: "sensor",
      fromSensor: "gh",
    };
    expect(buildFlowTestRequest({ ...picked, fromSensor: "" })).toMatchObject({
      ok: false,
    });
    for (const item of ["-1", "1.5", "two"]) {
      expect(buildFlowTestRequest({ ...picked, item })).toMatchObject({
        ok: false,
      });
    }
  });
});

describe("overlayFromTestResult", () => {
  it("marks the nodes that ran and the edges taken; the rest stay unmarked", () => {
    const o = overlayFromTestResult(flow, result());
    expect(o?.nodes.filter).toMatchObject({ visual: "emitted" });
    expect(o?.nodes.inbox?.visual).toBe("emitted");
    expect(o?.nodes.ask).toBeUndefined();
    expect([...(o?.takenEdges ?? [])]).toEqual(["filter|urgent|inbox"]);
  });

  it("shows a skipped agent node with the reason", () => {
    const r = result({
      nodes: [
        tnode({
          node_id: "ask",
          status: "skipped",
          decision: "skipped",
          reason: "assist test: agent nodes are not run",
          ports: [],
          outputs: [],
        }),
      ],
      edges_taken: [{ from: "filter", to: "ask", port: "out" }],
    });
    const o = overlayFromTestResult(flow, r);
    expect(o?.nodes.ask).toEqual({
      visual: "skipped",
      detail: "assist test: agent nodes are not run",
    });
  });

  it("shows a failed node as an error", () => {
    const o = overlayFromTestResult(
      flow,
      result({
        status: "failed",
        nodes: [
          tnode({
            status: "failed",
            decision: null,
            error: "exit 2",
            ports: [],
            outputs: [],
          }),
        ],
        edges_taken: [],
      }),
    );
    expect(o?.nodes.filter?.visual).toBe("error");
  });

  it("is dropped when the canvas no longer has the tested nodes or edges", () => {
    const without: Flow = {
      ...flow,
      nodes: flow.nodes.filter((n) => n.id !== "inbox"),
    };
    expect(overlayFromTestResult(without, result())).toBeUndefined();
    const rewired: Flow = { ...flow, edges: [] };
    expect(overlayFromTestResult(rewired, result())).toBeUndefined();
  });
});

describe("describeTestFailure", () => {
  const err = (status: number, type: string, extra = {}) =>
    new ApiError(
      status,
      { type: `urn:hal9001:problem:${type}`, detail: "why", ...extra } as never,
      "x",
    );

  it("lists the diagnostics of a draft that does not validate", () => {
    const f = describeTestFailure(
      err(422, "validation-failed", {
        diagnostics: [
          { severity: "error", message: "unknown sensor", node_id: "n" },
        ],
      }),
    );
    expect(f.message).toMatch(/does not validate/);
    expect(f.diagnostics).toHaveLength(1);
  });

  it("explains a 422 without findings as a missing input", () => {
    const f = describeTestFailure(
      err(422, "validation-failed", { detail: "no input available" }),
    );
    expect(f.message).toMatch(/no input available/);
  });

  it("names an approval that is needed and the kill switch", () => {
    expect(describeTestFailure(err(409, "invalid-state")).message).toMatch(
      /^Approval needed: why/,
    );
    expect(describeTestFailure(err(423, "paused")).message).toMatch(
      /kill switch/,
    );
  });
});

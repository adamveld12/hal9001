import { describe, expect, it } from "vitest";
import { discoverFields } from "./fields";
import {
  countTrail,
  freshStepCount,
  groupOutputs,
  parseInput,
  previewableSteps,
  sampleKeys,
  sampleLabel,
  toCommandNode,
} from "./preview";
import { fieldsAfterStep, fieldsBeforeEachStep } from "./shape";
import type { FlowSample, TransformNode, TransformStep } from "./types";

const filter: TransformStep = {
  type: "filter",
  conditions: [{ field: "a", op: "exists" }],
};
const incomplete: TransformStep = {
  type: "filter",
  conditions: [{ field: "", op: "exists" }],
};

describe("previewableSteps", () => {
  it("sends everything when no step is incomplete", () => {
    expect(previewableSteps([filter, filter])).toEqual({
      steps: [filter, filter],
      blockedAt: null,
    });
  });

  it("sends the steps before the first incomplete one", () => {
    expect(previewableSteps([filter, incomplete, filter])).toEqual({
      steps: [filter],
      blockedAt: 1,
    });
    expect(previewableSteps([incomplete])).toEqual({ steps: [], blockedAt: 0 });
  });
});

describe("preview presentation", () => {
  it("groups outputs by port in the node's port order", () => {
    const outputs = [
      { port: "b", payload: 1 },
      { port: "out", payload: 2 },
      { port: "b", payload: 3 },
      { port: "stray", payload: 4 },
    ];
    expect(groupOutputs(outputs, ["out", "a", "b"])).toEqual([
      { port: "out", payloads: [2] },
      { port: "b", payloads: [1, 3] },
      { port: "stray", payloads: [4] },
    ]);
  });

  it("lists the item count after each step, starting with the input", () => {
    expect(
      countTrail([
        {
          step: 0,
          description: "",
          outputs_count: 3,
          dropped: false,
          error: null,
        },
        {
          step: 1,
          description: "",
          outputs_count: 0,
          dropped: true,
          error: null,
        },
      ]),
    ).toEqual([1, 3, 0]);
  });

  it("knows how many leading steps an old answer still describes", () => {
    const input = { a: 1 };
    const answered = { steps: [filter, filter], input };
    expect(freshStepCount(answered, [filter, filter], input)).toBe(2);
    expect(freshStepCount(answered, [filter, incomplete], input)).toBe(1);
    expect(freshStepCount(answered, [incomplete, filter], input)).toBe(0);
    expect(freshStepCount(answered, [filter], input)).toBe(1);
    expect(freshStepCount(answered, [filter, filter], { a: 1 })).toBe(0);
    expect(freshStepCount(undefined, [filter], input)).toBe(0);
  });
});

describe("samples", () => {
  const event: FlowSample = {
    source: "event",
    event_id: "01J00000000000000000ABCDEF",
    node_run_id: null,
    from_node_id: null,
    sensor_id: "github",
    created_at: "2026-11-05T10:00:00Z",
    payload: { title: "Fix login" },
  };
  const run: FlowSample = {
    source: "node_run",
    event_id: null,
    node_run_id: "01J00000000000000000QRSTUV",
    from_node_id: "classify",
    sensor_id: null,
    created_at: "2026-11-05T09:00:00Z",
    payload: [1, 2],
  };

  it("labels a sample with its source, age and a payload snippet", () => {
    const now = Date.parse("2026-11-05T10:03:00Z");
    expect(sampleLabel(event, now)).toBe(
      'Event 00ABCDEF (github) · 3m ago · {"title":"Fix login"}',
    );
    expect(sampleLabel(run, now)).toBe(
      "classify output, run 00QRSTUV · 1h ago · [1,2]",
    );
  });

  it("keeps keys unique when one run emitted several payloads", () => {
    expect(sampleKeys([run, run, event])).toEqual([
      "01J00000000000000000QRSTUV#0",
      "01J00000000000000000QRSTUV#1",
      "01J00000000000000000ABCDEF#0",
    ]);
  });

  it("parses pasted JSON and reports a syntax error without throwing", () => {
    expect(parseInput("  ")).toBeNull();
    expect(parseInput('{"a": 1}')).toEqual({ ok: true, value: { a: 1 } });
    expect(parseInput("[1, 2]")).toEqual({ ok: true, value: [1, 2] });
    const bad = parseInput("{oops");
    expect(bad?.ok).toBe(false);
  });
});

describe("toCommandNode", () => {
  const route: TransformStep = {
    type: "route",
    rules: [{ port: "urgent", conditions: [{ field: "x", op: "exists" }] }],
    default_port: "normal",
  };

  it("keeps id, position and the transform's ports, without error", () => {
    const node: TransformNode = {
      id: "triage",
      type: "transform",
      steps: [route],
      ui: { x: 10, y: 20 },
    };
    expect(toCommandNode(node, ".")).toEqual({
      id: "triage",
      type: "command",
      lang: "jq",
      script: ".",
      ports: ["out", "urgent", "normal"],
      ui: { x: 10, y: 20 },
    });
    expect(Object.keys(toCommandNode(node, "."))).toEqual([
      "id",
      "type",
      "lang",
      "script",
      "ports",
      "ui",
    ]);
  });

  it("declares no ports when out is the only one, and keeps the timeout", () => {
    const out = toCommandNode(
      { id: "t", type: "transform", steps: [filter], timeout: "5s" },
      ".x",
    );
    expect(out).toEqual({
      id: "t",
      type: "command",
      lang: "jq",
      script: ".x",
      timeout: "5s",
    });
  });
});

describe("fields after earlier steps", () => {
  const input = discoverFields({
    title: "t",
    author: { login: "ada", id: 1 },
    labels: [{ name: "bug" }],
    tags: ["a"],
  });
  const paths = (f: { path: string }[]) => f.map((x) => x.path);

  it("adds the field a set step writes, typed from its value", () => {
    const after = fieldsAfterStep(input, {
      type: "set",
      field: "label_count",
      value: { kind: "function", field: "labels", function: "length" },
    });
    expect(after.find((f) => f.path === "label_count")?.type).toBe("number");
    expect(
      fieldsAfterStep(input, {
        type: "set",
        field: "title",
        value: { kind: "constant", value: 5 },
      }).find((f) => f.path === "title")?.type,
    ).toBe("number");
  });

  it("drops removed fields with their children", () => {
    expect(
      paths(
        fieldsAfterStep(input, { type: "remove", fields: ["author", "tags"] }),
      ),
    ).toEqual(["title", "labels", "labels[].name"]);
  });

  it("keeps only picked fields, renamed or flattened", () => {
    expect(
      paths(
        fieldsAfterStep(input, {
          type: "pick",
          fields: ["title", "author.login"],
          rename: { title: "subject" },
        }),
      ),
    ).toEqual(["subject", "author.login"]);
    expect(
      paths(
        fieldsAfterStep(input, {
          type: "pick",
          fields: ["author.login"],
          flatten: true,
        }),
      ),
    ).toEqual(["author_login"]);
  });

  it("adds the aggregate target and replaces fields on split", () => {
    expect(
      fieldsAfterStep(input, {
        type: "aggregate",
        field: "labels",
        into: "n",
        op: "count",
      }).find((f) => f.path === "n")?.type,
    ).toBe("number");
    expect(
      paths(
        fieldsAfterStep(input, { type: "split", field: "labels", as: "label" }),
      ),
    ).toEqual(["label", "label.name"]);
    expect(
      paths(
        fieldsAfterStep(input, {
          type: "split",
          field: "labels",
          keep_parent: true,
        }),
      ),
    ).toEqual([
      "title",
      "author",
      "author.login",
      "author.id",
      "tags",
      "tags[]",
      "name",
    ]);
  });

  it("gives each step the fields before it", () => {
    const steps: TransformStep[] = [
      { type: "set", field: "x", value: { kind: "constant", value: 1 } },
      { type: "remove", fields: ["title"] },
      { type: "filter" },
    ];
    const lists = fieldsBeforeEachStep(input, steps);
    expect(lists).toHaveLength(3);
    expect(paths(lists[0])).not.toContain("x");
    expect(paths(lists[1])).toContain("x");
    expect(paths(lists[2])).toContain("x");
    expect(paths(lists[2])).not.toContain("title");
  });
});

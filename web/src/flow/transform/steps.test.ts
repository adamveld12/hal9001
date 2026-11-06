import { describe, expect, it } from "vitest";
import { allPorts, declaredPorts, type FlowNode } from "../model";
import { transformPorts } from "./ports";
import {
  insertIndex,
  moveItem,
  newRule,
  newStep,
  normalizeStep,
  stepTitle,
  summarizeStep,
} from "./steps";
import { STEP_KINDS, type TransformStep } from "./types";

describe("transformPorts", () => {
  it("is just out without a route step", () => {
    expect(transformPorts(undefined)).toEqual(["out"]);
    expect(transformPorts([{ type: "filter" }])).toEqual(["out"]);
  });

  it("adds the rule ports then the default port, deduplicated, in order", () => {
    const steps: TransformStep[] = [
      {
        type: "route",
        rules: [{ port: "urgent" }, { port: "normal" }, { port: "urgent" }],
        default_port: "other",
      },
    ];
    expect(transformPorts(steps)).toEqual(["out", "urgent", "normal", "other"]);
  });

  it("does not repeat out when it is the default port, and skips empty or reserved names", () => {
    expect(
      transformPorts([
        {
          type: "route",
          rules: [{ port: "" }, { port: "error" }, { port: "big" }],
          default_port: "out",
        },
      ]),
    ).toEqual(["out", "big"]);
  });

  it("is what a transform node declares, with error implicit", () => {
    const node: FlowNode = {
      id: "t",
      type: "transform",
      steps: [{ type: "route", rules: [{ port: "a" }], default_port: "b" }],
    };
    expect(declaredPorts(node)).toEqual(["out", "a", "b"]);
    expect(allPorts(node)).toEqual(["out", "a", "b", "error"]);
  });
});

describe("normalizeStep", () => {
  it("writes the type first and leaves out defaults and empty values", () => {
    expect(
      normalizeStep({
        keep: true,
        match: "all",
        conditions: [{ field: "a", op: "equals", value: 1 }],
        type: "filter",
      }),
    ).toEqual({
      type: "filter",
      conditions: [{ field: "a", op: "equals", value: 1 }],
    });
    expect(
      Object.keys(
        normalizeStep({
          keep: false,
          match: "any",
          conditions: [{ field: "a", op: "exists" }],
          type: "filter",
        }),
      ),
    ).toEqual(["type", "match", "conditions", "keep"]);
  });

  it("omits order when asc and default_port when out", () => {
    expect(
      normalizeStep({ type: "sort", field: "x", order: "asc", by: "" }),
    ).toEqual({
      type: "sort",
      field: "x",
    });
    expect(
      normalizeStep({ type: "sort", order: "desc", field: "x", by: "k" }),
    ).toEqual({
      type: "sort",
      field: "x",
      by: "k",
      order: "desc",
    });
    expect(
      normalizeStep({
        type: "route",
        default_port: "out",
        rules: [
          {
            port: "a",
            match: "all",
            conditions: [{ field: "x", op: "is_true" }],
          },
        ],
      }),
    ).toEqual({
      type: "route",
      rules: [{ port: "a", conditions: [{ field: "x", op: "is_true" }] }],
    });
  });

  it("keeps a required but empty port and a constant empty string", () => {
    expect(normalizeStep({ type: "route", rules: [{ port: "" }] })).toEqual({
      type: "route",
      rules: [{ port: "" }],
    });
    expect(
      normalizeStep({
        type: "set",
        field: "f",
        value: { kind: "constant", value: "" },
      }),
    ).toEqual({
      type: "set",
      field: "f",
      value: { kind: "constant", value: "" },
    });
    expect(
      normalizeStep({
        type: "set",
        field: "f",
        value: { kind: "constant", value: null },
      }).value,
    ).toEqual({ kind: "constant", value: null });
  });

  it("keeps only the fields of the chosen set value kind", () => {
    expect(
      normalizeStep({
        type: "set",
        field: "n",
        value: {
          kind: "function",
          function: "length",
          field: "tags",
          arg: "x",
          template: "t",
          value: 1,
        },
      }).value,
    ).toEqual({ kind: "function", field: "tags", function: "length" });
    expect(
      normalizeStep({
        type: "set",
        field: "n",
        value: { kind: "function", function: "join", field: "tags", arg: ", " },
      }).value,
    ).toEqual({ kind: "function", field: "tags", function: "join", arg: ", " });
  });

  it("drops renames of fields that are no longer picked, empty names, and false flags", () => {
    expect(
      normalizeStep({
        type: "pick",
        fields: ["a", "b"],
        rename: { a: "x", b: "", gone: "y" },
        flatten: false,
      }),
    ).toEqual({ type: "pick", fields: ["a", "b"], rename: { a: "x" } });
    expect(normalizeStep({ type: "pick", fields: [], flatten: true })).toEqual({
      type: "pick",
      flatten: true,
    });
  });

  it("drops aggregate fields the operation does not use", () => {
    expect(
      normalizeStep({
        type: "aggregate",
        field: "l",
        into: "n",
        op: "count",
        of: "x",
        group_by: "y",
      }),
    ).toEqual({ type: "aggregate", field: "l", into: "n", op: "count" });
    expect(
      normalizeStep({
        type: "aggregate",
        field: "l",
        into: "n",
        op: "group_count",
        group_by: "y",
        of: "x",
      }),
    ).toEqual({
      type: "aggregate",
      field: "l",
      into: "n",
      op: "group_count",
      group_by: "y",
    });
    expect(
      normalizeStep({
        type: "aggregate",
        op: "sum",
        into: "t",
        of: "price",
        field: "items",
      }),
    ).toEqual({
      type: "aggregate",
      field: "items",
      into: "t",
      op: "sum",
      of: "price",
    });
  });

  it("keeps count 0 and is idempotent for every new step", () => {
    expect(normalizeStep({ type: "limit", field: "l", count: 0 })).toEqual({
      type: "limit",
      field: "l",
      count: 0,
    });
    for (const kind of STEP_KINDS) {
      const once = normalizeStep(newStep(kind));
      expect(normalizeStep(once)).toEqual(once);
      expect(JSON.stringify(normalizeStep(once))).toBe(JSON.stringify(once));
    }
  });
});

describe("step factories", () => {
  it("starts a filter with one empty condition and a route with one rule", () => {
    expect(newStep("filter").conditions).toEqual([{ field: "", op: "equals" }]);
    expect(newStep("route").rules).toHaveLength(1);
    expect(newStep("filter_list").conditions?.[0].field).toBe(".");
  });

  it("names new rule ports without clashing", () => {
    expect(newRule([]).port).toBe("match-1");
    expect(newRule(["match-1", "match-2"]).port).toBe("match-3");
  });

  it("has a title for every kind", () => {
    expect(stepTitle({ type: "filter" })).toBe("Keep only items where…");
    expect(stepTitle({ type: "filter", keep: false })).toBe(
      "Drop items where…",
    );
    expect(stepTitle({ type: "aggregate" })).toBe("Summarize a list");
    for (const kind of STEP_KINDS) {
      expect(stepTitle(newStep(kind))).not.toBe("");
    }
  });
});

describe("ordering", () => {
  it("inserts before a trailing route step", () => {
    expect(insertIndex([])).toBe(0);
    expect(insertIndex([{ type: "filter" }, { type: "set" }])).toBe(2);
    expect(insertIndex([{ type: "filter" }, { type: "route" }])).toBe(1);
  });

  it("moves an item to its final index", () => {
    expect(moveItem(["a", "b", "c", "d"], 0, 2)).toEqual(["b", "c", "a", "d"]);
    expect(moveItem(["a", "b", "c", "d"], 3, 1)).toEqual(["a", "d", "b", "c"]);
    expect(moveItem(["a", "b"], 1, 1)).toEqual(["a", "b"]);
  });
});

describe("summarizeStep", () => {
  it("describes conditions in plain words", () => {
    expect(
      summarizeStep({
        type: "filter",
        match: "any",
        conditions: [
          { field: "state", op: "equals", value: "open" },
          { field: "labels[].name", op: "contains", value: "bug" },
        ],
      }),
    ).toBe(
      "Keep only items where state is “open” or labels[].name contains “bug”",
    );
    expect(
      summarizeStep({
        type: "filter",
        keep: false,
        conditions: [{ field: "draft", op: "is_true" }],
      }),
    ).toBe("Drop items where draft is true");
  });

  it("tolerates half-filled steps", () => {
    for (const kind of STEP_KINDS) {
      expect(summarizeStep(newStep(kind)).length).toBeGreaterThan(3);
    }
  });
});

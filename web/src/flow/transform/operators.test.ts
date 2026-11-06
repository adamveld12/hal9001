import { describe, expect, it } from "vitest";
import {
  ALL_OPERATORS,
  coerceListItem,
  coerceText,
  isTextOperator,
  normalizeCondition,
  operatorsFor,
  reconcileCondition,
  reconcileValue,
  takesValue,
  valueNeed,
  valueTypesFor,
  withOperator,
} from "./operators";

describe("operatorsFor", () => {
  it("offers text operators only for strings", () => {
    expect(operatorsFor("string")).toContain("starts_with");
    expect(operatorsFor("string")).toContain("matches");
    expect(operatorsFor("number")).not.toContain("contains");
    expect(operatorsFor("number")).toEqual(
      expect.arrayContaining(["gt", "gte", "lt", "lte", "in", "equals"]),
    );
  });

  it("offers truth tests for booleans and presence tests for lists and objects", () => {
    expect(operatorsFor("boolean")).toEqual(
      expect.arrayContaining(["is_true", "is_false"]),
    );
    expect(operatorsFor("boolean")).not.toContain("gt");
    for (const t of ["array", "object"] as const) {
      expect(operatorsFor(t)).toEqual([
        "exists",
        "not_exists",
        "is_empty",
        "is_not_empty",
      ]);
    }
  });

  it("offers everything for unknown and null fields", () => {
    expect(operatorsFor("unknown")).toEqual([...ALL_OPERATORS]);
    expect(operatorsFor("null")).toEqual([...ALL_OPERATORS]);
    expect(ALL_OPERATORS).toHaveLength(18);
  });

  it("only offers operators that exist", () => {
    for (const t of [
      "string",
      "number",
      "boolean",
      "array",
      "object",
    ] as const) {
      for (const op of operatorsFor(t)) expect(ALL_OPERATORS).toContain(op);
    }
  });
});

describe("value rules", () => {
  it("requires no value for presence, emptiness and truth tests", () => {
    for (const op of [
      "exists",
      "not_exists",
      "is_empty",
      "is_not_empty",
      "is_true",
      "is_false",
    ] as const) {
      expect(takesValue(op)).toBe(false);
    }
    expect(valueNeed("in")).toBe("list");
    expect(valueNeed("matches")).toBe("text");
    expect(valueNeed("gt")).toBe("compare");
    expect(valueNeed("equals")).toBe("any");
  });

  it("allows case sensitivity only on the text operators of the contract", () => {
    const yes = [
      "equals",
      "not_equals",
      "contains",
      "not_contains",
      "starts_with",
      "ends_with",
      "matches",
      "in",
    ] as const;
    for (const op of ALL_OPERATORS) {
      expect(isTextOperator(op)).toBe((yes as readonly string[]).includes(op));
    }
  });

  it("chooses value types from operator and field type", () => {
    expect(valueTypesFor("starts_with", "unknown")).toEqual(["string"]);
    expect(valueTypesFor("gt", "number")).toEqual(["number"]);
    expect(valueTypesFor("gt", "string")).toEqual(["string"]);
    expect(valueTypesFor("gt", "unknown")).toEqual(["number", "string"]);
    expect(valueTypesFor("equals", "boolean")).toEqual(["boolean"]);
    expect(valueTypesFor("equals", "unknown")).toEqual([
      "string",
      "number",
      "boolean",
    ]);
    expect(valueTypesFor("exists", "string")).toEqual([]);
  });
});

describe("coercion", () => {
  it("coerces typed text", () => {
    expect(coerceText("12.5", "number")).toBe(12.5);
    expect(coerceText("", "number")).toBeUndefined();
    expect(coerceText("1.", "number")).toBe(1);
    expect(coerceText("abc", "number")).toBeUndefined();
    expect(coerceText("true", "boolean")).toBe(true);
    expect(coerceText("yes", "boolean")).toBeUndefined();
    expect(coerceText(" x ", "string")).toBe(" x ");
  });

  it("types list entries by field type, inferring for unknown fields", () => {
    expect(coerceListItem("7", "number")).toBe(7);
    expect(coerceListItem("seven", "number")).toBeUndefined();
    expect(coerceListItem("7", "string")).toBe("7");
    expect(coerceListItem("TRUE", "boolean")).toBe(true);
    expect(coerceListItem("7", "unknown")).toBe(7);
    expect(coerceListItem("true", "unknown")).toBe(true);
    expect(coerceListItem(" open ", "unknown")).toBe("open");
    expect(coerceListItem("  ", "unknown")).toBeUndefined();
  });
});

describe("keeping a condition consistent", () => {
  it("drops the value and case flag an operator does not take", () => {
    expect(
      normalizeCondition({
        field: "a",
        op: "exists",
        value: "x",
        case_sensitive: true,
      }),
    ).toEqual({ field: "a", op: "exists" });
    expect(
      normalizeCondition({
        field: "a",
        op: "gt",
        value: 1,
        case_sensitive: true,
      }),
    ).toEqual({ field: "a", op: "gt", value: 1 });
    expect(normalizeCondition({ field: "a", op: "in", value: [] })).toEqual({
      field: "a",
      op: "in",
    });
  });

  it("writes keys in field, op, value, case_sensitive order", () => {
    const c = normalizeCondition({
      case_sensitive: true,
      value: "x",
      op: "equals",
      field: "a",
    });
    expect(Object.keys(c)).toEqual(["field", "op", "value", "case_sensitive"]);
  });

  it("converts or drops the value when the operator changes", () => {
    const eq = { field: "n", op: "equals" as const, value: "5" };
    expect(withOperator(eq, "in", "number").value).toEqual([5]);
    expect(withOperator(eq, "exists", "number").value).toBeUndefined();
    expect(
      withOperator(
        { field: "n", op: "in", value: ["a", "b"] },
        "equals",
        "string",
      ).value,
    ).toBe("a");
    expect(reconcileValue("starts_with", "string", 12)).toBe("12");
    expect(reconcileValue("gt", "number", "abc")).toBeUndefined();
  });

  it("falls back to equals when the new field type lacks the operator, and converts the value", () => {
    const c = reconcileCondition(
      { field: "number", op: "contains", value: "4", case_sensitive: true },
      "number",
    );
    expect(c).toEqual({ field: "number", op: "equals", value: 4 });
    expect(
      reconcileCondition({ field: "x", op: "gt", value: 3 }, "string"),
    ).toEqual({ field: "x", op: "gt", value: "3" });
    expect(
      reconcileCondition({ field: "x", op: "equals", value: true }, "array"),
    ).toEqual({ field: "x", op: "exists" });
  });
});

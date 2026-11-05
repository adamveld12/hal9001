import { describe, expect, it } from "vitest";
import { planRender } from "./preview";

const EVENT = "01JC7Z3K9Q8V5T2M4N6P8R0S1T";

describe("planRender", () => {
  it("renders unsaved text against a stored event", () => {
    expect(
      planRender("Hi {{.input.a}}", { kind: "event", eventId: EVENT }, ""),
    ).toEqual({
      ok: true,
      request: { markdown: "Hi {{.input.a}}", event_id: EVENT },
    });
  });

  it("asks for an event when none is picked", () => {
    expect(planRender("x", { kind: "event", eventId: null }, "")).toMatchObject(
      { ok: false, reason: "no-sample" },
    );
  });

  it("parses pasted JSON into `input`, including non-object values", () => {
    expect(
      planRender("x", { kind: "json", text: '{"a": [1, 2]}' }, ""),
    ).toEqual({ ok: true, request: { markdown: "x", input: { a: [1, 2] } } });
    expect(planRender("x", { kind: "json", text: "null" }, "")).toMatchObject({
      ok: true,
      request: { input: null },
    });
  });

  it("reports malformed JSON without making a request", () => {
    const plan = planRender("x", { kind: "json", text: "{a:" }, "");
    expect(plan).toMatchObject({ ok: false, reason: "invalid-json" });
    expect(plan.ok ? "" : plan.message).toMatch(/^Invalid JSON: /);
  });

  it("treats blank pasted text as no sample", () => {
    expect(planRender("x", { kind: "json", text: " \n" }, "")).toMatchObject({
      ok: false,
      reason: "no-sample",
    });
  });

  it("sends extra_prompt only when it has content", () => {
    const sample = { kind: "event", eventId: EVENT } as const;
    expect(planRender("x", sample, "  ")).toEqual({
      ok: true,
      request: { markdown: "x", event_id: EVENT },
    });
    expect(planRender("x", sample, "Be brief.")).toMatchObject({
      request: { extra_prompt: "Be brief." },
    });
  });
});

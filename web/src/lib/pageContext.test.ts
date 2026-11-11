import { describe, expect, it } from "vitest";
import {
  capPageContext,
  contextBytes,
  MAX_ENTITIES,
  MAX_PAGE_CONTEXT_BYTES,
  PageContextRegistry,
} from "./pageContext";

describe("capPageContext", () => {
  it("keeps a small context as it is and adds the route", () => {
    expect(
      capPageContext("/inbox?kind=action", {
        title: "Inbox",
        summary: "3 items",
        facts: [{ label: "filter", value: "action" }],
        entities: [{ kind: "inbox_item", id: "01A", label: "Deploy failed" }],
      }),
    ).toEqual({
      route: "/inbox?kind=action",
      title: "Inbox",
      summary: "3 items",
      facts: [{ label: "filter", value: "action" }],
      entities: [{ kind: "inbox_item", id: "01A", label: "Deploy failed" }],
    });
  });

  it("omits empty optional parts", () => {
    expect(capPageContext("/timeline", { title: "Timeline" })).toEqual({
      route: "/timeline",
      title: "Timeline",
    });
  });

  it("clips long strings and strips control characters", () => {
    const ctx = capPageContext("/x", {
      title: "A\nB\u0000" + "t".repeat(500),
      entities: [{ kind: "flow", label: "l".repeat(500) }],
    });
    expect(ctx.title.includes("\n")).toBe(false);
    expect(ctx.title.includes("\u0000")).toBe(false);
    expect(ctx.title.length).toBeLessThanOrEqual(160);
    expect(ctx.title.endsWith("…")).toBe(true);
    expect(ctx.entities?.[0]?.label?.length).toBeLessThanOrEqual(120);
  });

  it("limits the lists and drops entities, then facts, to fit the byte cap", () => {
    const entities = Array.from({ length: 200 }, (_, i) => ({
      kind: "sensor",
      id: `sensor-${i}`,
      label: "n".repeat(100),
      detail: "d".repeat(100),
    }));
    const facts = Array.from({ length: 10 }, (_, i) => ({
      label: `f${i}`,
      value: "v".repeat(250),
    }));
    const ctx = capPageContext("/sensors", {
      title: "Sensors",
      entities,
      facts,
    });
    expect(contextBytes(ctx)).toBeLessThanOrEqual(MAX_PAGE_CONTEXT_BYTES);
    expect((ctx.entities?.length ?? 0) < MAX_ENTITIES).toBe(true);
    expect(ctx.title).toBe("Sensors");
    // facts survive while entities absorb the cut
    expect(ctx.facts?.length).toBe(10);
  });

  it("measures bytes, not characters", () => {
    const ctx = capPageContext(
      "/x",
      {
        title: "T",
        summary: "é".repeat(400),
        facts: [{ label: "a", value: "é".repeat(300) }],
      },
      300,
    );
    expect(contextBytes(ctx)).toBeLessThanOrEqual(300);
    expect(ctx.title).toBe("T");
  });

  it("does not split a surrogate pair when clipping", () => {
    const ctx = capPageContext("/x", { title: "😀".repeat(200) });
    const last = ctx.title.charCodeAt(ctx.title.length - 2);
    expect(last >= 0xd800 && last <= 0xdbff).toBe(false);
  });
});

describe("PageContextRegistry", () => {
  it("returns the newest page's input", () => {
    const r = new PageContextRegistry();
    const a = Symbol("a");
    r.set(a, { title: "A" });
    r.set(a, { title: "A2" });
    expect(r.get()?.title).toBe("A2");
  });

  it("a successor survives the previous page's late cleanup", () => {
    const r = new PageContextRegistry();
    const a = Symbol("a");
    const b = Symbol("b");
    r.set(a, { title: "A" });
    r.set(b, { title: "B" });
    r.clear(a);
    expect(r.get()?.title).toBe("B");
    r.clear(b);
    expect(r.get()).toBeNull();
  });
});

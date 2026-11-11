import { describe, expect, it } from "vitest";
import { NAV_SECTIONS, navCommands } from "../../layout/nav";
import { CommandRegistry } from "./registry";
import type { Command } from "./types";

const cmd = (id: string, extra: Partial<Command> = {}): Command => ({
  id,
  title: id,
  group: "Actions",
  run: () => undefined,
  ...extra,
});

describe("CommandRegistry", () => {
  it("lists the commands of every owner in registration order", () => {
    const r = new CommandRegistry();
    r.set("a", [cmd("one"), cmd("two")]);
    r.set("b", [cmd("three")]);
    expect(r.getSnapshot().map((c) => c.id)).toEqual(["one", "two", "three"]);
    r.remove("a");
    expect(r.getSnapshot().map((c) => c.id)).toEqual(["three"]);
  });

  it("shows every sidebar page and settings section as a Go to command", () => {
    const r = new CommandRegistry();
    const went: string[] = [];
    r.set(
      "nav",
      navCommands(NAV_SECTIONS, (to) => went.push(to)),
    );
    const pages = NAV_SECTIONS.flatMap((s) => s.items);
    const goTo = r.getSnapshot().filter((c) => c.group === "Go to");
    expect(goTo).toHaveLength(pages.length);
    expect(goTo.map((c) => c.id)).toEqual(pages.map((p) => `go:${p.to}`));
    expect(goTo.some((c) => c.title === "Go to Settings: General")).toBe(true);
    expect(r.find("go:/inbox")?.shortcut).toBe("g i");
    r.find("go:/flows")?.run({ source: "palette" });
    expect(went).toEqual(["/flows"]);
  });

  it("gives a page added to the nav a command without further wiring", () => {
    const r = new CommandRegistry();
    const sections = [
      ...NAV_SECTIONS,
      {
        label: "New",
        items: [
          {
            to: "/reports",
            label: "Reports",
            icon: "timeline" as const,
            key: "w",
          },
        ],
      },
    ];
    r.set(
      "nav",
      navCommands(sections, () => undefined),
    );
    expect(r.find("go:/reports")?.shortcut).toBe("g w");
  });

  it("drops a shortcut an earlier command holds so the hint is the one that works", () => {
    const r = new CommandRegistry();
    r.set(
      "nav",
      navCommands(NAV_SECTIONS, () => undefined),
    );
    // Context and MCP both declare `g c`; the first one wins in the handler.
    expect(r.find("go:/settings/context")?.shortcut).toBe("g c");
    expect(r.find("go:/settings/mcp")?.shortcut).toBeUndefined();
  });

  it("keeps the first command of a duplicate id", () => {
    const r = new CommandRegistry();
    r.set("a", [cmd("x", { title: "first" })]);
    r.set("b", [cmd("x", { title: "second" })]);
    expect(r.getSnapshot()).toHaveLength(1);
    expect(r.find("x")?.title).toBe("first");
  });

  it("tells listeners only about visible changes, and always hands out the newest run", () => {
    const r = new CommandRegistry();
    let told = 0;
    r.subscribe(() => told++);
    const calls: string[] = [];
    r.set("a", [cmd("x", { run: () => calls.push("old") })]);
    expect(told).toBe(1);
    const shown = r.getSnapshot();
    r.set("a", [cmd("x", { run: () => calls.push("new") })]);
    expect(told).toBe(1);
    expect(r.getSnapshot()).toBe(shown);
    r.find("x")?.run({ source: "shortcut" });
    expect(calls).toEqual(["new"]);
    r.set("a", [cmd("x", { disabled: "Not now" })]);
    expect(told).toBe(2);
    expect(r.getSnapshot()[0]?.disabled).toBe("Not now");
  });
});

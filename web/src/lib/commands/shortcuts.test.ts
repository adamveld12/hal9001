import { describe, expect, it } from "vitest";
import { isModKey, resolveKey, shortcutKeys, shortcutLabel } from "./shortcuts";
import type { Command } from "./types";

const key = (k: string, mods: Record<string, boolean> = {}) => ({
  key: k,
  metaKey: false,
  ctrlKey: false,
  altKey: false,
  shiftKey: false,
  ...mods,
});

const cmd = (id: string, shortcut?: string): Command => ({
  id,
  title: id,
  group: "Actions",
  shortcut,
  run: () => undefined,
});

describe("isModKey", () => {
  it("matches Cmd or Ctrl plus the key and nothing else", () => {
    expect(isModKey(key("p", { metaKey: true }), "p")).toBe(true);
    expect(isModKey(key("P", { ctrlKey: true }), "p")).toBe(true);
    expect(isModKey(key("p"), "p")).toBe(false);
    expect(isModKey(key("k", { metaKey: true }), "p")).toBe(false);
    expect(isModKey(key("p", { metaKey: true, shiftKey: true }), "p")).toBe(
      false,
    );
    expect(isModKey(key("p", { ctrlKey: true, altKey: true }), "p")).toBe(
      false,
    );
  });
});

describe("resolveKey", () => {
  const commands = [
    cmd("inbox", "g i"),
    cmd("sidebar", "["),
    cmd("ask", "mod+k"),
    cmd("none"),
  ];

  it("starts a sequence on its first key and runs on the second", () => {
    expect(resolveKey(commands, null, "g")).toEqual({
      kind: "pending",
      first: "g",
    });
    const r = resolveKey(commands, "g", "i");
    expect(r.kind === "run" && r.command.id).toBe("inbox");
  });

  it("runs a single-key shortcut at once", () => {
    const r = resolveKey(commands, null, "[");
    expect(r.kind === "run" && r.command.id).toBe("sidebar");
  });

  it("ignores unbound keys, a wrong second key and mod shortcuts", () => {
    expect(resolveKey(commands, null, "x")).toEqual({ kind: "none" });
    expect(resolveKey(commands, "g", "x")).toEqual({ kind: "none" });
    expect(resolveKey(commands, null, "k")).toEqual({ kind: "none" });
  });
});

describe("shortcut display", () => {
  it("shows the platform's modifier", () => {
    expect(shortcutKeys("mod+k", true)).toEqual(["⌘", "K"]);
    expect(shortcutKeys("mod+k", false)).toEqual(["Ctrl", "K"]);
    expect(shortcutKeys("g i", true)).toEqual(["g", "i"]);
    expect(shortcutLabel("mod+p", true)).toBe("⌘P");
    expect(shortcutLabel("g i", false)).toBe("g i");
  });
});

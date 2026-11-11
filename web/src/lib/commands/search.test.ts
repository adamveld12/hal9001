import { describe, expect, it } from "vitest";
import { itemCommands } from "./items";
import { parseArgument, search } from "./search";
import type { Command } from "./types";

const cmd = (
  id: string,
  title: string,
  extra: Partial<Command> = {},
): Command => ({
  id,
  title,
  group: "Actions",
  run: () => undefined,
  ...extra,
});

const COMMANDS: Command[] = [
  cmd("go:inbox", "Go to Inbox", { group: "Go to" }),
  cmd("go:flows", "Go to Flows", { group: "Go to" }),
  cmd("create:flow", "New flow", { group: "Create" }),
  cmd("create:flow-ai", "New flow with AI…", {
    group: "Create",
    acceptsText: true,
  }),
  cmd("chat:ask", "Ask AI…", { group: "Chat", acceptsText: true }),
  cmd("chat:new", "New chat", { group: "Chat" }),
  cmd("action:theme", "Toggle theme"),
  cmd("flow:triage", "triage-github", { group: "Flows", queryOnly: true }),
];

const ids = (r: ReturnType<typeof search>) => r.flat.map((m) => m.command.id);

describe("parseArgument", () => {
  it("splits at the first colon", () => {
    expect(parseArgument("ask ai: hello: world")).toEqual({
      head: "ask ai",
      arg: "hello: world",
    });
    expect(parseArgument("no colon")).toBeNull();
    expect(parseArgument(": text")).toBeNull();
  });
});

describe("search", () => {
  it("lists every group without items when nothing is typed", () => {
    const r = search("", COMMANDS, []);
    expect(r.sections.map((s) => s.title)).toEqual([
      "Go to",
      "Create",
      "Chat",
      "Actions",
    ]);
    expect(ids(r)).not.toContain("flow:triage");
  });

  it("puts recently used commands first and not again below", () => {
    const r = search("", COMMANDS, ["action:theme", "go:flows", "gone"]);
    expect(r.sections[0]).toMatchObject({ title: "Recent", recent: true });
    expect(r.sections[0]?.items.map((m) => m.command.id)).toEqual([
      "action:theme",
      "go:flows",
    ]);
    expect(ids(r).filter((id) => id === "action:theme")).toHaveLength(1);
  });

  it("ranks by match, then recency, and groups the results", () => {
    expect(ids(search("flow", COMMANDS, []))[0]).toBe("create:flow");
    const r = search("new", COMMANDS, []);
    expect(ids(r).slice(0, 3).sort()).toEqual([
      "chat:new",
      "create:flow",
      "create:flow-ai",
    ]);
    // A recent command beats an otherwise equal match.
    const recent = search("new", COMMANDS, ["chat:new"]);
    expect(ids(recent)[0]).toBe("chat:new");
  });

  it("finds items by name only once something is typed", () => {
    const r = search("triage", COMMANDS, []);
    expect(ids(r)).toEqual(["flow:triage"]);
    expect(r.sections[0]?.title).toBe("Flows");
  });

  it("matches keywords", () => {
    const withKw = [cmd("t", "Toggle theme", { keywords: ["dark", "light"] })];
    expect(ids(search("dark", withKw, []))).toEqual(["t"]);
  });

  it("caps each item group", () => {
    const many = itemCommands(
      { flows: Array.from({ length: 40 }, (_, i) => ({ id: `flow-${i}` })) },
      () => undefined,
      (id) => `/chat/${id}`,
    );
    expect(search("flow", many, []).flat).toHaveLength(6);
  });

  it("passes the text after the colon to a command that takes text", () => {
    const r = search("ask ai: hello there", COMMANDS, []);
    expect(r.arg).toBe("hello there");
    expect(r.flat[0]?.command.id).toBe("chat:ask");
    expect(r.flat.every((m) => m.command.acceptsText)).toBe(true);
    const draft = search("new flow with ai: triage github", COMMANDS, []);
    expect(draft.flat[0]?.command.id).toBe("create:flow-ai");
    expect(draft.arg).toBe("triage github");
  });

  it("has no argument without text after the colon, or for a command that takes none", () => {
    expect(search("ask ai:", COMMANDS, []).arg).toBeUndefined();
    const plain = search("toggle theme: now", COMMANDS, []);
    expect(plain.arg).toBeUndefined();
  });

  it("lists disabled commands with their reason", () => {
    const list = [cmd("x", "Stop all", { disabled: "Nothing running" })];
    expect(search("stop", list, []).flat[0]?.command.disabled).toBe(
      "Nothing running",
    );
  });
});

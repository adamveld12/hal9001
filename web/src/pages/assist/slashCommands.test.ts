import { describe, expect, it } from "vitest";
import { parseSlashCommand, slashHints } from "./slashCommands";

describe("parseSlashCommand", () => {
  it("recognises /new and /clear, ignoring surrounding space", () => {
    expect(parseSlashCommand("/new")?.name).toBe("/new");
    expect(parseSlashCommand("  /clear \n")?.name).toBe("/clear");
  });

  it("treats anything else as a message", () => {
    expect(parseSlashCommand("/new chat please")).toBeNull();
    expect(parseSlashCommand("/newer")).toBeNull();
    expect(parseSlashCommand("new")).toBeNull();
    expect(parseSlashCommand("")).toBeNull();
  });
});

describe("slashHints", () => {
  it("lists matching commands while a command is being typed", () => {
    expect(slashHints("/").map((c) => c.name)).toEqual(["/new", "/clear"]);
    expect(slashHints("/cl").map((c) => c.name)).toEqual(["/clear"]);
  });

  it("is empty for a message or once a space follows", () => {
    expect(slashHints("hello /new")).toEqual([]);
    expect(slashHints("/new now")).toEqual([]);
    expect(slashHints("/x")).toEqual([]);
  });
});

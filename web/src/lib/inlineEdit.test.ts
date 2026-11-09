import { describe, expect, it } from "vitest";
import {
  IDLE,
  pressKey,
  saveFailed,
  startEditing,
  typed,
  validateName,
} from "./inlineEdit";

const check = (v: string) =>
  validateName(v, "ping", ["pong", "ping"], "sensor");

describe("validateName", () => {
  it("accepts a valid new name and the current one", () => {
    expect(validateName("new-name_1", "ping", [], "sensor")).toBeNull();
    expect(check("ping")).toBeNull();
  });
  it("rejects empty and invalid names", () => {
    expect(check("")).toMatch(/required/);
    expect(check("bad name")).toMatch(/letters, digits/);
    expect(check("-lead")).toMatch(/letters, digits/);
    expect(check("a".repeat(65))).toMatch(/letters, digits/);
  });
  it("rejects a name another item has", () => {
    expect(check("pong")).toBe("A sensor named pong already exists.");
  });
});

describe("pressKey", () => {
  const editing = (draft: string) => typed(startEditing("ping"), draft);

  it("saves a valid changed name on Enter and locks the editor", () => {
    const out = pressKey(editing(" pang "), "Enter", "ping", check);
    expect(out.action).toBe("save");
    if (out.action === "save") expect(out.value).toBe("pang");
    expect(out.state.saving).toBe(true);
  });
  it("keeps the editor open with the message for an invalid name", () => {
    const out = pressKey(editing("pong"), "Enter", "ping", check);
    expect(out.action).toBe("none");
    expect(out.state.editing).toBe(true);
    expect(out.state.error).toMatch(/already exists/);
  });
  it("closes without saving when the name is unchanged", () => {
    expect(pressKey(editing("ping"), "Enter", "ping", check).state).toEqual(
      IDLE,
    );
  });
  it("discards the draft on Escape", () => {
    expect(pressKey(editing("zzz"), "Escape", "ping", check).state).toEqual(
      IDLE,
    );
  });
  it("ignores keys while saving and other keys", () => {
    const saving = { ...editing("pang"), saving: true };
    expect(pressKey(saving, "Escape", "ping", check).state).toBe(saving);
    expect(pressKey(editing("p"), "a", "ping", check).action).toBe("none");
  });
  it("shows a server error and clears it when typing again", () => {
    const failed = saveFailed(editing("pang"), "taken on disk");
    expect(failed).toMatchObject({ saving: false, error: "taken on disk" });
    expect(typed(failed, "pang2").error).toBeNull();
  });
});

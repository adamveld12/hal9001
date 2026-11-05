import { describe, expect, it } from "vitest";
import {
  initEdit,
  isDirty,
  reduceEdit,
  type EditAction,
  type EditState,
} from "./editState";

const v1 = { hash: "h1", text: "one" };
const v2 = { hash: "h2", text: "two" };
const v3 = { hash: "h3", text: "three" };

function run(start: EditState, ...actions: EditAction[]): EditState {
  return actions.reduce(reduceEdit, start);
}

describe("reduceEdit", () => {
  it("tracks whether the draft differs from the base", () => {
    const s = run(initEdit(v1), { type: "edit", draft: "one!" });
    expect(isDirty(s)).toBe(true);
    expect(isDirty(run(s, { type: "edit", draft: "one" }))).toBe(false);
  });

  it("adopts a newer server version silently when the draft is clean", () => {
    const s = run(initEdit(v1), { type: "server", file: v2 });
    expect(s).toEqual(initEdit(v2));
  });

  it("flags a remote change instead of overwriting an edited draft", () => {
    const s = run(
      initEdit(v1),
      { type: "edit", draft: "mine" },
      { type: "server", file: v2 },
    );
    expect(s.draft).toBe("mine");
    expect(s.base).toEqual(v1);
    expect(s.conflict).toEqual({ kind: "remote", currentHash: "h2" });
  });

  it("ignores a read of the version it already has", () => {
    const s = run(initEdit(v1), { type: "edit", draft: "mine" });
    expect(run(s, { type: "server", file: v1 })).toBe(s);
  });

  it("reload drops the draft and the conflict", () => {
    const s = run(
      initEdit(v1),
      { type: "edit", draft: "mine" },
      { type: "conflict", currentHash: "h2" },
      { type: "reload", file: v2 },
    );
    expect(s).toEqual(initEdit(v2));
  });

  it("rebase keeps the draft, moves the hash, and clears the conflict", () => {
    const s = run(
      initEdit(v1),
      { type: "edit", draft: "mine" },
      { type: "conflict", currentHash: "h2" },
      { type: "rebase", file: v2 },
    );
    expect(s.draft).toBe("mine");
    expect(s.base).toEqual(v2);
    expect(s.conflict).toBeNull();
    expect(isDirty(s)).toBe(true);
  });

  it("after a successful save the draft is clean; typing during the save is kept", () => {
    const edited = run(initEdit(v1), { type: "edit", draft: "mine" });
    const saved = run(edited, {
      type: "saved",
      submitted: "mine",
      file: { hash: "h2", text: "mine" },
    });
    expect(isDirty(saved)).toBe(false);
    expect(saved.base.hash).toBe("h2");

    const typedMore = run(
      edited,
      { type: "edit", draft: "mine and more" },
      { type: "saved", submitted: "mine", file: { hash: "h2", text: "mine" } },
    );
    expect(typedMore.draft).toBe("mine and more");
    expect(isDirty(typedMore)).toBe(true);
  });

  it("the stream echo of our own save does not raise a conflict", () => {
    const s = run(
      initEdit(v1),
      { type: "edit", draft: "mine" },
      { type: "saved", submitted: "mine", file: v2 },
      { type: "edit", draft: "mine again" },
      { type: "server", file: v2 },
    );
    expect(s.conflict).toBeNull();
  });

  it("does not replace a save conflict by the same remote hash", () => {
    const s = run(
      initEdit(v1),
      { type: "edit", draft: "mine" },
      { type: "conflict", currentHash: "h2" },
    );
    expect(run(s, { type: "server", file: v2 })).toBe(s);
    expect(run(s, { type: "server", file: v3 }).conflict).toEqual({
      kind: "remote",
      currentHash: "h3",
    });
  });
});

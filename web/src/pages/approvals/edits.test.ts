import { describe, expect, it } from "vitest";
import { buildEdits, draftText, editorKind, initialDrafts } from "./edits";

const editable = [
  { key: "draft", value: "hi" },
  { key: "tags", value: ["a", "b"] },
  { key: "gone", value: null },
];

describe("edit form", () => {
  it("edits strings as text and everything else as JSON", () => {
    expect(editorKind("x")).toBe("text");
    expect(editorKind({})).toBe("json");
    expect(editorKind(null)).toBe("json");
    expect(initialDrafts(editable)).toEqual({
      draft: "hi",
      tags: '[\n  "a",\n  "b"\n]',
      gone: "null",
    });
  });

  it("sends nothing while the boxes are untouched or equal in value", () => {
    expect(buildEdits(editable, initialDrafts(editable))).toEqual({
      edits: {},
      errors: {},
    });
    expect(
      buildEdits(editable, { ...initialDrafts(editable), tags: '["a","b"]' })
        .edits,
    ).toEqual({});
  });

  it("sends only the changed paths, parsing JSON boxes", () => {
    const drafts = {
      ...initialDrafts(editable),
      draft: "hello",
      tags: '["z"]',
    };
    expect(buildEdits(editable, drafts)).toEqual({
      edits: { draft: "hello", tags: ["z"] },
      errors: {},
    });
  });

  it("reports a JSON box that does not parse and sends nothing for it", () => {
    const { edits, errors } = buildEdits(editable, {
      ...initialDrafts(editable),
      tags: "[",
    });
    expect(edits).toEqual({});
    expect(Object.keys(errors)).toEqual(["tags"]);
  });

  it("keeps an emptied string as an edit", () => {
    expect(buildEdits(editable, { draft: "" }).edits).toEqual({ draft: "" });
    expect(draftText(undefined)).toBe("null");
  });
});

import { describe, expect, it } from "vitest";
import type { AssistDraft } from "../../api/assist";
import {
  changedLines,
  initSync,
  keepMine,
  pickChecked,
  pushed,
  reconcileDraft,
  shouldPush,
  takeAi,
  type SyncState,
} from "./draftSync";

function draft(
  yaml: string,
  hash: string,
  by: AssistDraft["updated_by"] = "agent",
): AssistDraft {
  return {
    kind: "sensor",
    yaml,
    hash,
    sensor_id: null,
    flow_id: null,
    prompts: [],
    diagnostics: [],
    updated_at: "2026-11-05T10:00:00Z",
    updated_by: by,
  };
}

const synced = (hash: string, text: string): SyncState => ({
  syncedHash: hash,
  syncedText: text,
  pending: null,
});

describe("reconcileDraft: an agent update", () => {
  it("replaces the editor when the user has not typed since the last sync", () => {
    const r = reconcileDraft(synced("h1", "a"), draft("b", "h2"), "a");
    expect(r.text).toBe("b");
    expect(r.sync).toEqual(synced("h2", "b"));
    expect(r.aiChange).toEqual({ before: "a", after: "b" });
  });

  it("waits for a decision when the user has typed since", () => {
    const update = draft("b", "h2");
    const r = reconcileDraft(synced("h1", "a"), update, "a!");
    expect(r.text).toBe("a!");
    expect(r.sync.pending).toBe(update);
    expect(r.sync.syncedHash).toBe("h1");
    expect(r.aiChange).toBeNull();
  });

  it("does nothing for a draft it already knows", () => {
    const s = synced("h1", "a");
    const r = reconcileDraft(s, draft("a", "h1"), "a");
    expect(r.sync).toBe(s);
    const pending = { ...s, pending: draft("b", "h2") };
    expect(reconcileDraft(pending, draft("b", "h2"), "a!").sync).toBe(pending);
  });

  it("only syncs when the draft already equals the editor text", () => {
    const r = reconcileDraft(synced("h1", "a"), draft("a!", "h2"), "a!");
    expect(r.sync).toEqual(synced("h2", "a!"));
    expect(r.aiChange).toBeNull();
  });

  it("replaces an older undecided update with a newer one", () => {
    const s = { ...synced("h1", "a"), pending: draft("b", "h2") };
    const r = reconcileDraft(s, draft("c", "h3"), "a!");
    expect(r.sync.pending?.hash).toBe("h3");
  });
});

describe("reconcileDraft: the user's own draft changes", () => {
  it("never raises the banner for an echo of the user's push or message", () => {
    const r = reconcileDraft(
      synced("h1", "a"),
      draft("ab", "h2", "user"),
      "abc",
    );
    expect(r.text).toBe("abc");
    expect(r.sync.pending).toBeNull();
    // Typing went on after the push: the rest still has to be pushed, on top of h2.
    expect(shouldPush(r.sync, "abc")).toBe(true);
    expect(r.sync.syncedHash).toBe("h2");
  });
});

describe("reconcileDraft: first draft after binding", () => {
  it("shows the session's draft in a clean editor", () => {
    const r = reconcileDraft(
      initSync("file", false),
      draft("draft", "h1", "user"),
      "file",
    );
    expect(r.text).toBe("draft");
    expect(r.sync).toEqual(synced("h1", "draft"));
  });

  it("holds the session's draft back from an editor with unsaved edits", () => {
    const r = reconcileDraft(
      initSync("mine", true),
      draft("draft", "h1", "user"),
      "mine",
    );
    expect(r.text).toBe("mine");
    expect(r.sync.pending?.hash).toBe("h1");
  });

  it("is quiet when a new session starts from the editor's text", () => {
    const r = reconcileDraft(
      initSync("mine", true),
      draft("mine", "h1", "user"),
      "mine",
    );
    expect(r.sync).toEqual(synced("h1", "mine"));
    expect(r.aiChange).toBeNull();
  });
});

describe("pushing user edits", () => {
  it("pushes once the editor differs from the synced draft", () => {
    expect(shouldPush(synced("h1", "a"), "a")).toBe(false);
    expect(shouldPush(synced("h1", "a"), "ab")).toBe(true);
  });

  it("does not push before the first draft or while an update awaits a decision", () => {
    expect(shouldPush(initSync("a", false), "ab")).toBe(false);
    const waiting = { ...synced("h1", "a"), pending: draft("b", "h2") };
    expect(shouldPush(waiting, "ab")).toBe(false);
  });

  it("marks the sent text as synced, even if the server stored it differently", () => {
    const s = pushed(draft("ab\n", "h2", "user"), "ab");
    expect(s).toEqual(synced("h2", "ab"));
    expect(shouldPush(s, "ab")).toBe(false);
  });
});

describe("deciding on an undecided update", () => {
  const waiting = { ...synced("h1", "a"), pending: draft("b", "h2") };

  it("Take AI version puts the update in the editor and marks it", () => {
    const r = takeAi(waiting, "a!");
    expect(r.text).toBe("b");
    expect(r.sync).toEqual(synced("h2", "b"));
    expect(r.aiChange).toEqual({ before: "a!", after: "b" });
  });

  it("Keep mine rebases onto the update so the next push overwrites it", () => {
    const s = keepMine(waiting);
    expect(s.pending).toBeNull();
    expect(s.syncedHash).toBe("h2");
    expect(shouldPush(s, "a!")).toBe(true);
  });
});

describe("changedLines", () => {
  it("lists added and changed lines by their number in the new text", () => {
    expect(changedLines("a\nb\nc\n", "a\nB\nc\nd\n")).toEqual([2, 4]);
  });

  it("lists nothing for a deletion-only change or identical text", () => {
    expect(changedLines("a\nb\nc\n", "a\nc\n")).toEqual([]);
    expect(changedLines("a\n", "a\n")).toEqual([]);
  });
});

describe("pickChecked", () => {
  const a = { text: "x", diagnostics: [] };
  const b = { text: "y", diagnostics: [] };
  it("prefers the list checked against exactly this text", () => {
    expect(pickChecked("y", a, b)).toBe(b);
  });
  it("falls back to the first list, which the display marks stale", () => {
    expect(pickChecked("z", a, b)).toBe(a);
    expect(pickChecked("z", null, b)).toBe(b);
    expect(pickChecked("z", null)).toBeNull();
  });
});

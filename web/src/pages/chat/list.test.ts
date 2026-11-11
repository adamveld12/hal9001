import { describe, expect, it } from "vitest";
import type { Conversation } from "../../api/conversations";
import { mergePages } from "./list";

const c = (id: string) => ({ id }) as Conversation;

describe("mergePages", () => {
  it("joins pages in order", () => {
    expect(
      mergePages([{ items: [c("a"), c("b")] }, { items: [c("c")] }]).map(
        (x) => x.id,
      ),
    ).toEqual(["a", "b", "c"]);
  });

  it("lists a conversation once when it moved between pages", () => {
    const first = { ...c("b"), title: "newer" } as Conversation;
    const out = mergePages([
      { items: [c("a"), first] },
      { items: [{ ...c("b"), title: "older" } as Conversation, c("d")] },
    ]);
    expect(out.map((x) => x.id)).toEqual(["a", "b", "d"]);
    expect(out[1]?.title).toBe("newer");
  });

  it("is empty before anything loaded", () => {
    expect(mergePages(undefined)).toEqual([]);
  });
});

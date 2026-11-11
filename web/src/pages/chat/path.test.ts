import { describe, expect, it } from "vitest";
import { chatIdFromPath, chatPath, isChatPath, isNewChatPath } from "./path";

describe("chat paths", () => {
  it("recognises the page and its conversations only", () => {
    expect(isChatPath("/chat")).toBe(true);
    expect(isChatPath("/chat/01JC7Z3K9Q8V5T2M4N6P8R0S1T")).toBe(true);
    expect(isChatPath("/chats")).toBe(false);
    expect(isChatPath("/agent-runs/chat")).toBe(false);
  });

  it("builds the path of a conversation", () => {
    expect(chatPath()).toBe("/chat");
    expect(chatPath(null)).toBe("/chat");
    expect(chatPath("01JC")).toBe("/chat/01JC");
  });

  it("reads the conversation id, but not the new-chat view", () => {
    expect(chatIdFromPath("/chat/01JC")).toBe("01JC");
    expect(chatIdFromPath("/chat/01JC/")).toBe("01JC");
    expect(chatIdFromPath("/chat")).toBeNull();
    expect(chatIdFromPath("/chat/new")).toBeNull();
    expect(isNewChatPath("/chat/new")).toBe(true);
    expect(isNewChatPath("/chat/01JC")).toBe(false);
  });
});

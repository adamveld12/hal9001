import { describe, expect, it } from "vitest";
import { defaultTitle } from "./routeTitle";

describe("defaultTitle", () => {
  it("names list and detail pages", () => {
    expect(defaultTitle("/flows")).toBe("Flows");
    expect(defaultTitle("/flows/triage")).toBe("Flow");
    expect(defaultTitle("/flows/triage/versions/abc")).toBe("Flow version");
    expect(defaultTitle("/inbox/approval/01H")).toBe("Approval");
    expect(defaultTitle("/chat")).toBe("Chat");
    expect(defaultTitle("/chat/new")).toBe("New chat");
    expect(defaultTitle("/chat/01JC7Z3K9Q8V5T2M4N6P8R0S1T")).toBe(
      "Conversation",
    );
    expect(defaultTitle("/settings/runtimes")).toBe("Runtimes");
  });

  it("ignores a trailing slash and falls back for unknown paths", () => {
    expect(defaultTitle("/sensors/")).toBe("Sensors");
    expect(defaultTitle("/nope")).toBe("hal9001");
  });
});

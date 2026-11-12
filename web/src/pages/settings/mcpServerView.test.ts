import { describe, expect, it } from "vitest";
import {
  changesSummary,
  connectLabel,
  filterTools,
  serversSummary,
  showsError,
} from "./mcpServerView";

describe("connectLabel", () => {
  it("offers Connect or Reconnect for OAuth servers only", () => {
    expect(connectLabel({ auth: "oauth", status: "disconnected" })).toBe(
      "Connect",
    );
    expect(
      connectLabel({ auth: "oauth_client", status: "needs_reconnect" }),
    ).toBe("Reconnect");
    expect(connectLabel({ auth: "oauth", status: "error" })).toBe("Reconnect");
    expect(connectLabel({ auth: "oauth", status: "connected" })).toBeNull();
    expect(connectLabel({ auth: "header", status: "disconnected" })).toBeNull();
    expect(connectLabel({ auth: "none", status: "error" })).toBeNull();
  });
});

describe("showsError", () => {
  it("shows the error for error and needs_reconnect only", () => {
    expect(showsError({ status: "error", error: "x" })).toBe(true);
    expect(showsError({ status: "needs_reconnect", error: "x" })).toBe(true);
    expect(showsError({ status: "connected", error: "x" })).toBe(false);
    expect(showsError({ status: "error" })).toBe(false);
  });
});

describe("changesSummary", () => {
  it("counts non-empty groups", () => {
    expect(
      changesSummary({ added: ["a", "b"], removed: [], changed: ["c"] }),
    ).toBe("2 added, 1 changed");
    expect(changesSummary({ added: [], removed: ["x"], changed: [] })).toBe(
      "1 removed",
    );
    expect(changesSummary({ added: [], removed: [], changed: [] })).toBe(
      "No changes",
    );
  });
});

describe("filterTools", () => {
  const tools = [
    { name: "search", description: "Find pages", input_schema: {} },
    { name: "fetch", title: "Get Page", description: "x", input_schema: {} },
  ];
  it("matches name, title and description case-insensitively", () => {
    expect(filterTools(tools, "SEARCH").map((t) => t.name)).toEqual(["search"]);
    expect(filterTools(tools, "get page").map((t) => t.name)).toEqual([
      "fetch",
    ]);
    expect(filterTools(tools, "pages").map((t) => t.name)).toEqual(["search"]);
    expect(filterTools(tools, "  ")).toHaveLength(2);
  });
});

describe("serversSummary", () => {
  it("lists names and statuses", () => {
    expect(
      serversSummary([
        { name: "a", status: "connected" },
        { name: "b", status: "unknown" },
      ]),
    ).toBe("a: Connected, b: Not tested");
  });
});

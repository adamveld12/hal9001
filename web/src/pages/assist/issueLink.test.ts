import { describe, expect, it } from "vitest";
import type { AssistMessage } from "../../api/assist";
import { isGitHubNewIssueUrl, parseIssueLink } from "./issueLink";

type ToolCall = NonNullable<AssistMessage["tool_call"]>;

const URL_OK =
  "https://github.com/adamveld12/hal9001/issues/new?title=Flow+fails&body=Steps%3A%0A1.+Run&labels=bug";

function call(over: Partial<ToolCall> = {}, text?: string): ToolCall {
  return {
    tool_call_id: "c1",
    title: "mcp__hal__github_issue_link",
    kind: "other",
    status: "completed",
    raw_input: {},
    content: [
      {
        type: "content",
        content: {
          type: "text",
          text:
            text ??
            JSON.stringify({
              url: URL_OK,
              repo: "adamveld12/hal9001",
              title: "Flow fails",
              body: "Steps:\n1. Run\n\nFull body that is longer",
              truncated: false,
            }),
        },
      },
    ],
    ...over,
  } as ToolCall;
}

describe("parseIssueLink", () => {
  it("reads the link, repo, title and full body of a completed call", () => {
    expect(parseIssueLink(call())).toEqual({
      url: URL_OK,
      repo: "adamveld12/hal9001",
      title: "Flow fails",
      body: "Steps:\n1. Run\n\nFull body that is longer",
      truncated: false,
    });
  });

  it("recognizes the tool under the titles runtimes give it", () => {
    for (const title of [
      "mcp__hal__github_issue_link",
      "mcp__hal_github_issue_link",
      "hal:github_issue_link",
      "hal.github_issue_link",
      "github_issue_link",
    ]) {
      expect(parseIssueLink(call({ title }))).not.toBeNull();
    }
    expect(parseIssueLink(call({ title: "mcp__hal__draft_write" }))).toBeNull();
  });

  it("shows nothing until the call completed successfully", () => {
    for (const status of ["pending", "in_progress", "failed"]) {
      expect(parseIssueLink(call({ status }))).toBeNull();
    }
  });

  it("falls back to the link's own body and title when the result lacks them", () => {
    const text = JSON.stringify({ url: URL_OK });
    const link = parseIssueLink(call({}, text));
    expect(link?.body).toBe("Steps:\n1. Run");
    expect(link?.title).toBe("Flow fails");
    expect(link?.repo).toBe("adamveld12/hal9001");
  });

  it("keeps the truncated flag so the card can say the link holds only the start", () => {
    const text = JSON.stringify({ url: URL_OK, body: "x", truncated: true });
    expect(parseIssueLink(call({}, text))?.truncated).toBe(true);
  });

  it("finds the object when a runtime wraps the result in text", () => {
    const text = `Result:\n\`\`\`json\n${JSON.stringify({ url: URL_OK, body: "b" })}\n\`\`\``;
    expect(parseIssueLink(call({}, text))?.url).toBe(URL_OK);
  });

  it("refuses links that do not open GitHub's new-issue page", () => {
    for (const url of [
      "https://evil.example/adamveld12/hal9001/issues/new?title=x",
      "http://github.com/adamveld12/hal9001/issues/new?title=x",
      "https://github.com.evil.example/a/b/issues/new",
      "https://user:pw@github.com/a/b/issues/new",
      "https://github.com/a/b/pull/1",
      "javascript:alert(1)",
      "https://github.com/a/b/issues/new/../../../../login",
    ]) {
      expect(parseIssueLink(call({}, JSON.stringify({ url })))).toBeNull();
    }
  });

  it("returns null for a result that is not JSON", () => {
    expect(parseIssueLink(call({}, "all done"))).toBeNull();
    expect(parseIssueLink(call({ content: [] }))).toBeNull();
  });
});

describe("isGitHubNewIssueUrl", () => {
  it("accepts other repositories on github.com", () => {
    expect(
      isGitHubNewIssueUrl("https://github.com/octo-org/octo-repo/issues/new"),
    ).toBe(true);
    expect(
      isGitHubNewIssueUrl(
        "https://github.com/octo-org/octo-repo/issues/new?labels=a%2Cb",
      ),
    ).toBe(true);
  });
});

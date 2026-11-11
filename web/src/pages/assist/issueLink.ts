// The github_issue_link tool result in the assist conversation: finding it in a tool call and reading the link and
// the body the chat offers to open and copy.

import type { AssistMessage } from "../../api/assist";
import { toolContent } from "../runs/transcript";

type ToolCall = NonNullable<AssistMessage["tool_call"]>;

export interface IssueLink {
  /** The new-issue URL on GitHub, ready to open. */
  url: string;
  /** owner/name of the repository. */
  repo: string;
  title: string;
  /** The complete body, to copy. It can be longer than the link's. */
  body: string;
  /** The link carries only the start of the body. */
  truncated: boolean;
}

/** Runtimes title an MCP tool call mcp__hal__<tool>, hal:<tool>, hal.<tool> and similar; the tool name ends the title. */
export function isIssueLinkCall(call: ToolCall): boolean {
  return /(^|[^a-z0-9])github_issue_link$/i.test(call.title.trim());
}

const NEW_ISSUE = /^\/[^/]+\/[^/]+\/issues\/new$/;

/** True for a link that opens GitHub's new-issue page: https, github.com, no credentials. */
export function isGitHubNewIssueUrl(raw: string): boolean {
  try {
    const u = new URL(raw);
    return (
      u.protocol === "https:" &&
      u.host === "github.com" &&
      u.username === "" &&
      u.password === "" &&
      NEW_ISSUE.test(u.pathname)
    );
  } catch {
    return false;
  }
}

function asRecord(v: unknown): Record<string, unknown> | null {
  return typeof v === "object" && v !== null && !Array.isArray(v)
    ? (v as Record<string, unknown>)
    : null;
}

function parseJSONObject(text: string): Record<string, unknown> | null {
  try {
    return asRecord(JSON.parse(text));
  } catch {
    // Some runtimes wrap the result in prose or a fence; take the outermost object.
    const from = text.indexOf("{");
    const to = text.lastIndexOf("}");
    if (from < 0 || to <= from) return null;
    try {
      return asRecord(JSON.parse(text.slice(from, to + 1)));
    } catch {
      return null;
    }
  }
}

/**
 * The link of a completed github_issue_link call, or null (another tool, still running, failed, or a result that does
 * not hold a GitHub new-issue link). Only github.com links are accepted: the text comes from a model.
 */
export function parseIssueLink(call: ToolCall): IssueLink | null {
  if (!isIssueLinkCall(call) || call.status !== "completed") return null;
  for (const item of toolContent(call.content)) {
    if (item.type !== "text") continue;
    const o = parseJSONObject(item.text);
    const url = typeof o?.url === "string" ? o.url : "";
    if (!o || !isGitHubNewIssueUrl(url)) continue;
    const u = new URL(url);
    // Untruncated links carry the whole body themselves, so the copy still works if the result's body was clipped.
    const body =
      typeof o.body === "string" ? o.body : (u.searchParams.get("body") ?? "");
    return {
      url,
      repo:
        typeof o.repo === "string"
          ? o.repo
          : u.pathname.split("/").slice(1, 3).join("/"),
      title:
        typeof o.title === "string"
          ? o.title
          : (u.searchParams.get("title") ?? ""),
      body,
      truncated: o.truncated === true,
    };
  }
  return null;
}

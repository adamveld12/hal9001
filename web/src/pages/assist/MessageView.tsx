import { Link } from "react-router";
import type { AssistMessage } from "../../api/assist";
import { Icon, type IconName } from "../../components/icons";
import { JsonView } from "../../components/JsonView";
import { Markdown } from "../../components/Markdown";
import { Badge } from "../../components/ui";
import { toolContent, toolTone } from "../runs/transcript";
import { ToolContentView } from "../runs/TranscriptView";
import { MessageApproval } from "../approvals/ConversationApprovals";
import { extractCommand, extractDiffs } from "../approvals/diff";
import { DiffView } from "../approvals/DiffView";
import { IssueLinkCard } from "./IssueLinkCard";
import { parseIssueLink } from "./issueLink";

const KIND_ICON: Record<string, IconName> = {
  read: "note",
  edit: "save",
  delete: "trash",
  move: "refresh",
  search: "search",
  execute: "command",
  think: "agent",
  fetch: "external",
};

type ToolCall = NonNullable<AssistMessage["tool_call"]>;

/** What a tool call does, as far as its input and content show it: command, diffs, and anything else as JSON. */
function ToolDetails({ call }: { call: ToolCall }) {
  const content = toolContent(call.content);
  const command = extractCommand(call.raw_input);
  const inputDiffs = content.some((c) => c.type === "diff")
    ? []
    : extractDiffs(call.raw_input);
  const hasInput = call.raw_input !== null && call.raw_input !== undefined;
  return (
    <div className="space-y-2">
      {command !== null && (
        <pre className="m-0 overflow-auto whitespace-pre-wrap break-words rounded-md border border-line bg-raised p-2 font-mono text-[11px]">
          $ {command}
        </pre>
      )}
      {inputDiffs.map((d, i) => (
        <DiffView key={i} diff={d} />
      ))}
      {content.map((c, i) => (
        <ToolContentView key={i} item={c} />
      ))}
      {hasInput && command === null && inputDiffs.length === 0 && (
        <JsonView value={call.raw_input} maxHeight="12rem" />
      )}
      {!hasInput && content.length === 0 && (
        <div className="text-muted">No details recorded.</div>
      )}
    </div>
  );
}

function ToolRow({
  message,
  call,
}: {
  message: AssistMessage;
  call: ToolCall;
}) {
  const needsApproval = message.approval_id !== null;
  const issue = parseIssueLink(call);
  return (
    <div className="space-y-1.5">
      {/* A call waiting for a decision opens, so the user reads what they are asked to allow. */}
      <details
        open={
          needsApproval &&
          (call.status === "pending" || call.status === "in_progress")
        }
        className="rounded-md border border-line bg-panel text-xs"
      >
        <summary className="flex cursor-pointer select-none items-center gap-2 px-2 py-1.5">
          <Icon
            name={KIND_ICON[call.kind] ?? "settings"}
            size={13}
            className="shrink-0 text-muted"
          />
          <span className="min-w-0 flex-1 truncate font-medium">
            {call.title}
          </span>
          <Badge tone={toolTone(call.status)}>
            {call.status.replaceAll("_", " ")}
          </Badge>
        </summary>
        <div className="border-t border-line p-2">
          <ToolDetails call={call} />
        </div>
      </details>
      {issue && <IssueLinkCard link={issue} />}
      {needsApproval && (
        <MessageApproval approvalId={message.approval_id as string} />
      )}
    </div>
  );
}

/** One conversation entry. `traceRunId` adds the "View trace" link under the last message of a turn. */
export function MessageView({
  message,
  traceRunId,
}: {
  message: AssistMessage;
  traceRunId: string | undefined;
}) {
  let body;
  switch (message.role) {
    case "user":
      return (
        <div className="ml-8 whitespace-pre-wrap break-words rounded-lg bg-accent/10 px-3 py-2 text-[13px]">
          {message.text}
        </div>
      );
    case "assistant":
      body = <Markdown>{message.text ?? ""}</Markdown>;
      break;
    case "tool":
      body = message.tool_call ? (
        <ToolRow message={message} call={message.tool_call} />
      ) : null;
      break;
    case "system":
      body = (
        <div
          role="alert"
          className="rounded-md border border-bad/30 bg-bad/5 px-2 py-1.5 text-xs text-bad"
        >
          {message.text}
        </div>
      );
      break;
  }
  return (
    <div>
      {body}
      {traceRunId && (
        <Link
          to={`/agent-runs/${traceRunId}`}
          className="mt-1 inline-block text-[11px] text-muted hover:text-accent hover:underline"
        >
          View trace
        </Link>
      )}
    </div>
  );
}

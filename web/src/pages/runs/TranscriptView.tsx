import type { ReactNode } from "react";
import { JsonView } from "../../components/JsonView";
import { Markdown } from "../../components/Markdown";
import { Time } from "../../components/Time";
import { Badge } from "../../components/ui";
import { DiffView } from "./DiffView";
import { SectionTitle } from "./Meta";
import {
  toolTone,
  type PlanItem,
  type ToolContent,
  type ToolItem,
  type TranscriptItem,
} from "./transcript";

function Collapsible({
  summary,
  children,
  open,
}: {
  summary: ReactNode;
  children: ReactNode;
  open?: boolean;
}) {
  return (
    <details
      open={open}
      className="rounded-md border border-line bg-panel text-xs"
    >
      <summary className="flex cursor-pointer select-none items-center gap-2 px-2.5 py-1.5">
        {summary}
      </summary>
      <div className="space-y-2 border-t border-line p-2.5">{children}</div>
    </details>
  );
}

function hasValue(v: unknown): boolean {
  return v !== undefined && v !== null && v !== "";
}

function ToolContentView({ item }: { item: ToolContent }) {
  switch (item.type) {
    case "text":
      return (
        <pre className="m-0 max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md border border-line bg-raised p-2 font-mono text-[11px]">
          {item.text}
        </pre>
      );
    case "diff":
      return (
        <DiffView
          path={item.path}
          oldText={item.oldText}
          newText={item.newText}
        />
      );
    case "terminal":
      return (
        <div className="font-mono text-[11px] text-muted">
          terminal {item.terminalId}
        </div>
      );
    case "json":
      return <JsonView value={item.value} maxHeight="14rem" />;
  }
}

function ToolCall({ item }: { item: ToolItem }) {
  return (
    <Collapsible
      summary={
        <>
          <span className="min-w-0 flex-1 truncate font-medium">
            {item.title}
          </span>
          {item.toolKind && <Badge>{item.toolKind}</Badge>}
          {item.status && (
            <Badge tone={toolTone(item.status)}>
              {item.status.replaceAll("_", " ")}
            </Badge>
          )}
          <Time ts={item.ts} />
        </>
      }
    >
      {item.locations.length > 0 && (
        <div className="font-mono text-[11px] text-muted">
          {item.locations.join(", ")}
        </div>
      )}
      {hasValue(item.input) && (
        <div>
          <SectionTitle>Input</SectionTitle>
          <JsonView value={item.input} maxHeight="14rem" />
        </div>
      )}
      {item.content.length > 0 && (
        <div className="space-y-2">
          <SectionTitle>Content</SectionTitle>
          {item.content.map((c, i) => (
            <ToolContentView key={i} item={c} />
          ))}
        </div>
      )}
      {hasValue(item.output) && (
        <div>
          <SectionTitle>Output</SectionTitle>
          <JsonView value={item.output} maxHeight="14rem" />
        </div>
      )}
      {!hasValue(item.input) &&
        !hasValue(item.output) &&
        item.content.length === 0 && (
          <div className="text-muted">No details recorded.</div>
        )}
    </Collapsible>
  );
}

function Plan({ item }: { item: PlanItem }) {
  return (
    <Collapsible
      open
      summary={
        <>
          <span className="font-medium">Plan</span>
          <span className="text-muted">
            {item.entries.filter((e) => e.status === "completed").length}/
            {item.entries.length} done
          </span>
        </>
      }
    >
      <ul className="m-0 list-none space-y-1 p-0">
        {item.entries.map((e, i) => (
          <li key={i} className="flex items-start gap-2">
            <Badge tone={toolTone(e.status)}>
              {(e.status ?? "pending").replaceAll("_", " ")}
            </Badge>
            <span className="min-w-0 flex-1">{e.content}</span>
            {e.priority && <span className="text-muted">{e.priority}</span>}
          </li>
        ))}
      </ul>
    </Collapsible>
  );
}

function TranscriptEntry({ item }: { item: TranscriptItem }) {
  switch (item.type) {
    case "message":
      return (
        <div>
          <div className="mb-0.5 flex items-center gap-2 text-[11px] text-muted">
            Agent <Time ts={item.ts} />
          </div>
          <Markdown>{item.text}</Markdown>
        </div>
      );
    case "user":
      return (
        <div className="rounded-md bg-raised px-2.5 py-1.5">
          <div className="mb-0.5 text-[11px] text-muted">Prompt</div>
          <Markdown>{item.text}</Markdown>
        </div>
      );
    case "thought":
      return (
        <Collapsible summary={<span className="text-muted">Thinking</span>}>
          <div className="whitespace-pre-wrap break-words italic text-muted">
            {item.text}
          </div>
        </Collapsible>
      );
    case "tool":
      return <ToolCall item={item} />;
    case "plan":
      return <Plan item={item} />;
    case "other":
      return (
        <Collapsible summary={<span className="text-muted">{item.kind}</span>}>
          <JsonView value={item.payload} maxHeight="12rem" />
        </Collapsible>
      );
  }
}

export function TranscriptView({
  items,
}: {
  items: readonly TranscriptItem[];
}) {
  return (
    <div className="space-y-2">
      {items.map((item) => (
        <TranscriptEntry key={item.key} item={item} />
      ))}
    </div>
  );
}

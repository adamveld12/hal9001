import type { MouseEvent } from "react";
import { Link } from "react-router";
import type { PromptSummary } from "../../api/prompts";
import { Badge, Table, Td, Th } from "../../components/ui";
import { cx } from "../../lib/cx";
import { promptRow, type PromptRow } from "./view";

type Guard = (e: MouseEvent) => void;

function OutputBadge({ output }: { output: PromptRow["output"] }) {
  return <Badge tone={output === "note" ? "info" : "muted"}>{output}</Badge>;
}

function Findings({ row }: { row: PromptRow }) {
  if (row.broken) return <Badge tone="bad">does not parse</Badge>;
  if (row.errors === 0 && row.warnings === 0) {
    return <span className="text-muted">–</span>;
  }
  return (
    <span className="flex gap-1">
      {row.errors > 0 && (
        <Badge tone="bad">
          {row.errors} error{row.errors > 1 ? "s" : ""}
        </Badge>
      )}
      {row.warnings > 0 && (
        <Badge tone="warn">
          {row.warnings} warning{row.warnings > 1 ? "s" : ""}
        </Badge>
      )}
    </span>
  );
}

/** Full-width table, shown when no prompt is selected. */
export function PromptTable({
  prompts,
  guard,
}: {
  prompts: readonly PromptSummary[];
  guard: Guard;
}) {
  return (
    <Table>
      <thead>
        <tr>
          <Th>Prompt</Th>
          <Th>Description</Th>
          <Th>Output</Th>
          <Th>Problems</Th>
        </tr>
      </thead>
      <tbody>
        {prompts.map((p) => {
          const r = promptRow(p);
          return (
            <tr key={r.name} className="hover:bg-raised">
              <Td>
                <Link
                  to={`/prompts/${r.name}`}
                  onClick={guard}
                  className="font-mono text-accent hover:underline"
                >
                  {r.name}
                </Link>
              </Td>
              <Td className="text-muted">{r.description || "–"}</Td>
              <Td>
                <OutputBadge output={r.output} />
              </Td>
              <Td>
                <Findings row={r} />
              </Td>
            </tr>
          );
        })}
      </tbody>
    </Table>
  );
}

/** Narrow list next to the editor. */
export function PromptSidebar({
  prompts,
  selected,
  guard,
}: {
  prompts: readonly PromptSummary[];
  selected: string;
  guard: Guard;
}) {
  return (
    <ul className="divide-y divide-line">
      {prompts.map((p) => {
        const r = promptRow(p);
        return (
          <li key={r.name}>
            <Link
              to={`/prompts/${r.name}`}
              onClick={guard}
              aria-current={r.name === selected ? "page" : undefined}
              className={cx(
                "block space-y-0.5 px-3 py-2 text-xs hover:bg-raised",
                r.name === selected && "bg-raised",
              )}
            >
              <span className="flex items-center gap-2">
                <span className="min-w-0 truncate font-mono font-medium">
                  {r.name}
                </span>
                <span className="ml-auto shrink-0">
                  <OutputBadge output={r.output} />
                </span>
              </span>
              {r.description && (
                <span className="block truncate text-muted">
                  {r.description}
                </span>
              )}
              {(r.broken || r.errors > 0) && (
                <span className="block text-bad">
                  {r.broken
                    ? "does not parse"
                    : `${r.errors} error${r.errors > 1 ? "s" : ""}`}
                </span>
              )}
            </Link>
          </li>
        );
      })}
    </ul>
  );
}

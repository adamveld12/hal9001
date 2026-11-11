import { Icon } from "../../components/icons";
import { cx } from "../../lib/cx";
import { EntryRow } from "../runs/SandboxLog";
import type { SandboxLogEntry } from "../runs/sandboxEntries";
import { sandboxGroupHasProblem, sandboxGroupLabel } from "./threadItems";

/** Sandbox activity between two messages: one compact row that opens to the log entries. */
export function SandboxGroup({
  entries,
}: {
  entries: readonly SandboxLogEntry[];
}) {
  const problem = sandboxGroupHasProblem(entries);
  return (
    <details
      aria-label="Sandbox activity"
      className="rounded-md border border-line bg-panel text-xs"
    >
      <summary className="flex cursor-pointer select-none items-center gap-2 px-2 py-1.5">
        <Icon
          name="box"
          size={13}
          className={cx("shrink-0", problem ? "text-bad" : "text-muted")}
        />
        <span className="shrink-0 text-muted">Sandbox</span>
        <span className="min-w-0 flex-1 truncate">
          {sandboxGroupLabel(entries)}
        </span>
      </summary>
      <ul className="m-0 list-none border-t border-line p-0">
        {entries.map((e) => (
          <EntryRow key={e.id} e={e} sessionLabel={null} />
        ))}
      </ul>
    </details>
  );
}

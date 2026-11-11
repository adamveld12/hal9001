// Pieces of a chat that the drawer and the Chat page both show.

import type { ReactNode } from "react";
import type { AssistTargetKind } from "../../api/assist";
import { useRuntimeOptions } from "../../api/settings";
import { Select, TextInput } from "../../components/ui";
import { runtimeOptions } from "../../lib/runtimeStatus";
import { useAssist } from "./context";
import { starterPrompts } from "./targets";

/** Title row of a chat panel with the status badge, extra rows and the action buttons. */
export function Header({
  title,
  badge,
  children,
  actions,
}: {
  title: ReactNode;
  badge?: ReactNode;
  children?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="space-y-2 border-b border-line px-3 py-2">
      <div className="flex items-center gap-2">
        <h2 className="m-0 min-w-0 flex-1 truncate text-[13px] font-semibold">
          {title}
        </h2>
        {badge}
      </div>
      {children}
      {actions && (
        <div className="flex flex-wrap items-center gap-2">{actions}</div>
      )}
    </header>
  );
}

export function RuntimeControls({
  runtime,
  model,
  locked,
  onRuntime,
  onModel,
}: {
  runtime: string;
  model: string;
  locked: boolean;
  onRuntime?: (r: string) => void;
  onModel?: (m: string) => void;
}) {
  const runtimes = useRuntimeOptions();
  const options = runtimeOptions(runtimes.data ?? [], runtime);
  return (
    <div className="flex gap-2">
      <Select
        aria-label="Runtime"
        className="w-48"
        value={runtime}
        disabled={locked || options.length === 0}
        title={
          locked
            ? "The runtime is fixed once the conversation has started."
            : undefined
        }
        onChange={(e) => onRuntime?.(e.target.value)}
      >
        {options.length === 0 && <option value="">no runtimes</option>}
        {options.map((o) => (
          <option key={o.name} value={o.name}>
            {o.label}
          </option>
        ))}
      </Select>
      <TextInput
        aria-label="Model"
        className="min-w-0 flex-1"
        placeholder="model (optional)"
        value={model}
        disabled={locked}
        onChange={(e) => onModel?.(e.target.value)}
      />
    </div>
  );
}

export function StarterPrompts({
  kind,
  targetId,
}: {
  kind: AssistTargetKind;
  targetId: string | null;
}) {
  const { seedText } = useAssist();
  return (
    <div className="space-y-1.5">
      <div className="text-[11px] font-medium text-muted">Try asking</div>
      <div className="flex flex-wrap gap-1.5">
        {starterPrompts(kind, targetId).map((p) => (
          <button
            key={p}
            type="button"
            className="rounded-full border border-line bg-panel px-2.5 py-1 text-xs hover:bg-raised"
            onClick={() => seedText(p)}
          >
            {p}
          </button>
        ))}
      </div>
    </div>
  );
}

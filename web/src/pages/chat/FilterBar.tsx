import { useEffect, useRef, useState } from "react";
import { useFlows } from "../../api/flows";
import { useRuntimeNames } from "../../api/settings";
import { Button, Select, TextInput } from "../../components/ui";
import { cx } from "../../lib/cx";
import {
  hasActiveFilters,
  NO_CHAT_FILTERS,
  Q_DEBOUNCE_MS,
  SOURCES,
  STATUSES,
  type ChatFilters,
} from "./filters";
import { statusLabel } from "./status";

/** A choice that may no longer exist (a deleted flow in an old link) stays selectable so the filter shows. */
function withCurrent(options: readonly string[], current: string): string[] {
  return current !== "" && !options.includes(current)
    ? [current, ...options]
    : [...options];
}

/**
 * The list's filters. Source is a segmented control; the others are selects and
 * two date inputs. Text search waits for typing to pause before it changes the
 * URL, so each keystroke does not start a request.
 */
export function FilterBar({
  filters,
  onChange,
}: {
  filters: ChatFilters;
  onChange: (next: ChatFilters) => void;
}) {
  const flows = useFlows();
  const runtimes = useRuntimeNames();
  const [text, setText] = useState(filters.q);
  const [seenQ, setSeenQ] = useState(filters.q);
  // The URL changed from elsewhere (Clear, a link): show its text.
  if (filters.q !== seenQ) {
    setSeenQ(filters.q);
    setText(filters.q);
  }
  const latest = useRef({ filters, onChange });
  useEffect(() => {
    latest.current = { filters, onChange };
  });
  useEffect(() => {
    if (text === filters.q) return;
    const t = setTimeout(
      () => latest.current.onChange({ ...latest.current.filters, q: text }),
      Q_DEBOUNCE_MS,
    );
    return () => clearTimeout(t);
  }, [text, filters.q]);

  const set = (patch: Partial<ChatFilters>) =>
    onChange({ ...filters, ...patch });
  const flowIds = withCurrent(
    (flows.data ?? []).map((f) => f.id),
    filters.flow,
  );
  const runtimeNames = withCurrent(runtimes.data ?? [], filters.runtime);

  return (
    <div className="space-y-2 border-b border-line p-3">
      <div role="group" aria-label="Source" className="flex gap-1">
        {SOURCES.map((s) => (
          <button
            key={s.value}
            type="button"
            aria-pressed={filters.source === s.value}
            className={cx(
              "h-7 flex-1 rounded-md border px-2 text-xs font-medium",
              filters.source === s.value
                ? "border-accent/50 bg-accent/10 text-accent"
                : "border-line hover:bg-raised",
            )}
            onClick={() => set({ source: s.value })}
          >
            {s.label}
          </button>
        ))}
      </div>
      <TextInput
        type="search"
        aria-label="Search conversations"
        placeholder="Search title, text, flow, node"
        maxLength={200}
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      <div className="grid grid-cols-2 gap-2">
        <Select
          aria-label="Flow"
          value={filters.flow}
          onChange={(e) => set({ flow: e.target.value })}
        >
          <option value="">Any flow</option>
          {flowIds.map((id) => (
            <option key={id} value={id}>
              {id}
            </option>
          ))}
        </Select>
        <Select
          aria-label="Runtime"
          value={filters.runtime}
          onChange={(e) => set({ runtime: e.target.value })}
        >
          <option value="">Any runtime</option>
          {runtimeNames.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </Select>
        <Select
          aria-label="Status"
          className="col-span-2"
          value={filters.status}
          onChange={(e) =>
            set({ status: e.target.value as ChatFilters["status"] })
          }
        >
          <option value="">Any status</option>
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {statusLabel(s)}
            </option>
          ))}
        </Select>
        <label className="space-y-0.5 text-[11px] text-muted">
          Since
          <TextInput
            type="date"
            value={filters.since}
            max={filters.until || undefined}
            onChange={(e) => set({ since: e.target.value })}
          />
        </label>
        <label className="space-y-0.5 text-[11px] text-muted">
          Until
          <TextInput
            type="date"
            value={filters.until}
            min={filters.since || undefined}
            onChange={(e) => set({ until: e.target.value })}
          />
        </label>
      </div>
      {hasActiveFilters(filters) && (
        <Button variant="ghost" onClick={() => onChange(NO_CHAT_FILTERS)}>
          Clear filters
        </Button>
      )}
    </div>
  );
}

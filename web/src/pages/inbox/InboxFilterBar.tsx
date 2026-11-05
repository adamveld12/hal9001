import { useEffect, useState } from "react";
import { PRIORITIES, type InboxSort, type Priority } from "../../api/inbox";
import { Button, Select, TextInput } from "../../components/ui";
import {
  activeFilterCount,
  DEFAULT_FILTERS,
  type DryRunFilter,
  type InboxFilters,
} from "./filters";
import { PRIORITY_LABEL } from "./model";

const SEARCH_DEBOUNCE_MS = 250;

export function InboxFilterBar({
  filters,
  onChange,
}: {
  filters: InboxFilters;
  onChange: (next: InboxFilters) => void;
}) {
  // The box keeps what was typed; the URL (and the query) follows after a pause.
  const [text, setText] = useState(filters.q);
  const [seenQ, setSeenQ] = useState(filters.q);
  if (seenQ !== filters.q) {
    // The URL changed from outside (clear filters, back button).
    setSeenQ(filters.q);
    setText(filters.q);
  }
  useEffect(() => {
    if (text === filters.q) return;
    const t = setTimeout(
      () => onChange({ ...filters, q: text }),
      SEARCH_DEBOUNCE_MS,
    );
    return () => clearTimeout(t);
  }, [text, filters, onChange]);

  const toggle = (key: "unread" | "starred" | "archived", label: string) => (
    <Button
      variant={filters[key] ? "primary" : "default"}
      aria-pressed={filters[key]}
      onClick={() => onChange({ ...filters, [key]: !filters[key] })}
    >
      {label}
    </Button>
  );

  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2">
      {toggle("unread", "Unread")}
      {toggle("starred", "Starred")}
      {toggle("archived", "Archived")}
      <Select
        aria-label="Minimum priority"
        className="w-auto"
        value={filters.priority ?? ""}
        onChange={(e) =>
          onChange({
            ...filters,
            priority: (e.target.value || null) as Priority | null,
          })
        }
      >
        <option value="">Any priority</option>
        {PRIORITIES.slice(1).map((p) => (
          <option key={p} value={p}>
            {PRIORITY_LABEL[p]} and above
          </option>
        ))}
      </Select>
      <Select
        aria-label="Dry-run items"
        className="w-auto"
        value={filters.dryRun}
        onChange={(e) =>
          onChange({ ...filters, dryRun: e.target.value as DryRunFilter })
        }
      >
        <option value="any">Dry-run: all</option>
        <option value="hide">Hide dry-run</option>
        <option value="only">Only dry-run</option>
      </Select>
      <Select
        aria-label="Sort"
        className="w-auto"
        value={filters.sort}
        onChange={(e) =>
          onChange({ ...filters, sort: e.target.value as InboxSort })
        }
      >
        <option value="priority">Priority, then time</option>
        <option value="time">Time</option>
      </Select>
      <TextInput
        aria-label="Search notes"
        type="search"
        placeholder="Search title and summary"
        className="w-56"
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      {activeFilterCount(filters) > 0 && (
        <Button
          variant="ghost"
          onClick={() => {
            setText("");
            onChange({ ...DEFAULT_FILTERS, sort: filters.sort });
          }}
        >
          Clear filters
        </Button>
      )}
    </div>
  );
}

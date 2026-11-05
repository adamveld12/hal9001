import { useMemo, useState, type ReactNode } from "react";
import {
  buildColumns,
  defaultVisible,
  describeCell,
  filterRows,
  toRows,
  type ItemRow,
  type RawItem,
} from "../lib/sensorItems";
import { cx } from "../lib/cx";
import { JsonView } from "./JsonView";
import { Button, TextInput } from "./ui";

type View = "table" | "json" | "raw";

const CELL_CHARS = 200;
const HOVER_CHARS = 2000;
const PAGE_ROWS = 200;

function cut(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max)}…` : s;
}

function Segmented({
  view,
  onChange,
  raw,
}: {
  view: View;
  onChange: (v: View) => void;
  raw: boolean;
}) {
  const views: View[] = raw ? ["table", "json", "raw"] : ["table", "json"];
  return (
    <div
      role="group"
      aria-label="View"
      className="inline-flex overflow-hidden rounded-md border border-line text-xs"
    >
      {views.map((v) => (
        <button
          key={v}
          type="button"
          aria-pressed={view === v}
          className={cx(
            "h-7 px-2.5 capitalize",
            view === v ? "bg-raised font-medium" : "bg-panel text-muted",
          )}
          onClick={() => onChange(v)}
        >
          {v === "json" ? "JSON" : v}
        </button>
      ))}
    </div>
  );
}

function KeyCell({ row }: { row: ItemRow }) {
  if (row.keyError) {
    return (
      <span className="text-bad" title={row.keyError}>
        error: {cut(row.keyError, 80)}
      </span>
    );
  }
  if (row.dedupKey === null || row.dedupKey === undefined) {
    return <span className="text-muted">–</span>;
  }
  return <span title={row.dedupKey}>{cut(row.dedupKey, CELL_CHARS)}</span>;
}

function ColumnPicker({
  columns,
  isVisible,
  onToggle,
  onAll,
  onDefault,
}: {
  columns: readonly { path: string; count: number }[];
  isVisible: (path: string) => boolean;
  onToggle: (path: string, on: boolean) => void;
  onAll: () => void;
  onDefault: () => void;
}) {
  const shown = columns.filter((c) => isVisible(c.path)).length;
  return (
    <details className="relative">
      <summary className="inline-flex h-7 cursor-pointer select-none items-center rounded-md border border-line bg-panel px-2.5 text-xs font-medium hover:bg-raised">
        Columns {shown}/{columns.length}
      </summary>
      <div className="absolute right-0 z-20 mt-1 w-64 rounded-md border border-line bg-panel p-2 shadow-lg">
        <div className="mb-1 flex gap-2">
          <Button onClick={onAll}>All</Button>
          <Button onClick={onDefault}>Default</Button>
        </div>
        <ul className="m-0 max-h-64 list-none space-y-0.5 overflow-auto p-0">
          {columns.map((c) => (
            <li key={c.path}>
              <label className="flex cursor-pointer items-center gap-2 text-xs">
                <input
                  type="checkbox"
                  checked={isVisible(c.path)}
                  onChange={(e) => onToggle(c.path, e.target.checked)}
                />
                <span className="min-w-0 flex-1 truncate font-mono">
                  {c.path}
                </span>
                <span className="text-muted">{c.count}</span>
              </label>
            </li>
          ))}
        </ul>
      </div>
    </details>
  );
}

/**
 * Sensor items as a table: one row per item, one column per (flattened) field.
 * Rows expand to the pretty-printed item; the JSON view keeps raw access.
 * Items from the assist test also carry the daemon's dedup key (first column).
 */
export function SensorItemsTable({
  items,
  rawView,
  notice,
  maxHeight = "28rem",
}: {
  items: readonly RawItem[];
  /** Content of an extra "Raw" view, for callers that have the source text. */
  rawView?: ReactNode;
  /** Shown above the table, for example a note that the output was cut. */
  notice?: ReactNode;
  maxHeight?: string;
}) {
  const [view, setView] = useState<View>("table");
  const [query, setQuery] = useState("");
  const [overrides, setOverrides] = useState<Record<string, boolean>>({});
  const [open, setOpen] = useState<ReadonlySet<number>>(new Set());
  const [limit, setLimit] = useState(PAGE_ROWS);

  const rows = useMemo(() => toRows(items), [items]);
  const columns = useMemo(() => buildColumns(rows.map((r) => r.flat)), [rows]);
  const defaults = useMemo(() => defaultVisible(columns), [columns]);
  const hasKeys = rows.some(
    (r) => r.dedupKey !== undefined || r.keyError !== undefined,
  );
  const isVisible = (path: string) => overrides[path] ?? defaults.has(path);
  const shownColumns = columns.filter((c) => isVisible(c.path));
  const filtered = useMemo(() => filterRows(rows, query), [rows, query]);
  const page = filtered.slice(0, limit);
  const span = shownColumns.length + 1 + (hasKeys ? 1 : 0);

  const toggleRow = (i: number) =>
    setOpen((cur) => {
      const next = new Set(cur);
      if (!next.delete(i)) next.add(i);
      return next;
    });

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Segmented view={view} onChange={setView} raw={rawView !== undefined} />
        <span className="text-xs text-muted">
          {query.trim() === ""
            ? `${rows.length} item${rows.length === 1 ? "" : "s"}`
            : `${filtered.length} of ${rows.length} items`}
        </span>
        {view === "table" && rows.length > 0 && (
          <div className="ml-auto flex items-center gap-2">
            <TextInput
              aria-label="Filter items"
              placeholder="Filter…"
              className="w-44"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <ColumnPicker
              columns={columns}
              isVisible={isVisible}
              onToggle={(path, on) =>
                setOverrides((o) => ({ ...o, [path]: on }))
              }
              onAll={() =>
                setOverrides(
                  Object.fromEntries(columns.map((c) => [c.path, true])),
                )
              }
              onDefault={() => setOverrides({})}
            />
          </div>
        )}
      </div>
      {notice}
      {view === "raw" && rawView}
      {view === "json" && (
        <JsonView value={items.map((i) => i.item)} maxHeight={maxHeight} />
      )}
      {view === "table" &&
        (rows.length === 0 ? (
          <div className="text-xs text-muted">No items.</div>
        ) : (
          <div
            className="overflow-auto rounded-md border border-line"
            style={{ maxHeight }}
          >
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr>
                  <th className="sticky top-0 z-10 w-10 border-b border-line bg-bg px-2 py-1.5 text-left text-[11px] font-medium text-muted">
                    #
                  </th>
                  {hasKeys && (
                    <th className="sticky top-0 z-10 whitespace-nowrap border-b border-line bg-bg px-3 py-1.5 text-left text-[11px] font-medium text-muted">
                      dedup key
                    </th>
                  )}
                  {shownColumns.map((c) => (
                    <th
                      key={c.path}
                      title={`${c.path} (${c.count} of ${rows.length} items)`}
                      className="sticky top-0 z-10 whitespace-nowrap border-b border-line bg-bg px-3 py-1.5 text-left font-mono text-[11px] font-medium text-muted"
                    >
                      {c.path}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {page.map((row) => {
                  const expanded = open.has(row.index);
                  return (
                    <ItemRowView
                      key={row.index}
                      row={row}
                      expanded={expanded}
                      hasKeys={hasKeys}
                      columns={shownColumns.map((c) => c.path)}
                      span={span}
                      onToggle={() => toggleRow(row.index)}
                    />
                  );
                })}
              </tbody>
            </table>
            {filtered.length === 0 && (
              <div className="p-2 text-xs text-muted">
                No item matches “{query}”.
              </div>
            )}
            {filtered.length > page.length && (
              <div className="p-2">
                <Button onClick={() => setLimit((l) => l + PAGE_ROWS)}>
                  Show more ({filtered.length - page.length} left)
                </Button>
              </div>
            )}
          </div>
        ))}
    </div>
  );
}

function ItemRowView({
  row,
  expanded,
  hasKeys,
  columns,
  span,
  onToggle,
}: {
  row: ItemRow;
  expanded: boolean;
  hasKeys: boolean;
  columns: readonly string[];
  span: number;
  onToggle: () => void;
}) {
  const cell = "border-b border-line px-3 py-1.5 align-top";
  return (
    <>
      <tr
        className="cursor-pointer hover:bg-raised"
        onClick={onToggle}
        data-expanded={expanded}
      >
        <td className="border-b border-line px-2 py-1.5 align-top">
          <button
            type="button"
            aria-expanded={expanded}
            aria-label={`${expanded ? "Collapse" : "Expand"} item ${row.index + 1}`}
            className="flex items-center gap-1 text-muted"
            onClick={(e) => {
              e.stopPropagation();
              onToggle();
            }}
          >
            <span aria-hidden="true">{expanded ? "▾" : "▸"}</span>
            {row.index + 1}
          </button>
        </td>
        {hasKeys && (
          <td
            className={cx(
              cell,
              "max-w-[16rem] truncate whitespace-nowrap font-mono",
              row.keyError && "bg-bad/10",
            )}
          >
            <KeyCell row={row} />
          </td>
        )}
        {columns.map((path) => {
          if (!row.flat.has(path)) return <td key={path} className={cell} />;
          const c = describeCell(row.flat.get(path));
          return (
            <td
              key={path}
              title={cut(c.full, HOVER_CHARS)}
              className={cx(
                cell,
                "max-w-[18rem] truncate whitespace-nowrap font-mono",
                (c.kind === "null" ||
                  c.kind === "array" ||
                  c.kind === "object") &&
                  "text-muted",
              )}
            >
              {cut(c.text, CELL_CHARS)}
            </td>
          );
        })}
      </tr>
      {expanded && (
        <tr>
          <td colSpan={span} className="border-b border-line bg-panel p-2">
            {row.keyError && (
              <div role="alert" className="mb-2 text-xs text-bad">
                Dedup key error: {row.keyError}
              </div>
            )}
            <JsonView value={row.item} maxHeight="20rem" />
          </td>
        </tr>
      )}
    </>
  );
}

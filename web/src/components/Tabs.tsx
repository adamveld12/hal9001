import { cx } from "../lib/cx";

export interface TabItem<K extends string> {
  key: K;
  label: string;
  count?: number;
}

export function Tabs<K extends string>({
  tabs,
  active,
  onChange,
}: {
  tabs: readonly TabItem<K>[];
  active: K;
  onChange: (key: K) => void;
}) {
  return (
    <div role="tablist" className="flex gap-1 border-b border-line px-3">
      {tabs.map((t) => (
        <button
          key={t.key}
          type="button"
          role="tab"
          aria-selected={t.key === active}
          onClick={() => onChange(t.key)}
          className={cx(
            "-mb-px border-b-2 px-2.5 py-2 text-xs font-medium",
            t.key === active
              ? "border-accent text-fg"
              : "border-transparent text-muted hover:text-fg",
          )}
        >
          {t.label}
          {t.count !== undefined && (
            <span className="ml-1.5 tabular-nums text-muted">{t.count}</span>
          )}
        </button>
      ))}
    </div>
  );
}

import { cx } from "../lib/cx";

/** Pretty-printed JSON in a scrollable block. */
export function JsonView({
  value,
  maxHeight = "24rem",
  className,
}: {
  value: unknown;
  maxHeight?: string;
  className?: string;
}) {
  return (
    <pre
      className={cx(
        "overflow-auto rounded-md border border-line bg-raised p-2 font-mono text-[11px]",
        className,
      )}
      style={{ maxHeight }}
    >
      {JSON.stringify(value, null, 2)}
    </pre>
  );
}

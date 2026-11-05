import type { AssistTestResult } from "../../api/assist";
import { BlobView } from "../../components/BlobView";
import { SensorItemsTable } from "../../components/SensorItemsTable";
import { Badge } from "../../components/ui";
import { formatDuration } from "../../lib/format";
import type { Tone } from "../../lib/status";
import { summarizeTest, type TestSummary } from "./testSummary";

const OUTCOME: Record<TestSummary["outcome"], { label: string; tone: Tone }> = {
  ok: { label: "ok", tone: "ok" },
  failed: { label: "failed", tone: "bad" },
  timed_out: { label: "timed out", tone: "warn" },
  parse_error: { label: "bad output", tone: "bad" },
};

/** Outcome of a draft test: exit code, duration, the parsed items with their dedup keys, and captured output. */
export function TestResultView({ result }: { result: AssistTestResult }) {
  const s = summarizeTest(result);
  const outcome = OUTCOME[s.outcome];
  return (
    <div className="space-y-3 text-xs">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Badge tone={outcome.tone}>{outcome.label}</Badge>
        <span>
          exit code{" "}
          <span className="font-mono font-medium">{result.exit_code}</span>
        </span>
        <span>{formatDuration(result.duration_ms)}</span>
        <span>
          {result.item_count} item{result.item_count === 1 ? "" : "s"}
        </span>
        {s.total > s.listed && (
          <span className="text-muted">first {s.listed} listed</span>
        )}
      </div>
      {result.parse_error && (
        <div role="alert" className="text-bad">
          {result.parse_error}
        </div>
      )}
      {s.keyErrors > 0 && (
        <div role="alert" className="text-bad">
          The key template failed on {s.keyErrors} item
          {s.keyErrors === 1 ? "" : "s"}.
        </div>
      )}
      {s.duplicateKeys > 0 && (
        <div className="text-warn">
          {s.duplicateKeys} item{s.duplicateKeys === 1 ? "" : "s"} repeat
          another item&rsquo;s dedup key and would not become separate events.
        </div>
      )}
      {!result.parse_error && (
        <SensorItemsTable
          items={result.items.map((i) => ({
            item: i.item,
            dedupKey: i.dedup_key,
            keyError: i.key_error,
          }))}
          maxHeight="20rem"
        />
      )}
      <details className="rounded-md border border-line">
        <summary className="cursor-pointer select-none px-2 py-1.5">
          stdout
        </summary>
        <div className="border-t border-line p-2">
          <BlobView blob={result.stdout} maxHeight="14rem" />
        </div>
      </details>
      <details
        className="rounded-md border border-line"
        open={result.exit_code !== 0 || undefined}
      >
        <summary className="cursor-pointer select-none px-2 py-1.5">
          stderr
        </summary>
        <div className="border-t border-line p-2">
          <BlobView blob={result.stderr} maxHeight="14rem" />
        </div>
      </details>
    </div>
  );
}

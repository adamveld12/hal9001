import { JsonView } from "../../components/JsonView";
import { SensorItemsTable } from "../../components/SensorItemsTable";
import { Badge } from "../../components/ui";
import { formatDuration } from "../../lib/format";

export interface ToolRunResult {
  ok: boolean;
  args: unknown;
  stdout: string;
  structured: unknown;
  stderr: string;
  exit_code: number;
  duration_ms: number;
  timed_out: boolean;
  error: string | null;
}

/** Outcome of one tool call: status, structured output as a table or JSON, else text, and stderr. */
export function ToolResultView({ result }: { result: ToolRunResult }) {
  const s = result.structured;
  return (
    <div className="space-y-2 text-xs" data-testid="tool-result">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Badge tone={result.ok ? "ok" : result.timed_out ? "warn" : "bad"}>
          {result.ok ? "ok" : result.timed_out ? "timed out" : "failed"}
        </Badge>
        <span>
          exit code <span className="font-mono">{result.exit_code}</span>
        </span>
        <span>{formatDuration(result.duration_ms)}</span>
      </div>
      {result.error && (
        <div role="alert" className="whitespace-pre-wrap text-bad">
          {result.error}
        </div>
      )}
      {Array.isArray(s) &&
      s.length > 0 &&
      s.every((i) => typeof i === "object" && i !== null) ? (
        <SensorItemsTable
          items={s.map((item) => ({ item, dedupKey: null, keyError: null }))}
          maxHeight="20rem"
        />
      ) : s !== null && s !== undefined ? (
        <JsonView value={s} />
      ) : result.stdout !== "" ? (
        <pre className="max-h-80 overflow-auto whitespace-pre-wrap rounded-md border border-line bg-raised p-2 font-mono text-[11px]">
          {result.stdout}
        </pre>
      ) : (
        !result.error && <div className="text-muted">No output.</div>
      )}
      {result.stderr !== "" && (
        <details>
          <summary className="cursor-pointer text-muted">stderr</summary>
          <pre className="max-h-60 overflow-auto whitespace-pre-wrap rounded-md border border-line bg-raised p-2 font-mono text-[11px]">
            {result.stderr}
          </pre>
        </details>
      )}
    </div>
  );
}

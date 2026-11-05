import { useMemo, useState } from "react";
import { useBlobText } from "../api/blobs";
import { formatBytes } from "../lib/format";
import { parseSensorStdout } from "../lib/sensorItems";
import { BlobView, type TextBlobLike } from "./BlobView";
import { SensorItemsTable } from "./SensorItemsTable";
import { Button } from "./ui";

/**
 * A sensor's stdout shown as an items table, parsed the way the daemon parses
 * it (a JSON array, or JSONL). Output that does not parse is shown as raw
 * text with the line of the first error. Cut-off output gives a table of the
 * items that were kept, flagged as possibly partial, with the stored full
 * output on request.
 */
export function SensorStdout({
  blob,
}: {
  blob: TextBlobLike | null | undefined;
}) {
  const [loadFull, setLoadFull] = useState(false);
  const hash = blob?.blob ?? null;
  const full = useBlobText(hash, loadFull);
  const text = full.data ?? blob?.text ?? "";
  const parsed = useMemo(() => parseSensorStdout(text), [text]);

  if (!blob || blob.size === 0) {
    return <div className="text-xs text-muted">(empty)</div>;
  }
  const partial =
    full.data === undefined &&
    (blob.truncated || hash !== null) &&
    text.length < blob.size;

  if (parsed.error && !partial) {
    return (
      <div className="space-y-2">
        <div role="alert" className="text-xs text-bad">
          stdout is not a JSON array or JSONL: line {parsed.error.line}:{" "}
          {parsed.error.message}. Showing the raw text.
        </div>
        <BlobView blob={blob} />
      </div>
    );
  }

  const notice = partial ? (
    <div
      role="status"
      className="flex flex-wrap items-center gap-2 rounded-md border border-warn/30 bg-warn/10 px-2 py-1.5 text-xs text-warn"
    >
      <span>
        Only {formatBytes(text.length)} of {formatBytes(blob.size)} was kept
        here; the table may be partial
        {parsed.error
          ? ` (stopped at line ${parsed.error.line}: ${parsed.error.message})`
          : ""}
        .
      </span>
      {hash !== null && (
        <Button disabled={full.isFetching} onClick={() => setLoadFull(true)}>
          {full.isFetching ? "Loading…" : "Load full output"}
        </Button>
      )}
      {full.isError && <span className="text-bad">{full.error.message}</span>}
    </div>
  ) : null;

  return (
    <SensorItemsTable
      items={parsed.items.map((item) => ({ item }))}
      notice={notice}
      rawView={<BlobView blob={blob} />}
    />
  );
}

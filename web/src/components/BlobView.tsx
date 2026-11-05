import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { qk } from "../api/keys";
import { formatBytes } from "../lib/format";
import { Button } from "./ui";

/** Shape of the API's TextBlob (stdin/stdout/stderr captures). */
export interface TextBlobLike {
  text: string;
  size: number;
  truncated: boolean;
  blob: string | null;
}

async function fetchBlobText(hash: string): Promise<string> {
  const res = await fetch(`/api/blobs/${hash}`);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.text();
}

/**
 * Captured text with its size. When the capture was stored as a blob, a
 * button loads the full content from /api/blobs/{hash}.
 */
export function BlobView({
  blob,
  empty = "(empty)",
  maxHeight = "20rem",
}: {
  blob: TextBlobLike | null | undefined;
  empty?: string;
  maxHeight?: string;
}) {
  const [loadFull, setLoadFull] = useState(false);
  const hash = blob?.blob ?? null;
  const full = useQuery({
    queryKey: qk("blob", hash),
    queryFn: () => fetchBlobText(hash as string),
    enabled: loadFull && hash !== null,
    staleTime: Infinity,
  });
  if (!blob || blob.size === 0)
    return <div className="text-xs text-muted">{empty}</div>;
  const text = full.data ?? blob.text;
  const partial =
    full.data === undefined &&
    (blob.truncated || hash !== null) &&
    text.length < blob.size;
  return (
    <div className="space-y-1">
      <pre
        className="overflow-auto whitespace-pre-wrap break-words rounded-md border border-line bg-raised p-2 font-mono text-[11px]"
        style={{ maxHeight }}
      >
        {text}
      </pre>
      <div className="flex items-center gap-2 text-[11px] text-muted">
        {formatBytes(blob.size)}
        {partial && hash !== null && (
          <Button disabled={full.isFetching} onClick={() => setLoadFull(true)}>
            {full.isFetching ? "Loading…" : "Load full"}
          </Button>
        )}
        {partial && hash === null && (
          <span>shown truncated; open the detail view for more</span>
        )}
        {full.isError && <span className="text-bad">{full.error.message}</span>}
      </div>
    </div>
  );
}

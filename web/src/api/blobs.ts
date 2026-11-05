import { useQuery } from "@tanstack/react-query";
import { qk } from "./keys";

async function fetchBlobText(hash: string): Promise<string> {
  const res = await fetch(`/api/blobs/${hash}`);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.text();
}

/** Full content of a stored blob (GET /api/blobs/{hash}), loaded once `enabled` and `hash` is set. */
export function useBlobText(hash: string | null, enabled: boolean) {
  return useQuery({
    queryKey: qk("blob", hash),
    queryFn: () => fetchBlobText(hash as string),
    enabled: enabled && hash !== null,
    staleTime: Infinity,
  });
}

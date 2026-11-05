import { useContext, useEffect, useRef } from "react";
import type { StreamType } from "./invalidate";
import {
  StreamContext,
  type Listener,
  type StreamState,
} from "./streamContext";

export function useStreamState(): StreamState {
  return useContext(StreamContext)?.state ?? "connecting";
}

/**
 * Run `fn` for each stream event of the given types while mounted. `fn` may
 * change between renders without resubscribing.
 */
export function useStreamEvents(
  types: readonly StreamType[],
  fn: Listener,
): void {
  const ctx = useContext(StreamContext);
  const latest = useRef(fn);
  useEffect(() => {
    latest.current = fn;
  });
  const key = types.join(",");
  useEffect(() => {
    if (!ctx) return;
    return ctx.subscribe(
      key === "" ? [] : (key.split(",") as StreamType[]),
      (e) => latest.current(e),
    );
  }, [ctx, key]);
}

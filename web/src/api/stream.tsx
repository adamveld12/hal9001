import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  rootsForBatch,
  STREAM_TYPES,
  type DataStreamType,
  type StreamType,
} from "./invalidate";
import { qk } from "./keys";

import {
  StreamContext,
  type Listener,
  type StreamContextValue,
  type StreamEvent,
  type StreamState,
} from "./streamContext";

/** Refetches coalesce for this long so event bursts cause one request per query family. */
const INVALIDATE_DELAY_MS = 200;

/**
 * Owns the single EventSource on /api/stream. Events refresh the react-query
 * families they affect (invalidate.ts); `status.changed` writes the header
 * counters directly; `stream.resync` refetches everything on screen.
 */
export function StreamProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const [state, setState] = useState<StreamState>("connecting");
  const listeners = useRef(new Map<Listener, ReadonlySet<StreamType>>());

  useEffect(() => {
    const es = new EventSource("/api/stream");
    const pending = new Set<DataStreamType>();
    let timer: ReturnType<typeof setTimeout> | undefined;

    const flush = () => {
      timer = undefined;
      const roots = rootsForBatch(pending);
      pending.clear();
      for (const root of roots) void qc.invalidateQueries({ queryKey: [root] });
    };

    const handle = (e: MessageEvent<string>) => {
      let event: StreamEvent;
      try {
        event = JSON.parse(e.data) as StreamEvent;
      } catch {
        return; // malformed frame; the next event or a resync repairs state
      }
      for (const [fn, types] of listeners.current) {
        if (types.size === 0 || types.has(event.type)) fn(event);
      }
      switch (event.type) {
        case "stream.ready":
          setState("open");
          break;
        case "stream.resync":
          void qc.invalidateQueries();
          break;
        case "status.changed":
          qc.setQueryData(qk("status"), event.data);
          break;
        default:
          pending.add(event.type);
          timer ??= setTimeout(flush, INVALIDATE_DELAY_MS);
      }
    };

    for (const t of STREAM_TYPES)
      es.addEventListener(t, handle as EventListener);
    es.onopen = () => setState("open");
    es.onerror = () => setState("reconnecting");

    return () => {
      if (timer) clearTimeout(timer);
      es.close();
    };
  }, [qc]);

  const value = useMemo<StreamContextValue>(
    () => ({
      state,
      subscribe: (types, fn) => {
        listeners.current.set(fn, new Set(types));
        return () => listeners.current.delete(fn);
      },
    }),
    [state],
  );

  return (
    <StreamContext.Provider value={value}>{children}</StreamContext.Provider>
  );
}

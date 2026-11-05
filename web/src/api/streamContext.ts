import { createContext } from "react";
import type { Schemas } from "./client";
import type { StreamType } from "./invalidate";

export type StreamEvent = Schemas["StreamEvent"];
export type StreamState = "connecting" | "open" | "reconnecting";
export type Listener = (event: StreamEvent) => void;

export interface StreamContextValue {
  state: StreamState;
  /** Listen to events of the given types (all types when empty). Returns an unsubscribe function. */
  subscribe: (types: readonly StreamType[], fn: Listener) => () => void;
}

export const StreamContext = createContext<StreamContextValue | null>(null);

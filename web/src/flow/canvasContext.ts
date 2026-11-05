import { createContext, useContext } from "react";
import type { RunOverlay } from "./overlay";
import type { Diagnostic } from "./model";

/** Render-time data shared with node components (kept out of node `data`). */
export interface CanvasContextValue {
  readOnly: boolean;
  diagnostics: ReadonlyMap<string, Diagnostic[]>;
  overlay?: RunOverlay;
  /** Output ports that currently have an edge, per node id (incl. invalid ones). */
  usedPorts: ReadonlyMap<string, ReadonlySet<string>>;
  /** Sensors known to the daemon; used to flag subscriptions to missing ones. */
  knownSensors?: ReadonlySet<string>;
}

export const CanvasContext = createContext<CanvasContextValue>({
  readOnly: false,
  diagnostics: new Map(),
  usedPorts: new Map(),
});

export function useCanvasContext(): CanvasContextValue {
  return useContext(CanvasContext);
}

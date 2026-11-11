import type { components } from "../api/schema";
import type { Flow, Priority } from "./model";

type ApiFlow = components["schemas"]["Flow"];

/** Fill the defaults the API omits so the editor can rely on them. */
export function flowFromApi(f: ApiFlow): Flow {
  return {
    ...f,
    enabled: f.enabled ?? true,
    subscribe: f.subscribe ?? [],
    edges: f.edges ?? [],
  } as Flow;
}

/**
 * The editor model must stay assignable to the contract's Flow; if the
 * OpenAPI schema and model.ts drift apart this stops compiling.
 */
export function flowToApi(f: Flow): ApiFlow {
  // An inbox priority may be a template; the contract still lists the names only.
  return {
    ...f,
    nodes: f.nodes.map((n) =>
      n.type === "inbox" ? { ...n, priority: n.priority as Priority } : n,
    ),
  };
}

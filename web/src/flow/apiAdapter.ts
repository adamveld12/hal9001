import type { components } from "../api/schema";
import type { Flow } from "./model";

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
  return f;
}

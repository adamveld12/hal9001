import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema";

export type Schemas = components["schemas"];

/** RFC 9457 problem body (`application/problem+json`). */
export type Problem = Schemas["Problem"] & Record<string, unknown>;

export class ApiError extends Error {
  readonly status: number;
  readonly problem: Problem | undefined;

  constructor(status: number, problem: Problem | undefined, fallback: string) {
    super(problem?.detail ?? problem?.title ?? fallback);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
  }

  /** Stable problem type without the `urn:hal9001:problem:` prefix, e.g. `conflict`. */
  get kind(): string | undefined {
    return this.problem?.type?.replace("urn:hal9001:problem:", "");
  }
}

// Every non-GET/HEAD request must declare JSON, even without a body (§10.3).
const jsonContentType: Middleware = {
  onRequest({ request }) {
    if (request.method !== "GET" && request.method !== "HEAD") {
      request.headers.set("Content-Type", "application/json");
    }
    return request;
  },
};

// Array query parameters are comma separated (`style: form, explode: false` in the contract).
export const api = createClient<paths>({
  baseUrl: "",
  querySerializer: { array: { style: "form", explode: false } },
});
api.use(jsonContentType);

interface Result<D> {
  data?: D;
  error?: unknown;
  response: Response;
}

function toApiError(error: unknown, response: Response): ApiError {
  return new ApiError(
    response.status,
    error && typeof error === "object" ? (error as Problem) : undefined,
    `${response.status} ${response.statusText}`,
  );
}

/** Resolve with the response body, or throw an ApiError carrying the problem. */
export async function unwrap<D>(
  call: Promise<Result<D>>,
): Promise<NonNullable<D>> {
  const { data, error, response } = await call;
  if (!response.ok || error !== undefined) throw toApiError(error, response);
  return data as NonNullable<D>;
}

/** Like unwrap for operations without a response body (204). */
export async function unwrapVoid(
  call: Promise<Result<unknown>>,
): Promise<void> {
  const { error, response } = await call;
  if (!response.ok || error !== undefined) throw toApiError(error, response);
}

/** Human-readable message for any thrown value. */
export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

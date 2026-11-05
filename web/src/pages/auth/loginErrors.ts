import { ApiError } from "../../api/client";
import { describeWebAuthnError } from "./webauthn";

export type LoginMethod = "password" | "webauthn";

/** Interactive modules a failed or challenged login reported, if any. */
export function methodsFrom(problem: unknown): string[] | undefined {
  if (typeof problem !== "object" || problem === null) return undefined;
  if (!("methods" in problem)) return undefined;
  const m: unknown = problem.methods;
  if (!Array.isArray(m)) return undefined;
  return m.every((x) => typeof x === "string") ? m : undefined;
}

function traceResult(problem: unknown, module: string): string | undefined {
  if (typeof problem !== "object" || problem === null) return undefined;
  if (!("trace" in problem) || !Array.isArray(problem.trace)) return undefined;
  for (const e of problem.trace as unknown[]) {
    if (
      typeof e === "object" &&
      e !== null &&
      "module" in e &&
      e.module === module &&
      "result" in e &&
      typeof e.result === "string"
    ) {
      return e.result;
    }
  }
  return undefined;
}

/** Text for any error thrown while signing in with `method`. */
export function loginErrorMessage(e: unknown, method: LoginMethod): string {
  if (e instanceof ApiError) {
    switch (e.kind) {
      case "authentication-failed":
        if (method === "password") return "Wrong password.";
        return traceResult(e.problem, "webauthn") === "ignore"
          ? "No passkey is registered for this host name."
          : "The passkey was not accepted.";
      case "login-expired":
        return "The sign-in took too long. Try again.";
      case "host-not-allowed":
        return "This host name is not in allowed_hosts.";
      default:
        return e.message;
    }
  }
  if (method === "webauthn") return describeWebAuthnError(e, "sign in");
  return e instanceof Error ? e.message : String(e);
}

import type {
  AuthMethod,
  AuthMethodId,
  AuthOverview,
  AuthStacks,
  Passkey,
} from "../../api/auth";
import type { Schemas } from "../../api/client";
import type { TailscaleStatus } from "../../api/tailscale";
import { factList } from "../../lib/contextSummary";
import type { PageContextInput } from "../../lib/pageContext";
import type { Tone } from "../../lib/status";

type AuthMethodState = Schemas["AuthMethodState"];

const SERVICES = ["ui", "api", "approve", "admin"] as const;

export interface PasskeyGroup {
  rpId: string;
  passkeys: Passkey[];
}

/** Passkeys grouped by relying-party ID: `currentHost` first, then alphabetical; newest first within a group. */
export function groupPasskeys(
  creds: readonly Passkey[],
  currentHost: string,
): PasskeyGroup[] {
  const byHost = new Map<string, Passkey[]>();
  for (const c of creds) {
    const list = byHost.get(c.rp_id) ?? [];
    list.push(c);
    byHost.set(c.rp_id, list);
  }
  return [...byHost.entries()]
    .sort(([a], [b]) => {
      if (a === currentHost) return -1;
      if (b === currentHost) return 1;
      return a.localeCompare(b);
    })
    .map(([rpId, passkeys]) => ({
      rpId,
      passkeys: [...passkeys].sort((x, y) =>
        y.created_at.localeCompare(x.created_at),
      ),
    }));
}

export interface StackSummary {
  name: string;
  /** Services this stack guards, in canonical order. */
  services: string[];
  /** `module (control)` per entry, in order. No options: they may hold secrets. */
  modules: string[];
}

/** One summary per stack; a stack that a service names but the config lacks is listed with no modules. */
export function stackSummaries(
  source: Pick<AuthStacks, "stacks" | "services">,
): StackSummary[] {
  const { stacks, services } = source;
  const names = new Set([
    ...Object.keys(stacks),
    ...SERVICES.map((s) => services[s]),
  ]);
  return [...names].sort().map((name) => ({
    name,
    services: SERVICES.filter((s) => services[s] === name),
    modules: (stacks[name] ?? []).map((e) => `${e.module} (${e.control})`),
  }));
}

export const STATE_LABEL: Record<AuthMethodState, string> = {
  on: "On",
  off: "Off",
  attention: "Needs attention",
};

export const STATE_TONE: Record<AuthMethodState, Tone> = {
  on: "ok",
  off: "muted",
  attention: "warn",
};

/** The method with this id; the overview always lists all four. */
export function methodById(
  overview: AuthOverview,
  id: AuthMethodId,
): AuthMethod | undefined {
  return overview.methods.find((m) => m.id === id);
}

/** What the assistant gets about this page: which ways in are on, whether a password is set. No logins, hashes or options. */
export function authPageContext(input: {
  overview: AuthOverview | undefined;
  stacks: Pick<AuthStacks, "stacks" | "services"> | undefined;
  tailscale: TailscaleStatus | undefined;
}): PageContextInput | null {
  const { overview, stacks, tailscale } = input;
  if (!overview) return null;
  return {
    title: "Settings: Auth",
    summary:
      "How you sign in to hal: the ways in with their state, Tailscale sharing and who can sign in, the password, passkeys, sessions and sign-in events.",
    facts: factList([
      ...overview.methods.map(
        (m) =>
          [
            `${m.name}`,
            `${m.state}${m.custom ? " (custom stacks)" : ""}`,
          ] as const,
      ),
      ["password set", overview.password.set],
      ["passkeys", overview.passkey_count],
      ["tailnet logins listed", overview.allowed_logins.length],
      ["this request admitted by", overview.current.method],
      ["this request is remote", overview.current.remote],
      ["tailscale cli found", tailscale?.cli.found],
      ["shared on tailnet", tailscale?.serve.points_at_hal],
    ]),
    entities: stacks
      ? stackSummaries(stacks).map((s) => ({
          kind: "auth_stack",
          id: s.name,
          detail: `guards ${s.services.join(", ") || "nothing"}; modules ${s.modules.join(", ") || "none"}`,
        }))
      : [],
  };
}

const ACTION_LABELS: Record<string, string> = {
  auth_methods_change: "sign-in methods changed",
  auth_password_set: "password set",
  auth_password_change: "password changed",
  auth_password_remove: "password removed",
  auth_logins_change: "tailnet logins changed",
  auth_stacks_change: "auth stacks changed",
};

/** Text for an auth event's action: the known sign-in changes in plain words, any other action with its underscores as spaces. */
export function actionLabel(action: string): string {
  return ACTION_LABELS[action] ?? action.replaceAll("_", " ");
}

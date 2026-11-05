import type { Config } from "../../api/settings";
import type { Passkey } from "../../api/auth";

type AuthConfig = NonNullable<Config["auth"]>;

const SERVICES = ["ui", "api", "approve", "admin"] as const;
const DEFAULT_STACK = "default";

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

export interface StackView {
  name: string;
  /** Services this stack guards, in canonical order. */
  services: string[];
  entries: {
    module: string;
    control: string;
    options: [key: string, value: string][];
  }[];
}

/** Value text for a module option. Secrets arrive already redacted by the daemon. */
function optionText(v: unknown): string {
  return typeof v === "string" ? v : JSON.stringify(v);
}

/** Read-only view model of `auth.stacks` and `auth.services`. Unmapped services use `default`. */
export function stackViews(auth: AuthConfig | undefined): StackView[] {
  const stacks = auth?.stacks ?? {};
  const services = auth?.services ?? {};
  const guardedBy = (name: string) =>
    SERVICES.filter((s) => (services[s] ?? DEFAULT_STACK) === name);
  const names = new Set(Object.keys(stacks));
  for (const s of SERVICES) names.add(services[s] ?? DEFAULT_STACK);
  return [...names].sort().map((name) => ({
    name,
    services: guardedBy(name),
    entries: (stacks[name] ?? []).map((e) => ({
      module: e.module,
      control: e.control,
      options: Object.entries(e.options ?? {}).map(([k, v]) => [
        k,
        optionText(v),
      ]),
    })),
  }));
}

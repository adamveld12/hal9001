// State logic of the Advanced stacks editor: the server's `auth.stacks` and
// `auth.services` as an editable draft, and back into a request body. The
// server validates the result (POST /api/auth/stacks/preview); this module only
// keeps the draft well formed.

import type {
  AuthStacks,
  AuthStacksRequest,
  AuthModuleEntry,
} from "../../api/auth";

/** What the daemon sends in place of a secret option value; sent back unchanged it keeps the stored value. */
export const REDACTED = "«redacted»";

export const MODULES = [
  "localhost-trust",
  "tailscale-identity",
  "webauthn",
  "password",
] as const;
export const CONTROLS = [
  "required",
  "requisite",
  "sufficient",
  "optional",
] as const;
export const SERVICE_NAMES = ["ui", "api", "approve", "admin"] as const;

export type Control = (typeof CONTROLS)[number];
export type ServiceName = (typeof SERVICE_NAMES)[number];

export interface DraftEntry {
  /** Stable React key; not sent. */
  key: string;
  module: string;
  control: string;
  /** Module options as received (secrets redacted). Kept as they are; the editor does not edit them. */
  options: Record<string, unknown>;
}

export interface DraftStack {
  /** Stable React key; not sent. */
  key: string;
  name: string;
  entries: DraftEntry[];
}

export interface Draft {
  stacks: DraftStack[];
  services: Record<ServiceName, string>;
  nextKey: number;
}

/** The draft for what `GET /api/auth/stacks` returned. */
export function toDraft(
  source: Pick<AuthStacks, "stacks" | "services">,
): Draft {
  let n = 0;
  const key = () => `k${n++}`;
  const stacks = Object.entries(source.stacks).map(
    ([name, entries]): DraftStack => ({
      key: key(),
      name,
      entries: entries.map((e): DraftEntry => ({
        key: key(),
        module: e.module,
        control: e.control,
        options: { ...e.options },
      })),
    }),
  );
  return {
    stacks,
    services: {
      ui: source.services.ui,
      api: source.services.api,
      approve: source.services.approve,
      admin: source.services.admin,
    },
    nextKey: n,
  };
}

/** The request body for the draft (`expected_hash` is the file hash the draft started from). */
export function toRequest(
  draft: Draft,
  expectedHash: string,
  confirm = false,
): AuthStacksRequest {
  const stacks: Record<string, AuthModuleEntry[]> = {};
  for (const s of draft.stacks) {
    stacks[s.name] = s.entries.map((e) => {
      const entry: AuthModuleEntry = {
        module: e.module,
        control: e.control as Control,
      };
      if (Object.keys(e.options).length > 0) entry.options = { ...e.options };
      return entry;
    });
  }
  const body: AuthStacksRequest = {
    stacks,
    services: { ...draft.services },
    expected_hash: expectedHash,
  };
  if (confirm) body.confirm = true;
  return body;
}

/** The part of the request that says what the config would be: no hash, no confirmation. */
function content(draft: Draft): string {
  const { stacks, services } = toRequest(draft, "");
  return JSON.stringify({ stacks, services });
}

/** Whether `draft` differs from `base` (the draft the server's data gave). */
export function isDirty(draft: Draft, base: Draft): boolean {
  return content(draft) !== content(base);
}

function mapStack(
  draft: Draft,
  stackKey: string,
  edit: (s: DraftStack) => DraftStack,
): Draft {
  return {
    ...draft,
    stacks: draft.stacks.map((s) => (s.key === stackKey ? edit(s) : s)),
  };
}

function mapEntry(
  draft: Draft,
  stackKey: string,
  entryKey: string,
  edit: (e: DraftEntry) => DraftEntry,
): Draft {
  return mapStack(draft, stackKey, (s) => ({
    ...s,
    entries: s.entries.map((e) => (e.key === entryKey ? edit(e) : e)),
  }));
}

/** Adds an entry at the end of a stack: the first module the stack lacks, `sufficient`. */
export function addEntry(draft: Draft, stackKey: string): Draft {
  const next = mapStack(draft, stackKey, (s) => {
    const have = new Set(s.entries.map((e) => e.module));
    const module = MODULES.find((m) => !have.has(m)) ?? MODULES[0];
    const entry: DraftEntry = {
      key: `k${draft.nextKey}`,
      module,
      control: "sufficient",
      options: {},
    };
    return { ...s, entries: [...s.entries, entry] };
  });
  return { ...next, nextKey: draft.nextKey + 1 };
}

/** Removes an entry. A removed `password` entry takes its stored hash with it. */
export function removeEntry(
  draft: Draft,
  stackKey: string,
  entryKey: string,
): Draft {
  return mapStack(draft, stackKey, (s) => ({
    ...s,
    entries: s.entries.filter((e) => e.key !== entryKey),
  }));
}

/** Moves an entry one place up (`-1`) or down (`1`); at the end of the list nothing moves. */
export function moveEntry(
  draft: Draft,
  stackKey: string,
  entryKey: string,
  delta: -1 | 1,
): Draft {
  return mapStack(draft, stackKey, (s) => {
    const from = s.entries.findIndex((e) => e.key === entryKey);
    const to = from + delta;
    if (from < 0 || to < 0 || to >= s.entries.length) return s;
    const entries = [...s.entries];
    const [moved] = entries.splice(from, 1);
    if (moved) entries.splice(to, 0, moved);
    return { ...s, entries };
  });
}

/** Another module takes the entry's place; options belong to the module, so they are dropped. */
export function setEntryModule(
  draft: Draft,
  stackKey: string,
  entryKey: string,
  module: string,
): Draft {
  return mapEntry(draft, stackKey, entryKey, (e) =>
    e.module === module ? e : { ...e, module, options: {} },
  );
}

export function setEntryControl(
  draft: Draft,
  stackKey: string,
  entryKey: string,
  control: string,
): Draft {
  return mapEntry(draft, stackKey, entryKey, (e) => ({ ...e, control }));
}

/** A new, empty stack named `stack`, `stack-2`, ... whichever is free. */
export function addStack(draft: Draft): Draft {
  const names = new Set(draft.stacks.map((s) => s.name));
  let name = "stack";
  for (let i = 2; names.has(name); i++) name = `stack-${i}`;
  return {
    ...draft,
    stacks: [...draft.stacks, { key: `k${draft.nextKey}`, name, entries: [] }],
    nextKey: draft.nextKey + 1,
  };
}

/** The services that use the stack named `name`. */
export function stackUsedBy(draft: Draft, name: string): ServiceName[] {
  return SERVICE_NAMES.filter((s) => draft.services[s] === name);
}

/** Removes a stack no service uses; a used stack stays (point the services elsewhere first). */
export function removeStack(draft: Draft, stackKey: string): Draft {
  const stack = draft.stacks.find((s) => s.key === stackKey);
  if (!stack || stackUsedBy(draft, stack.name).length > 0) return draft;
  return { ...draft, stacks: draft.stacks.filter((s) => s.key !== stackKey) };
}

export interface RenameResult {
  draft: Draft;
  /** Why the name was not taken; null when it was. */
  error: string | null;
}

/** Renames a stack; the services that used the old name follow. A blank or taken name is refused. */
export function renameStack(
  draft: Draft,
  stackKey: string,
  name: string,
): RenameResult {
  const stack = draft.stacks.find((s) => s.key === stackKey);
  const next = name.trim();
  if (!stack) return { draft, error: null };
  if (next === stack.name) return { draft, error: null };
  if (next === "") return { draft, error: "A stack needs a name." };
  if (draft.stacks.some((s) => s.name === next)) {
    return { draft, error: `There is already a stack named ${next}.` };
  }
  const services = { ...draft.services };
  for (const s of SERVICE_NAMES) {
    if (services[s] === stack.name) services[s] = next;
  }
  return {
    draft: {
      ...mapStack(draft, stackKey, (s) => ({ ...s, name: next })),
      services,
    },
    error: null,
  };
}

export function setService(
  draft: Draft,
  service: ServiceName,
  stack: string,
): Draft {
  return { ...draft, services: { ...draft.services, [service]: stack } };
}

/** Problems that make the draft unrepresentable as a request: blank or repeated stack names. */
export function draftIssues(draft: Draft): string[] {
  const issues: string[] = [];
  const seen = new Set<string>();
  for (const s of draft.stacks) {
    if (s.name.trim() === "") issues.push("A stack needs a name.");
    else if (seen.has(s.name)) {
      issues.push(`The stack name ${s.name} is used twice.`);
    }
    seen.add(s.name);
  }
  return issues;
}

/** The options of an entry as rows of text. Secrets arrive redacted and are shown so. */
export function optionRows(entry: DraftEntry): [key: string, value: string][] {
  return Object.entries(entry.options).map(([k, v]) => [
    k,
    typeof v === "string" ? v : JSON.stringify(v),
  ]);
}

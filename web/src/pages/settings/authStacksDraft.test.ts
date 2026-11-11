import { describe, expect, it } from "vitest";
import type { AuthStacks } from "../../api/auth";
import {
  addEntry,
  addStack,
  draftIssues,
  isDirty,
  moveEntry,
  optionRows,
  REDACTED,
  removeEntry,
  removeStack,
  renameStack,
  setEntryControl,
  setEntryModule,
  setService,
  stackUsedBy,
  toDraft,
  toRequest,
} from "./authStacksDraft";

const source: AuthStacks = {
  hash: "h1",
  stacks: {
    default: [
      { module: "localhost-trust", control: "sufficient" },
      {
        module: "password",
        control: "sufficient",
        options: { hash: REDACTED },
      },
    ],
    strict: [{ module: "webauthn", control: "required" }],
  },
  services: {
    ui: "default",
    api: "default",
    approve: "default",
    admin: "strict",
  },
};

const base = toDraft(source);
const stackKey = (d: typeof base, name: string) =>
  d.stacks.find((s) => s.name === name)!.key;
const entryKey = (d: typeof base, stack: string, i: number) =>
  d.stacks.find((s) => s.name === stack)!.entries[i]!.key;
const modules = (d: typeof base, stack: string) =>
  d.stacks.find((s) => s.name === stack)!.entries.map((e) => e.module);

describe("draft and request", () => {
  it("round-trips what the server sent, redacted secrets included", () => {
    const req = toRequest(base, "h1");
    expect(req).toEqual({
      stacks: source.stacks,
      services: source.services,
      expected_hash: "h1",
    });
    expect(req.stacks.default?.[1]?.options).toEqual({ hash: REDACTED });
  });

  it("sends confirm only when asked to", () => {
    expect(toRequest(base, "h1")).not.toHaveProperty("confirm");
    expect(toRequest(base, "h1", true).confirm).toBe(true);
  });

  it("is not dirty until something changes, and clean again when changed back", () => {
    expect(isDirty(base, toDraft(source))).toBe(false);
    const changed = setEntryControl(
      base,
      stackKey(base, "default"),
      entryKey(base, "default", 0),
      "optional",
    );
    expect(isDirty(changed, base)).toBe(true);
    const back = setEntryControl(
      changed,
      stackKey(base, "default"),
      entryKey(base, "default", 0),
      "sufficient",
    );
    expect(isDirty(back, base)).toBe(false);
  });
});

describe("entries", () => {
  const sk = stackKey(base, "default");

  it("moves up and down and stops at the ends", () => {
    const down = moveEntry(base, sk, entryKey(base, "default", 0), 1);
    expect(modules(down, "default")).toEqual(["password", "localhost-trust"]);
    expect(isDirty(down, base)).toBe(true);
    expect(
      modules(moveEntry(base, sk, entryKey(base, "default", 0), -1), "default"),
    ).toEqual(["localhost-trust", "password"]);
    expect(
      modules(moveEntry(base, sk, entryKey(base, "default", 1), 1), "default"),
    ).toEqual(["localhost-trust", "password"]);
  });

  it("adds the first module the stack lacks, as sufficient, with a fresh key", () => {
    const added = addEntry(base, sk);
    expect(modules(added, "default")).toEqual([
      "localhost-trust",
      "password",
      "tailscale-identity",
    ]);
    const entry = added.stacks[0]!.entries[2]!;
    expect(entry.control).toBe("sufficient");
    const keys = added.stacks.flatMap((s) => s.entries.map((e) => e.key));
    expect(new Set(keys).size).toBe(keys.length);
    const twice = addEntry(added, sk);
    expect(modules(twice, "default")[3]).toBe("webauthn");
  });

  it("removes an entry, and its options go with it", () => {
    const removed = removeEntry(base, sk, entryKey(base, "default", 1));
    expect(modules(removed, "default")).toEqual(["localhost-trust"]);
    expect(JSON.stringify(toRequest(removed, "h1"))).not.toContain(REDACTED);
  });

  it("drops the options when the module changes, keeps them when it does not", () => {
    const pw = entryKey(base, "default", 1);
    expect(
      setEntryModule(base, sk, pw, "password").stacks[0]!.entries[1]!.options,
    ).toEqual({ hash: REDACTED });
    const other = setEntryModule(base, sk, pw, "webauthn");
    expect(other.stacks[0]!.entries[1]).toMatchObject({
      module: "webauthn",
      options: {},
    });
    expect(toRequest(other, "h1").stacks.default?.[1]).toEqual({
      module: "webauthn",
      control: "sufficient",
    });
  });

  it("lists options as text", () => {
    expect(optionRows(base.stacks[0]!.entries[1]!)).toEqual([
      ["hash", REDACTED],
    ]);
    expect(
      optionRows({
        key: "x",
        module: "m",
        control: "c",
        options: { n: 5, ok: true },
      }),
    ).toEqual([
      ["n", "5"],
      ["ok", "true"],
    ]);
  });
});

describe("stacks and services", () => {
  it("adds a stack with a free name", () => {
    const one = addStack(base);
    expect(one.stacks.map((s) => s.name)).toEqual([
      "default",
      "strict",
      "stack",
    ]);
    expect(addStack(one).stacks.at(-1)?.name).toBe("stack-2");
    expect(toRequest(one, "h1").stacks.stack).toEqual([]);
  });

  it("renames a stack and the services that used it", () => {
    const r = renameStack(base, stackKey(base, "strict"), " locked ");
    expect(r.error).toBeNull();
    expect(r.draft.services.admin).toBe("locked");
    expect(Object.keys(toRequest(r.draft, "h1").stacks)).toEqual([
      "default",
      "locked",
    ]);
  });

  it("refuses a blank or taken name and keeps the draft", () => {
    const sk = stackKey(base, "strict");
    expect(renameStack(base, sk, "  ")).toEqual({
      draft: base,
      error: "A stack needs a name.",
    });
    expect(renameStack(base, sk, "default").error).toMatch(
      /already a stack named default/,
    );
    expect(renameStack(base, sk, "strict")).toEqual({
      draft: base,
      error: null,
    });
  });

  it("removes only a stack no service uses", () => {
    expect(removeStack(base, stackKey(base, "strict"))).toBe(base);
    const moved = setService(base, "admin", "default");
    expect(
      removeStack(moved, stackKey(moved, "strict")).stacks.map((s) => s.name),
    ).toEqual(["default"]);
    expect(stackUsedBy(base, "default")).toEqual(["ui", "api", "approve"]);
  });

  it("reports blank and repeated stack names", () => {
    expect(draftIssues(base)).toEqual([]);
    const dup = {
      ...base,
      stacks: base.stacks.map((s) => ({ ...s, name: "same" })),
    };
    expect(draftIssues(dup)).toEqual(["The stack name same is used twice."]);
    const blank = {
      ...base,
      stacks: base.stacks.map((s, i) => (i === 0 ? { ...s, name: " " } : s)),
    };
    expect(draftIssues(blank)).toEqual(["A stack needs a name."]);
  });
});

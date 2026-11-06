import { describe, expect, it } from "vitest";
import { ApiError } from "../../api/client";
import {
  describeSaveFailure,
  saveFlowDraft,
  type FlowSaveApi,
  type SaveResult,
} from "./flowSave";

const prompt = (name: string, exists: boolean) => ({
  name,
  yaml: `---\nname: ${name}\n---\nbody`,
  exists,
});

function fakeApi(over: Partial<FlowSaveApi> = {}) {
  const calls: string[] = [];
  const api: FlowSaveApi = {
    createPrompt: (md) => {
      calls.push(`prompt:${md.split("\n")[1]}`);
      return Promise.resolve();
    },
    createFlow: (yaml) => {
      calls.push(`create:${yaml}`);
      return Promise.resolve({ id: "triage", hash: "h1" });
    },
    updateFlow: (id, hash, yaml) => {
      calls.push(`update:${id}:${hash}:${yaml}`);
      return Promise.resolve({ id, hash: "h2" });
    },
    ...over,
  };
  return { api, calls };
}

const problem = (status: number, type: string, extra = {}) =>
  new ApiError(
    status,
    { type: `urn:hal9001:problem:${type}`, ...extra } as never,
    "x",
  );

describe("saveFlowDraft", () => {
  it("creates the new prompts in order before the flow", async () => {
    const { api, calls } = fakeApi();
    const r = await saveFlowDraft(
      api,
      {
        yaml: "id: triage",
        prompts: [prompt("p1", false), prompt("p2", false)],
      },
      { mode: "create" },
    );
    expect(calls).toEqual([
      "prompt:name: p1",
      "prompt:name: p2",
      "create:id: triage",
    ]);
    expect(r).toMatchObject({ ok: true, createdPrompts: ["p1", "p2"] });
  });

  it("never touches a prompt that already exists", async () => {
    const { api, calls } = fakeApi();
    const r = await saveFlowDraft(
      api,
      {
        yaml: "id: triage",
        prompts: [prompt("old", true), prompt("new", false)],
      },
      { mode: "create" },
    );
    expect(calls).toEqual(["prompt:name: new", "create:id: triage"]);
    expect(r).toMatchObject({ ok: true, createdPrompts: ["new"] });
  });

  it("updates an existing flow against the hash the draft started from", async () => {
    const { api, calls } = fakeApi();
    const r = await saveFlowDraft(
      api,
      { yaml: "id: triage", prompts: [] },
      { mode: "update", id: "triage", expectedHash: "h0" },
    );
    expect(calls).toEqual(["update:triage:h0:id: triage"]);
    expect(r).toMatchObject({ ok: true, flow: { id: "triage", hash: "h2" } });
  });

  it("stops before the flow when a prompt cannot be created", async () => {
    const { api, calls } = fakeApi({
      createPrompt: (md) => {
        if (md.includes("p2")) return Promise.reject(problem(409, "conflict"));
        calls.push("prompt");
        return Promise.resolve();
      },
    });
    const r = await saveFlowDraft(
      api,
      {
        yaml: "id: triage",
        prompts: [
          prompt("p1", false),
          prompt("p2", false),
          prompt("p3", false),
        ],
      },
      { mode: "create" },
    );
    expect(calls).toEqual(["prompt"]);
    expect(r).toMatchObject({
      ok: false,
      stage: "prompt",
      prompt: "p2",
      createdPrompts: ["p1"],
    });
  });

  it("reports the prompts it created when the flow is then rejected", async () => {
    const { api } = fakeApi({
      updateFlow: () =>
        Promise.reject(problem(409, "conflict", { current_hash: "h9" })),
    });
    const r = await saveFlowDraft(
      api,
      { yaml: "id: triage", prompts: [prompt("p1", false)] },
      { mode: "update", id: "triage", expectedHash: "h0" },
    );
    expect(r).toMatchObject({
      ok: false,
      stage: "flow",
      createdPrompts: ["p1"],
    });
  });
});

describe("describeSaveFailure", () => {
  const failed = (r: SaveResult) => {
    if (r.ok) throw new Error("expected a failure");
    return describeSaveFailure(r);
  };

  it("says a taken prompt name stopped the save", () => {
    const f = failed({
      ok: false,
      stage: "prompt",
      prompt: "triage",
      error: problem(409, "conflict"),
      createdPrompts: [],
    });
    expect(f.message).toMatch(/Prompt "triage" already exists/);
    expect(f.message).toMatch(/flow was not saved/);
    expect(f.conflict).toBe(false);
  });

  it("offers to overwrite when the file changed on disk", () => {
    const f = failed({
      ok: false,
      stage: "flow",
      error: problem(409, "conflict"),
      createdPrompts: [],
    });
    expect(f.conflict).toBe(true);
    expect(f.message).toMatch(/changed on disk/);
  });

  it("carries the diagnostics of a rejected flow", () => {
    const f = failed({
      ok: false,
      stage: "flow",
      error: problem(422, "validation-failed", {
        diagnostics: [{ severity: "error", message: "unknown sensor" }],
      }),
      createdPrompts: [],
    });
    expect(f.diagnostics).toHaveLength(1);
    expect(f.conflict).toBe(false);
  });
});

import { describe, expect, it } from "vitest";
import type { AssistDraft } from "../../api/assist";
import { ApiError } from "../../api/client";
import {
  describeSaveFailure,
  describeSavePlan,
  saveFlowDraft,
  SensorExistsError,
  type FlowSaveApi,
  type SaveResult,
} from "./flowSave";

type Draft = Pick<AssistDraft, "yaml" | "prompts" | "sensors">;

const prompt = (name: string, exists: boolean) => ({
  name,
  yaml: `---\nname: ${name}\n---\nbody`,
  exists,
});

/** A sensor the agent drafted: new (`base` null), or an edit of the saved file that had hash `base`. */
const sensor = (
  id: string,
  base: string | null = null,
  exists = base !== null,
) => ({
  id,
  yaml: `interval: 5m\n# ${id}`,
  exists,
  base_hash: base,
  diagnostics: [],
});

const draftOf = (over: Partial<Draft> = {}): Draft => ({
  yaml: "id: triage",
  prompts: [],
  sensors: [],
  ...over,
});

function fakeApi(over: Partial<FlowSaveApi> = {}) {
  const calls: string[] = [];
  const api: FlowSaveApi = {
    createSensor: (id) => {
      calls.push(`sensor+:${id}`);
      return Promise.resolve();
    },
    updateSensor: (id, hash) => {
      calls.push(`sensor~:${id}:${hash}`);
      return Promise.resolve();
    },
    currentSensorHash: (id) => {
      calls.push(`hash:${id}`);
      return Promise.resolve(`now-${id}`);
    },
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

const update = { mode: "update", id: "triage", expectedHash: "h0" } as const;

describe("saveFlowDraft", () => {
  it("creates the new prompts in order before the flow", async () => {
    const { api, calls } = fakeApi();
    const r = await saveFlowDraft(
      api,
      draftOf({ prompts: [prompt("p1", false), prompt("p2", false)] }),
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
      draftOf({ prompts: [prompt("old", true), prompt("new", false)] }),
      { mode: "create" },
    );
    expect(calls).toEqual(["prompt:name: new", "create:id: triage"]);
    expect(r).toMatchObject({ ok: true, createdPrompts: ["new"] });
  });

  it("updates an existing flow against the hash the draft started from", async () => {
    const { api, calls } = fakeApi();
    const r = await saveFlowDraft(api, draftOf(), update);
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
      draftOf({
        prompts: [
          prompt("p1", false),
          prompt("p2", false),
          prompt("p3", false),
        ],
      }),
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
      draftOf({ prompts: [prompt("p1", false)] }),
      update,
    );
    expect(r).toMatchObject({
      ok: false,
      stage: "flow",
      createdPrompts: ["p1"],
    });
  });

  it("saves the sensors first, then the prompts, then the flow", async () => {
    const { api, calls } = fakeApi();
    const r = await saveFlowDraft(
      api,
      draftOf({
        sensors: [sensor("fresh"), sensor("edited", "h-old")],
        prompts: [prompt("old", true), prompt("p1", false)],
      }),
      { mode: "create" },
    );
    expect(calls).toEqual([
      "sensor+:fresh",
      "sensor~:edited:h-old",
      "prompt:name: p1",
      "create:id: triage",
    ]);
    expect(r).toMatchObject({
      ok: true,
      createdSensors: ["fresh"],
      updatedSensors: ["edited"],
      createdPrompts: ["p1"],
    });
  });

  it("replaces an edited sensor against the hash its draft copy was made at", async () => {
    const { api, calls } = fakeApi();
    await saveFlowDraft(
      api,
      draftOf({ sensors: [sensor("gh", "h-copy")] }),
      update,
    );
    expect(calls).toEqual(["sensor~:gh:h-copy", "update:triage:h0:id: triage"]);
  });

  it("stops before the prompts and the flow when a sensor changed on disk, and reports the sensors it saved", async () => {
    const { api, calls } = fakeApi({
      updateSensor: (id, hash) => {
        calls.push(`sensor~:${id}:${hash}`);
        return id === "b"
          ? Promise.reject(problem(409, "conflict", { current_hash: "h9" }))
          : Promise.resolve();
      },
    });
    const r = await saveFlowDraft(
      api,
      draftOf({
        sensors: [sensor("a"), sensor("b", "hb"), sensor("c", "hc")],
        prompts: [prompt("p1", false)],
      }),
      { mode: "create" },
    );
    expect(calls).toEqual(["sensor+:a", "sensor~:b:hb"]);
    expect(r).toMatchObject({
      ok: false,
      stage: "sensor",
      sensor: "b",
      action: "update",
      createdSensors: ["a"],
      updatedSensors: [],
      createdPrompts: [],
    });
  });

  it("does not call the API for a new sensor whose id a saved file already has", async () => {
    const { api, calls } = fakeApi();
    const r = await saveFlowDraft(
      api,
      draftOf({ sensors: [sensor("a"), sensor("taken", null, true)] }),
      { mode: "create" },
    );
    expect(calls).toEqual(["sensor+:a"]);
    expect(r).toMatchObject({
      ok: false,
      stage: "sensor",
      sensor: "taken",
      action: "create",
      createdSensors: ["a"],
    });
    if (r.ok) throw new Error("expected a failure");
    expect(r.error).toBeInstanceOf(SensorExistsError);
  });

  it("reports a create the daemon refuses as taken, with the sensors saved before it", async () => {
    const { api } = fakeApi({
      createSensor: (id) =>
        id === "b"
          ? Promise.reject(problem(409, "already-exists"))
          : Promise.resolve(),
    });
    const r = await saveFlowDraft(
      api,
      draftOf({ sensors: [sensor("a"), sensor("b")] }),
      { mode: "create" },
    );
    expect(r).toMatchObject({
      ok: false,
      stage: "sensor",
      sensor: "b",
      action: "create",
      createdSensors: ["a"],
    });
  });

  it("with overwrite replaces edited sensors against their current hash and still creates new ones", async () => {
    const { api, calls } = fakeApi();
    const r = await saveFlowDraft(
      api,
      draftOf({ sensors: [sensor("a"), sensor("b", "hb")] }),
      { mode: "create" },
      { overwrite: true },
    );
    expect(calls).toEqual([
      "sensor+:a",
      "hash:b",
      "sensor~:b:now-b",
      "create:id: triage",
    ]);
    expect(r).toMatchObject({
      ok: true,
      createdSensors: ["a"],
      updatedSensors: ["b"],
    });
  });

  it("reports the sensor when its current hash cannot be read for an overwrite", async () => {
    const { api, calls } = fakeApi({
      currentSensorHash: () => Promise.reject(problem(404, "not-found")),
    });
    const r = await saveFlowDraft(
      api,
      draftOf({ sensors: [sensor("b", "hb")] }),
      { mode: "create" },
      { overwrite: true },
    );
    expect(calls).toEqual([]);
    expect(r).toMatchObject({ ok: false, stage: "sensor", sensor: "b" });
  });
});

describe("describeSavePlan", () => {
  it("lists the flow, the sensors by id and the prompts that will be created", () => {
    expect(
      describeSavePlan(
        {
          sensors: [sensor("a"), sensor("b", "hb")],
          prompts: [prompt("p", false), prompt("old", true)],
        },
        { mode: "update" },
      ),
    ).toBe("Saves 1 flow, 2 sensors (new: a; updated: b), 1 prompt");
  });

  it("names a flow that does not exist yet as new", () => {
    expect(describeSavePlan(draftOf(), { mode: "create" })).toBe(
      "Saves 1 new flow",
    );
  });

  it("is only the flow when the draft has no other file to write", () => {
    expect(
      describeSavePlan(draftOf({ prompts: [prompt("old", true)] }), update),
    ).toBe("Saves 1 flow");
  });

  it("names only the kinds of sensor the draft has", () => {
    expect(
      describeSavePlan(draftOf({ sensors: [sensor("b", "hb")] }), update),
    ).toBe("Saves 1 flow, 1 sensor (updated: b)");
    expect(
      describeSavePlan(
        draftOf({
          sensors: [sensor("a"), sensor("c")],
          prompts: [prompt("p1", false), prompt("p2", false)],
        }),
        update,
      ),
    ).toBe("Saves 1 flow, 2 sensors (new: a, c), 2 prompts");
  });
});

describe("describeSaveFailure", () => {
  const none = { createdSensors: [], updatedSensors: [], createdPrompts: [] };
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
      ...none,
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
      ...none,
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
      ...none,
    });
    expect(f.diagnostics).toHaveLength(1);
    expect(f.conflict).toBe(false);
  });

  it("offers to overwrite a sensor that changed on disk and says what was already saved", () => {
    const f = failed({
      ok: false,
      stage: "sensor",
      sensor: "b",
      action: "update",
      error: problem(409, "conflict"),
      ...none,
      createdSensors: ["a"],
    });
    expect(f.conflict).toBe(true);
    expect(f.message).toMatch(
      /Sensor "b" changed on disk since this session copied it/,
    );
    expect(f.message).toMatch(/flow was not saved/);
    expect(f.message).toMatch(/Already saved: created sensor a\./);
    expect(f.message).toMatch(/Overwrite/);
  });

  it("says a taken sensor id stopped the save and offers no overwrite", () => {
    const f = failed({
      ok: false,
      stage: "sensor",
      sensor: "a",
      action: "create",
      error: problem(409, "already-exists"),
      ...none,
    });
    expect(f.message).toMatch(
      /Sensor "a" already exists \(created meanwhile\)/,
    );
    expect(f.message).toMatch(/not overwritten/);
    expect(f.message).not.toMatch(/Already saved/);
    expect(f.conflict).toBe(false);
  });

  it("explains a new sensor that the draft reports under a saved id", () => {
    const f = failed({
      ok: false,
      stage: "sensor",
      sensor: "a",
      action: "create",
      error: new SensorExistsError("a"),
      ...none,
    });
    expect(f.message).toMatch(
      /Sensor "a" already exists, but the draft treats it as new/,
    );
    expect(f.conflict).toBe(false);
  });

  it("carries the findings of a rejected sensor with its file", () => {
    const f = failed({
      ok: false,
      stage: "sensor",
      sensor: "a",
      action: "create",
      error: problem(422, "validation-failed", {
        diagnostics: [
          { severity: "error", message: "interval is not a duration", line: 2 },
        ],
      }),
      ...none,
    });
    expect(f.message).toMatch(/server rejected sensor "a"/);
    expect(f.diagnostics).toEqual([
      {
        severity: "error",
        message: "interval is not a duration",
        line: 2,
        file: "sensors/a.yaml",
      },
    ]);
    expect(f.conflict).toBe(false);
  });

  it("names the sensor and what was attempted when a call fails otherwise", () => {
    const f = failed({
      ok: false,
      stage: "sensor",
      sensor: "b",
      action: "update",
      error: new Error("network down"),
      ...none,
    });
    expect(f.message).toBe(
      'Sensor "b" could not be updated: network down. The flow was not saved.',
    );
  });

  it("lists the files that were saved when a later stage fails", () => {
    const written = {
      createdSensors: ["a"],
      updatedSensors: ["b"],
      createdPrompts: ["p"],
    };
    const atFlow = failed({
      ok: false,
      stage: "flow",
      error: problem(409, "conflict"),
      ...written,
    });
    expect(atFlow.conflict).toBe(true);
    expect(atFlow.message).toMatch(
      /Already saved: created sensor a; updated sensor b; created prompt p\./,
    );
    const atPrompt = failed({
      ok: false,
      stage: "prompt",
      prompt: "q",
      error: new Error("disk full"),
      ...written,
    });
    expect(atPrompt.message).toMatch(/Already saved: created sensor a;/);
  });
});

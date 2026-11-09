// Save of a flow draft, in this order: the sensors the agent drafted (new ones
// are created, edits replace the saved file), then the prompts it drafted that
// do not exist yet, then the flow file. A saved prompt or sensor is never
// replaced by accident: existing prompts are skipped, and a new sensor whose id
// is taken stops the save. A file that cannot be written stops the save before
// the next one. Pure apart from the calls on the `FlowSaveApi` it is given.

import type { AssistDraft } from "../../api/assist";
import { ApiError } from "../../api/client";
import {
  diagnosticsOf,
  isConflict,
  type Diagnostic,
} from "../files/diagnostics";
import { sensorDraftFile } from "./sensorDrafts";

export interface SavedFlow {
  id: string;
  hash: string;
}

/** The calls Save needs; the page backs them with the REST API. */
export interface FlowSaveApi {
  /** POST /api/sensors with the sensor's id and the file as written. */
  createSensor: (id: string, yaml: string) => Promise<unknown>;
  /** PUT /api/sensors/{id}, rejected with a conflict when the file's hash is not `expectedHash`. */
  updateSensor: (
    id: string,
    expectedHash: string,
    yaml: string,
  ) => Promise<unknown>;
  /** GET /api/sensors/{id}: the hash of the saved file now, read for an overwrite. */
  currentSensorHash: (id: string) => Promise<string>;
  /** POST /api/prompts with the prompt file as written. */
  createPrompt: (markdown: string) => Promise<unknown>;
  /** POST /api/flows with the flow's YAML. */
  createFlow: (yaml: string) => Promise<SavedFlow>;
  /** PUT /api/flows/{id}, rejected with a conflict when the file's hash is not `expectedHash`. */
  updateFlow: (
    id: string,
    expectedHash: string,
    yaml: string,
  ) => Promise<SavedFlow>;
}

/** A new flow, or an existing one with the hash of the file the draft started from. */
export type SaveTarget =
  { mode: "create" } | { mode: "update"; id: string; expectedHash: string };

export interface SaveOptions {
  /**
   * Replace sensors that changed on disk since the draft copied them: each
   * edit is made against the file's current hash instead of the hash it was
   * copied at.
   */
  overwrite?: boolean;
}

/** What a call wrote, also when it failed later. Ids are in save order. */
export interface Written {
  createdSensors: string[];
  /** Saved sensors that were replaced. */
  updatedSensors: string[];
  createdPrompts: string[];
}

export type SaveResult =
  | ({ ok: true; flow: SavedFlow } & Written)
  | ({
      ok: false;
      stage: "sensor";
      sensor: string;
      /** What was attempted on the sensor: creating the file or replacing it. */
      action: "create" | "update";
      error: unknown;
    } & Written)
  | ({ ok: false; stage: "prompt"; prompt: string; error: unknown } & Written)
  | ({ ok: false; stage: "flow"; error: unknown } & Written);

/**
 * The daemon reports a draft sensor as new while a saved file has its id.
 * Save stops without calling the API, so the saved sensor is never replaced.
 */
export class SensorExistsError extends Error {
  constructor(sensor: string) {
    super(`sensor ${sensor} already exists`);
    this.name = "SensorExistsError";
  }
}

/**
 * Writes the draft's sensors in draft order, then its new prompts in the order
 * the agent wrote them, then the flow. A sensor without `base_hash` is created;
 * one with it replaces the saved file if that still has this hash (with
 * `overwrite`, whatever hash it has now). The result names the files this call
 * wrote, also when it fails later.
 */
export async function saveFlowDraft(
  api: FlowSaveApi,
  draft: Pick<AssistDraft, "yaml" | "prompts" | "sensors">,
  target: SaveTarget,
  options: SaveOptions = {},
): Promise<SaveResult> {
  const written: Written = {
    createdSensors: [],
    updatedSensors: [],
    createdPrompts: [],
  };
  for (const s of draft.sensors) {
    const action = s.base_hash === null ? "create" : "update";
    const stop = (error: unknown): SaveResult => ({
      ok: false,
      stage: "sensor",
      sensor: s.id,
      action,
      error,
      ...written,
    });
    if (s.base_hash === null) {
      if (s.exists) return stop(new SensorExistsError(s.id));
      try {
        await api.createSensor(s.id, s.yaml);
      } catch (error) {
        return stop(error);
      }
      written.createdSensors.push(s.id);
    } else {
      try {
        const expected = options.overwrite
          ? await api.currentSensorHash(s.id)
          : s.base_hash;
        await api.updateSensor(s.id, expected, s.yaml);
      } catch (error) {
        return stop(error);
      }
      written.updatedSensors.push(s.id);
    }
  }
  for (const p of draft.prompts) {
    if (p.exists) continue;
    try {
      await api.createPrompt(p.yaml);
    } catch (error) {
      return { ok: false, stage: "prompt", prompt: p.name, error, ...written };
    }
    written.createdPrompts.push(p.name);
  }
  try {
    const flow =
      target.mode === "create"
        ? await api.createFlow(draft.yaml)
        : await api.updateFlow(target.id, target.expectedHash, draft.yaml);
    return { ok: true, flow, ...written };
  } catch (error) {
    return { ok: false, stage: "flow", error, ...written };
  }
}

const noun = (n: number, word: string) => (n === 1 ? word : `${word}s`);

/**
 * What Save will write, for the line next to the button: the flow, the draft's
 * sensors (new ones and edits of saved ones, by id) and the prompts that do not
 * exist yet.
 */
export function describeSavePlan(
  draft: Pick<AssistDraft, "prompts" | "sensors">,
  target: Pick<SaveTarget, "mode">,
): string {
  const parts = [target.mode === "create" ? "1 new flow" : "1 flow"];
  if (draft.sensors.length > 0) {
    const fresh = draft.sensors.filter((s) => s.base_hash === null);
    const edited = draft.sensors.filter((s) => s.base_hash !== null);
    const groups: string[] = [];
    if (fresh.length > 0) {
      groups.push(`new: ${fresh.map((s) => s.id).join(", ")}`);
    }
    if (edited.length > 0) {
      groups.push(`updated: ${edited.map((s) => s.id).join(", ")}`);
    }
    parts.push(
      `${draft.sensors.length} ${noun(draft.sensors.length, "sensor")} (${groups.join("; ")})`,
    );
  }
  const prompts = draft.prompts.filter((p) => !p.exists).length;
  if (prompts > 0) parts.push(`${prompts} ${noun(prompts, "prompt")}`);
  return `Saves ${parts.join(", ")}`;
}

export interface SaveFailure {
  message: string;
  /** Findings of the server's rejection; those of a sensor carry its file. */
  diagnostics: Diagnostic[];
  /** A file changed on disk since the draft started: saving again with `overwrite` replaces it. */
  conflict: boolean;
}

/** " Already saved: …" for the files an earlier stage wrote; empty when there are none. */
function alreadySaved(w: Written): string {
  const parts: string[] = [];
  const add = (verb: string, word: string, written: readonly string[]) => {
    if (written.length > 0) {
      parts.push(`${verb} ${noun(written.length, word)} ${written.join(", ")}`);
    }
  };
  add("created", "sensor", w.createdSensors);
  add("updated", "sensor", w.updatedSensors);
  add("created", "prompt", w.createdPrompts);
  return parts.length === 0 ? "" : ` Already saved: ${parts.join("; ")}.`;
}

/** The message of a failure as a sentence; the daemon's messages do not always end with a full stop. */
function reasonOf(e: unknown): string {
  const text = e instanceof Error ? e.message : String(e);
  return /[.!?]$/.test(text) ? text : `${text}.`;
}

function describeSensorFailure(
  r: Extract<SaveResult, { ok: false; stage: "sensor" }>,
  diagnostics: Diagnostic[],
  saved: string,
): SaveFailure {
  const file = sensorDraftFile(r.sensor);
  const located = diagnostics.map((d) => ({ ...d, file: d.file ?? file }));
  const reported = r.error instanceof SensorExistsError;
  const refused =
    r.action === "create" &&
    r.error instanceof ApiError &&
    r.error.status === 409;
  if (reported || refused) {
    const how = reported
      ? ", but the draft treats it as new"
      : " (created meanwhile)";
    return {
      message: `Sensor "${r.sensor}" already exists${how}. It was not overwritten and the flow was not saved.${saved} Rename the sensor in the draft or ask the assistant to edit the existing one.`,
      diagnostics: located,
      conflict: false,
    };
  }
  if (isConflict(r.error)) {
    return {
      message: `Sensor "${r.sensor}" changed on disk since this session copied it. The flow was not saved.${saved} Overwrite saves the draft over the files that changed.`,
      diagnostics: located,
      conflict: true,
    };
  }
  if (located.length > 0) {
    return {
      message: `The server rejected sensor "${r.sensor}"; see the findings. The flow was not saved.${saved}`,
      diagnostics: located,
      conflict: false,
    };
  }
  const verb = r.action === "create" ? "created" : "updated";
  return {
    message: `Sensor "${r.sensor}" could not be ${verb}: ${reasonOf(r.error)} The flow was not saved.${saved}`,
    diagnostics: located,
    conflict: false,
  };
}

/** The text and findings to show for a failed save. */
export function describeSaveFailure(
  r: Extract<SaveResult, { ok: false }>,
): SaveFailure {
  const diagnostics = diagnosticsOf(r.error) ?? [];
  const saved = alreadySaved(r);
  if (r.stage === "sensor") return describeSensorFailure(r, diagnostics, saved);
  if (r.stage === "prompt") {
    const taken = r.error instanceof ApiError && r.error.status === 409;
    return {
      message: taken
        ? `Prompt "${r.prompt}" already exists (it was created meanwhile). The flow was not saved.${saved} Rename the prompt in the draft or ask the assistant to use the existing one.`
        : `Prompt "${r.prompt}" could not be created: ${reasonOf(r.error)} The flow was not saved.${saved}`,
      diagnostics,
      conflict: false,
    };
  }
  if (isConflict(r.error)) {
    return {
      message: `The file changed on disk since this session started. Saving again overwrites it.${saved}`,
      diagnostics,
      conflict: true,
    };
  }
  if (diagnostics.length > 0) {
    return {
      message: `The server rejected the flow; see Problems.${saved}`,
      diagnostics,
      conflict: false,
    };
  }
  return {
    message: `${reasonOf(r.error)}${saved}`,
    diagnostics,
    conflict: false,
  };
}

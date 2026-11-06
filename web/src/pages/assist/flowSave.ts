// Save of a flow draft: the prompts the agent drafted are created first, then
// the flow file. Existing prompts are never overwritten, and a prompt that
// cannot be created stops the save before the flow is written. Pure apart from
// the calls on the `FlowSaveApi` it is given.

import type { AssistDraft } from "../../api/assist";
import { ApiError } from "../../api/client";
import {
  diagnosticsOf,
  isConflict,
  type Diagnostic,
} from "../files/diagnostics";

export interface SavedFlow {
  id: string;
  hash: string;
}

/** The calls Save needs; the page backs them with the REST API. */
export interface FlowSaveApi {
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

export type SaveResult =
  | { ok: true; flow: SavedFlow; createdPrompts: string[] }
  | {
      ok: false;
      stage: "prompt";
      prompt: string;
      error: unknown;
      createdPrompts: string[];
    }
  | { ok: false; stage: "flow"; error: unknown; createdPrompts: string[] };

/**
 * Creates the draft's new prompts in the order the agent wrote them, then the
 * flow. `createdPrompts` names the prompts that exist because of this call,
 * also when it fails later.
 */
export async function saveFlowDraft(
  api: FlowSaveApi,
  draft: Pick<AssistDraft, "yaml" | "prompts">,
  target: SaveTarget,
): Promise<SaveResult> {
  const createdPrompts: string[] = [];
  for (const p of draft.prompts) {
    if (p.exists) continue;
    try {
      await api.createPrompt(p.yaml);
      createdPrompts.push(p.name);
    } catch (error) {
      return {
        ok: false,
        stage: "prompt",
        prompt: p.name,
        error,
        createdPrompts,
      };
    }
  }
  try {
    const flow =
      target.mode === "create"
        ? await api.createFlow(draft.yaml)
        : await api.updateFlow(target.id, target.expectedHash, draft.yaml);
    return { ok: true, flow, createdPrompts };
  } catch (error) {
    return { ok: false, stage: "flow", error, createdPrompts };
  }
}

export interface SaveFailure {
  message: string;
  diagnostics: Diagnostic[];
  /** The flow file changed on disk since the draft started: Save again to overwrite it. */
  conflict: boolean;
}

/** The text and findings to show for a failed save. */
export function describeSaveFailure(
  r: Extract<SaveResult, { ok: false }>,
): SaveFailure {
  const diagnostics = diagnosticsOf(r.error) ?? [];
  if (r.stage === "prompt") {
    const taken = r.error instanceof ApiError && r.error.status === 409;
    const why = r.error instanceof Error ? r.error.message : String(r.error);
    return {
      message: taken
        ? `Prompt "${r.prompt}" already exists (it was created meanwhile). The flow was not saved. Rename the prompt in the draft or ask the assistant to use the existing one.`
        : `Prompt "${r.prompt}" could not be created: ${why} The flow was not saved.`,
      diagnostics,
      conflict: false,
    };
  }
  if (isConflict(r.error)) {
    return {
      message:
        "The file changed on disk since this session started. Saving again overwrites it.",
      diagnostics,
      conflict: true,
    };
  }
  if (diagnostics.length > 0) {
    return {
      message: "The server rejected the flow; see Problems.",
      diagnostics,
      conflict: false,
    };
  }
  return {
    message: r.error instanceof Error ? r.error.message : String(r.error),
    diagnostics,
    conflict: false,
  };
}

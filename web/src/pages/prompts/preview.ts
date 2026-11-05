import type { PromptRenderRequest } from "../../api/prompts";

/** Where the preview's `.event` / `.input` come from. */
export type Sample =
  { kind: "event"; eventId: string | null } | { kind: "json"; text: string };

export type RenderPlan =
  | { ok: true; request: PromptRenderRequest }
  | { ok: false; reason: "no-sample" | "invalid-json"; message: string };

/**
 * Builds the POST /api/prompts/render body for unsaved text against a sample,
 * or says why no request can be made yet. Pasted JSON is parsed here so a typo
 * does not cost a round trip.
 */
export function planRender(
  markdown: string,
  sample: Sample,
  extraPrompt: string,
): RenderPlan {
  const extra = extraPrompt.trim() === "" ? {} : { extra_prompt: extraPrompt };
  if (sample.kind === "event") {
    if (sample.eventId === null) {
      return {
        ok: false,
        reason: "no-sample",
        message: "Pick an event to render the prompt against.",
      };
    }
    return {
      ok: true,
      request: { markdown, event_id: sample.eventId, ...extra },
    };
  }
  if (sample.text.trim() === "") {
    return {
      ok: false,
      reason: "no-sample",
      message: "Paste the JSON of a sensor item to render the prompt against.",
    };
  }
  try {
    const input: unknown = JSON.parse(sample.text);
    return { ok: true, request: { markdown, input, ...extra } };
  } catch (e) {
    return {
      ok: false,
      reason: "invalid-json",
      message: `Invalid JSON: ${e instanceof Error ? e.message : String(e)}`,
    };
  }
}

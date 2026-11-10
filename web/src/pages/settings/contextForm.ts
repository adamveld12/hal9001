/** Matches the daemon's cap on `system_prompt` (16 KiB, counted in UTF-8 bytes). */
export const MAX_SYSTEM_PROMPT_BYTES = 16 << 10;

export function byteLength(s: string): number {
  return new TextEncoder().encode(s).length;
}

/** Client-side check of the system prompt; the daemon enforces the same limit with a 422. */
export function systemPromptError(s: string): string | undefined {
  const n = byteLength(s);
  return n > MAX_SYSTEM_PROMPT_BYTES
    ? `${n} bytes is over the ${MAX_SYSTEM_PROMPT_BYTES} byte limit.`
    : undefined;
}

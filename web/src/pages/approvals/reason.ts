/** Clip a reason to its first `maxLines` lines; `clipped` says whether text was left out. */
export function clipReason(
  text: string,
  maxLines = 8,
): { text: string; clipped: boolean } {
  const lines = text.split("\n");
  if (lines.length <= maxLines) return { text, clipped: false };
  return { text: lines.slice(0, maxLines).join("\n"), clipped: true };
}

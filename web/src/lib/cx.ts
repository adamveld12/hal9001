/** Join class names, skipping falsy values. */
export function cx(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(" ");
}

/**
 * Default `w-full` for form controls unless the caller sets its own width
 * (`w-56`, `w-1/2`, `max-w-*` does not count). Tailwind orders `w-full` after
 * the spacing widths in the generated CSS, so passing both would let `w-full`
 * win and ignore the caller's width.
 */
export function defaultFullWidth(className: string | undefined): string {
  if (className && /(^|\s)(?:[a-z-]+:)*w-/.test(className)) return "";
  return "w-full";
}

import { useEffect } from "react";

export const FAVICON_FLASH_MS = 1000;

const ACTIVE_FAVICON =
  "data:image/svg+xml," +
  encodeURIComponent(
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="7" fill="#16171b"/><circle cx="16" cy="16" r="13" fill="#f06a6a" opacity=".35"/><circle cx="16" cy="16" r="9" fill="#ff3b3b"/><circle cx="16" cy="16" r="3.5" fill="#fff"/></svg>',
  );

/** Favicon href for a given tick; undefined means "leave the original". */
export function faviconFrame(
  runningFlowRuns: number | undefined,
  tick: number,
  reducedMotion: boolean,
): "normal" | "active" {
  if ((runningFlowRuns ?? 0) <= 0) return "normal";
  if (reducedMotion) return "active";
  return tick % 2 === 0 ? "active" : "normal";
}

export function faviconTitle(
  base: string,
  runningFlowRuns: number | undefined,
): string {
  const n = runningFlowRuns ?? 0;
  return n > 0 ? `(${n}) ${base}` : base;
}

/** Flashes the tab favicon (and prefixes the title with the count) while flows run. */
export function useFaviconActivity(runningFlowRuns: number | undefined): void {
  const active = (runningFlowRuns ?? 0) > 0;
  useEffect(() => {
    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
    if (!active || !link) return;
    const original = link.href;
    const reduced = window.matchMedia?.(
      "(prefers-reduced-motion: reduce)",
    ).matches;
    const apply = (tick: number) => {
      link.href =
        faviconFrame(runningFlowRuns, tick, !!reduced) === "active"
          ? ACTIVE_FAVICON
          : original;
    };
    let tick = 0;
    apply(tick);
    const id = reduced
      ? undefined
      : window.setInterval(() => apply(++tick), FAVICON_FLASH_MS);
    return () => {
      if (id !== undefined) window.clearInterval(id);
      link.href = original;
    };
    // runningFlowRuns only matters through `active`
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active]);

  useEffect(() => {
    const base = document.title;
    document.title = faviconTitle(base, runningFlowRuns);
    return () => {
      document.title = base;
    };
  }, [runningFlowRuns]);
}

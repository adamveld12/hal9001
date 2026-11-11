import { createContext, useContext, useEffect, useRef } from "react";
import { PageContextRegistry, type PageContextInput } from "../lib/pageContext";

/** Provided once by `PageContextProvider` (the app shell). */
export const PageContextRegistryContext =
  createContext<PageContextRegistry | null>(null);

/** The registry the drawer reads when it sends a turn. */
export function usePageContextRegistry(): PageContextRegistry {
  const r = useContext(PageContextRegistryContext);
  if (!r) throw new Error("usePageContextRegistry outside PageContextProvider");
  return r;
}

/**
 * Publish what this page shows to the assistant. Call it from the page
 * component with a compact object (titles, ids, counts, statuses; never
 * secrets or environment values). The latest value is read when a turn is
 * sent, so it does not re-render anything; it is released when the page
 * unmounts. Pass `null` while the page has nothing to say yet.
 */
export function usePageContext(input: PageContextInput | null): void {
  const registry = usePageContextRegistry();
  const owner = useRef<symbol | null>(null);
  owner.current ??= Symbol("page-context");
  // Only a change in content updates the registry, not a new object each render.
  const key = input === null ? null : JSON.stringify(input);
  useEffect(() => {
    const me = owner.current;
    if (me === null) return;
    if (key === null) {
      registry.clear(me);
      return;
    }
    registry.set(me, JSON.parse(key) as PageContextInput);
  }, [registry, key]);
  useEffect(() => {
    const me = owner.current;
    return () => {
      if (me !== null) registry.clear(me);
    };
  }, [registry]);
}

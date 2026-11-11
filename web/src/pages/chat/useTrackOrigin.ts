import { useEffect } from "react";
import { useLocation } from "react-router";
import { loadOrigin, rememberOrigin, saveOrigin } from "./origin";

/**
 * Remember the last page that was not the Chat page, for `Back to drawer`.
 * Called once by the app shell.
 */
export function useTrackOrigin(): void {
  const { pathname, search } = useLocation();
  useEffect(() => {
    const next = rememberOrigin(loadOrigin(), { pathname, search });
    if (next !== null) saveOrigin(next);
  }, [pathname, search]);
}

import { useMemo, type ReactNode } from "react";
import { PageContextRegistry } from "../lib/pageContext";
import { PageContextRegistryContext } from "./pageContext";

/** Mounted once by the app shell, above the routed page and the assistant drawer. */
export function PageContextProvider({ children }: { children: ReactNode }) {
  const registry = useMemo(() => new PageContextRegistry(), []);
  return (
    <PageContextRegistryContext.Provider value={registry}>
      {children}
    </PageContextRegistryContext.Provider>
  );
}

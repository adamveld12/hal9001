import { useSyncExternalStore } from "react";

export type ThemeChoice = "light" | "dark" | "system";

const KEY = "hal9001.theme";
const listeners = new Set<() => void>();

export function readTheme(): ThemeChoice {
  const v = localStorage.getItem(KEY);
  return v === "light" || v === "dark" ? v : "system";
}

/** Apply the choice to <html>. "system" removes the attribute so CSS media queries decide. */
export function applyTheme(choice: ThemeChoice): void {
  const root = document.documentElement;
  if (choice === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", choice);
}

export function setTheme(choice: ThemeChoice): void {
  if (choice === "system") localStorage.removeItem(KEY);
  else localStorage.setItem(KEY, choice);
  applyTheme(choice);
  listeners.forEach((l) => l());
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useTheme(): ThemeChoice {
  return useSyncExternalStore(subscribe, readTheme);
}

/** Whether the effective palette is dark right now. */
export function isDark(choice: ThemeChoice): boolean {
  return (
    choice === "dark" ||
    (choice === "system" && matchMedia("(prefers-color-scheme: dark)").matches)
  );
}

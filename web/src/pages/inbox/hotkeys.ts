import { useEffect, useRef } from "react";

/** The parts of a KeyboardEvent that decide whether a shortcut applies. */
export interface KeyLike {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  defaultPrevented?: boolean;
  target: unknown;
}

interface ElementLike {
  tagName?: string;
  isContentEditable?: boolean;
}
const TEXT_ENTRY: Record<string, true> = {
  INPUT: true,
  TEXTAREA: true,
  SELECT: true,
};
const ACTIVATES_ON_ENTER: Record<string, true> = {
  BUTTON: true,
  A: true,
  SUMMARY: true,
};

/** True for form fields, `contenteditable` regions (CodeMirror), where keys are text. */
export function isTypingTarget(target: unknown): boolean {
  const el = target as ElementLike | null;
  if (!el || typeof el !== "object") return false;
  return (
    el.isContentEditable === true ||
    (el.tagName !== undefined &&
      Object.hasOwn(TEXT_ENTRY, el.tagName.toUpperCase()))
  );
}

/**
 * Single-key shortcuts must not fire while typing, with Ctrl/Cmd/Alt held
 * (browser and OS shortcuts), or when something already handled the key.
 * Enter on a focused button or link activates that control instead.
 */
export function shouldIgnoreHotkey(e: KeyLike): boolean {
  if (e.ctrlKey || e.metaKey || e.altKey || e.defaultPrevented) return true;
  if (isTypingTarget(e.target)) return true;
  if (e.key === "Enter") {
    const tag = (e.target as ElementLike | null)?.tagName;
    return (
      tag !== undefined && Object.hasOwn(ACTIVATES_ON_ENTER, tag.toUpperCase())
    );
  }
  return false;
}

/**
 * Bind single keys (`e.key` values such as "j", "Enter", "Escape") on the
 * window. The handlers may change on every render; the listener is attached
 * once. Bound keys have their default action prevented.
 */
export function useHotkeys(
  bindings: Record<string, (e: KeyboardEvent) => void>,
  enabled = true,
): void {
  const latest = useRef(bindings);
  useEffect(() => {
    latest.current = bindings;
  });
  useEffect(() => {
    if (!enabled) return;
    const onKey = (e: KeyboardEvent) => {
      if (shouldIgnoreHotkey(e)) return;
      const fn = latest.current[e.key];
      if (!fn) return;
      e.preventDefault();
      fn(e);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [enabled]);
}

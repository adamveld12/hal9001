import { useSyncExternalStore } from "react";
import type { Tone } from "../../lib/status";

export interface TimeLeft {
  /** Milliseconds until the deadline; 0 once it has passed. */
  ms: number;
  expired: boolean;
  /** "0:42", "12:05", "1h 02m", or "expired". */
  label: string;
  tone: Tone;
}

/** Whole seconds are shown, rounded up, so the label reads 0:01 until the deadline passes. */
export function formatCountdown(ms: number): string {
  if (ms <= 0) return "expired";
  const total = Math.ceil(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}h ${String(m).padStart(2, "0")}m`;
  return `${m}:${String(s).padStart(2, "0")}`;
}

const URGENT_MS = 60_000;
const SOON_MS = 5 * 60_000;

/**
 * Time left until `expiresAt` as of `now`. Null when the approval has no
 * deadline or the timestamp is unreadable.
 */
export function timeLeft(
  expiresAt: string | null | undefined,
  now: number,
): TimeLeft | null {
  if (!expiresAt) return null;
  const deadline = Date.parse(expiresAt);
  if (Number.isNaN(deadline)) return null;
  const ms = Math.max(0, deadline - now);
  return {
    ms,
    expired: ms === 0,
    label: formatCountdown(ms),
    tone: ms === 0 || ms < URGENT_MS ? "bad" : ms < SOON_MS ? "warn" : "muted",
  };
}

// One shared one-second ticker for every countdown on screen.
const listeners = new Set<() => void>();
let timer: ReturnType<typeof setInterval> | undefined;

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  timer ??= setInterval(() => {
    for (const l of listeners) l();
  }, 1000);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0 && timer !== undefined) {
      clearInterval(timer);
      timer = undefined;
    }
  };
}

const currentSecond = () => Math.floor(Date.now() / 1000) * 1000;

/** Current time in ms, re-rendering the caller once a second. */
export function useNow(): number {
  return useSyncExternalStore(subscribe, currentSecond);
}

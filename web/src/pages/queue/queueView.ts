import type { Queue, QueueItem } from "../../api/queue";

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

/** What a queued run waits for, in plain words, with how full the pool is. */
export function waitingText(item: QueueItem, limits: Queue["limits"]): string {
  switch (item.waiting_for) {
    case "agent_slot":
      return `waiting for an agent slot (${limits.agent_runs_in_use} of ${limits.max_agent_runs} in use)`;
    case "command_slot":
      return `waiting for a command slot (${limits.command_runs_in_use} of ${limits.max_command_runs} in use)`;
    case "flow_slot": {
      const f = limits.flows.find((x) => x.flow_id === item.flow_run.flow_id);
      return f
        ? `waiting for a slot in ${f.flow_id} (${f.running} of ${f.max_concurrent} running)`
        : "waiting for a flow slot";
    }
    case "kill_switch":
      return "waiting: the kill switch is engaged";
  }
}

/** The ids of `selected` that are still in the queue (a run that started or was removed drops out). */
export function pruneSelection(
  selected: ReadonlySet<string>,
  items: readonly QueueItem[],
): Set<string> {
  const present = new Set(items.map((i) => i.flow_run.id));
  return new Set([...selected].filter((id) => present.has(id)));
}

export function toggleSelected(
  selected: ReadonlySet<string>,
  id: string,
): Set<string> {
  const next = new Set(selected);
  if (!next.delete(id)) next.add(id);
  return next;
}

/** Select every item, or none when all are already selected. */
export function toggleAll(
  selected: ReadonlySet<string>,
  items: readonly QueueItem[],
): Set<string> {
  return selected.size === items.length && items.length > 0
    ? new Set()
    : new Set(items.map((i) => i.flow_run.id));
}

/** The confirmation text of "Clear queue". */
export function clearConfirmText(count: number, flowId: string): string {
  const what = flowId
    ? `${count} queued ${plural(count, "run", "runs")} of ${flowId}`
    : `${count} queued ${plural(count, "run", "runs")}`;
  return `Remove ${what}? ${count === 1 ? "It is" : "They are"} cancelled and kept in history. Runs that already started are not affected.`;
}

/** The note shown after removing runs; failures name what could not be removed. */
export function removeNote(r: {
  removed: readonly string[];
  failed: readonly { id: string; message: string }[];
}): string {
  const n = r.removed.length;
  const done = `Removed ${n} ${plural(n, "run", "runs")} from the queue.`;
  if (r.failed.length === 0) return done;
  const k = r.failed.length;
  return `${done} ${k} could not be removed (${r.failed[0]?.message ?? "unknown error"}).`;
}

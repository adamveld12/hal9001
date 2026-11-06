// Output ports of a transform node: `out`, then every port a route step names
// (rules in order, then `default_port`), deduplicated. `error` is implicit and
// is added by model.ts like for every node.

import type { TransformStep } from "./types";

const OUT = "out";
const ERROR = "error";

export function transformPorts(
  steps: readonly TransformStep[] | undefined,
): string[] {
  const ports = [OUT];
  const add = (name: string | undefined) => {
    // `error` is implicit; validation reports a route that names it.
    if (
      name !== undefined &&
      name.trim() !== "" &&
      name !== ERROR &&
      !ports.includes(name)
    ) {
      ports.push(name);
    }
  };
  for (const step of steps ?? []) {
    if (step.type !== "route") continue;
    for (const rule of step.rules ?? []) add(rule.port);
    add(step.default_port);
  }
  return ports;
}

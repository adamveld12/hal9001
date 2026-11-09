export function eyeThrobs(runningFlowRuns: number | undefined): boolean {
  return (runningFlowRuns ?? 0) > 0;
}

export function eyeLabel(runningFlowRuns: number | undefined): string {
  const n = runningFlowRuns ?? 0;
  return n > 0 ? `${n} ${n === 1 ? "flow" : "flows"} running` : "hal9001";
}

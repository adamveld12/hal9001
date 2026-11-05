export interface MeasuredDims {
  width: number;
  height: number;
}

/**
 * Returns the nodes with React Flow's measured sizes attached. Nodes without a
 * recorded size, and the original array when nothing was measured yet, are
 * returned as they are so memoised children keep their identity.
 */
export function withMeasured<N extends { id: string }>(
  nodes: N[],
  dims: ReadonlyMap<string, MeasuredDims>,
): (N & { measured?: MeasuredDims })[] {
  if (dims.size === 0) return nodes;
  return nodes.map((n) => {
    const d = dims.get(n.id);
    return d ? { ...n, measured: d } : n;
  });
}

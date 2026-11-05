// Line diff for tool-call edits (ACP `diff` content: old text -> new text).

export type DiffLine =
  { type: "eq" | "add" | "del"; text: string } | { type: "gap"; count: number };

/** Above this many LCS cells the middle is shown as a plain replace. */
const MAX_CELLS = 4_000_000;

function lines(text: string): string[] {
  if (text === "") return [];
  const out = text.split("\n");
  if (out[out.length - 1] === "") out.pop();
  return out;
}

/** Line diff of `oldText` (null for a new file) against `newText`. */
export function lineDiff(oldText: string | null, newText: string): DiffLine[] {
  const a = lines(oldText ?? "");
  const b = lines(newText);

  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }

  const out: DiffLine[] = [];
  for (let i = 0; i < start; i++) out.push({ type: "eq", text: a[i] });

  const midA = a.slice(start, endA);
  const midB = b.slice(start, endB);
  if (midA.length * midB.length > MAX_CELLS) {
    for (const t of midA) out.push({ type: "del", text: t });
    for (const t of midB) out.push({ type: "add", text: t });
  } else {
    out.push(...lcsDiff(midA, midB));
  }

  for (let i = endA; i < a.length; i++) out.push({ type: "eq", text: a[i] });
  return out;
}

function lcsDiff(a: string[], b: string[]): DiffLine[] {
  const n = a.length;
  const m = b.length;
  // len[i][j] = LCS length of a[i..] and b[j..]
  const len: Uint32Array[] = [];
  for (let i = 0; i <= n; i++) len.push(new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      len[i][j] =
        a[i] === b[j]
          ? len[i + 1][j + 1] + 1
          : Math.max(len[i + 1][j], len[i][j + 1]);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ type: "eq", text: a[i] });
      i++;
      j++;
    } else if (len[i + 1][j] >= len[i][j + 1]) {
      out.push({ type: "del", text: a[i++] });
    } else {
      out.push({ type: "add", text: b[j++] });
    }
  }
  while (i < n) out.push({ type: "del", text: a[i++] });
  while (j < m) out.push({ type: "add", text: b[j++] });
  return out;
}

/** Replaces runs of unchanged lines longer than 2*`context` with a gap marker. */
export function collapseContext(diff: DiffLine[], context = 3): DiffLine[] {
  const out: DiffLine[] = [];
  let i = 0;
  while (i < diff.length) {
    if (diff[i].type !== "eq") {
      out.push(diff[i++]);
      continue;
    }
    let j = i;
    while (j < diff.length && diff[j].type === "eq") j++;
    const run = diff.slice(i, j);
    const head = i === 0 ? 0 : context;
    const tail = j === diff.length ? 0 : context;
    if (run.length > head + tail + 1) {
      out.push(...run.slice(0, head));
      out.push({ type: "gap", count: run.length - head - tail });
      out.push(...run.slice(run.length - tail));
    } else {
      out.push(...run);
    }
    i = j;
  }
  return out;
}

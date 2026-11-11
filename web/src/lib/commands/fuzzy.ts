// Fuzzy matching for the command palette: the query's characters must appear
// in order in the text. The best alignment wins; matches at the start of the
// text or of a word and runs of consecutive characters score higher.

export interface FuzzyMatch {
  score: number;
  /** Positions in `text` of the matched characters, for highlighting. */
  indices: number[];
}

const WORD_START = 5;
const TEXT_START = 8;
const CONSECUTIVE = 6;
const GAP = 0.5;
const LEAD = 0.3;
const PREFIX = 10;
const EXACT = 20;

function isAlnum(c: string): boolean {
  return /[\p{L}\p{N}]/u.test(c);
}

function boundaryBonus(text: string, j: number): number {
  if (j === 0) return TEXT_START;
  const prev = text.charAt(j - 1);
  const cur = text.charAt(j);
  if (!isAlnum(prev)) return WORD_START;
  if (prev === prev.toLowerCase() && cur !== cur.toLowerCase()) return 4;
  return 0;
}

/**
 * Match `query` against `text`, ignoring case and whitespace in the query.
 * Null when the characters do not occur in order. An empty query matches
 * everything with score 0.
 */
export function fuzzyMatch(query: string, text: string): FuzzyMatch | null {
  const q = Array.from(query.toLowerCase().replace(/\s+/g, ""));
  if (q.length === 0) return { score: 0, indices: [] };
  const chars = Array.from(text);
  const lower = chars.map((c) => c.toLowerCase());
  const n = chars.length;
  const m = q.length;
  if (m > n) return null;

  // best[i][j]: best score with q[i] matched at text position j; from[i][j] is where q[i-1] was matched.
  const best: number[][] = [];
  const from: number[][] = [];
  for (let i = 0; i < m; i++) {
    const row: number[] = new Array<number>(n).fill(-Infinity);
    const back: number[] = new Array<number>(n).fill(-1);
    best.push(row);
    from.push(back);
    // Best of best[i-1][k] + GAP*k over k <= j-2, so a gap of j-k-1 costs GAP*(j-k-1).
    let runMax = -Infinity;
    let runArg = -1;
    for (let j = 0; j < n; j++) {
      if (i > 0 && j >= 2) {
        const prevRow = best[i - 1];
        const k = j - 2;
        const v = (prevRow?.[k] ?? -Infinity) + GAP * k;
        if (v > runMax) {
          runMax = v;
          runArg = k;
        }
      }
      if (lower[j] !== q[i]) continue;
      const base = 1 + boundaryBonus(text, j);
      if (i === 0) {
        row[j] = base - Math.min(j, 10) * LEAD;
        continue;
      }
      let top = -Infinity;
      let topFrom = -1;
      const adj = best[i - 1]?.[j - 1] ?? -Infinity;
      if (j >= 1 && adj > -Infinity) {
        top = adj + CONSECUTIVE;
        topFrom = j - 1;
      }
      if (runMax > -Infinity) {
        const viaGap = runMax - GAP * (j - 1);
        if (viaGap > top) {
          top = viaGap;
          topFrom = runArg;
        }
      }
      if (topFrom >= 0) {
        row[j] = top + base;
        back[j] = topFrom;
      }
    }
  }

  const last = best[m - 1];
  if (!last) return null;
  let end = -1;
  let score = -Infinity;
  for (let j = 0; j < n; j++) {
    const v = last[j] ?? -Infinity;
    if (v > score) {
      score = v;
      end = j;
    }
  }
  if (end < 0) return null;
  const indices: number[] = new Array<number>(m);
  let j = end;
  for (let i = m - 1; i >= 0; i--) {
    indices[i] = j;
    j = from[i]?.[j] ?? -1;
  }
  const joined = q.join("");
  const full = lower.join("").replace(/\s+/g, "");
  if (full === joined) score += EXACT;
  else if (full.startsWith(joined)) score += PREFIX;
  score -= (n - m) * 0.05;
  return { score, indices };
}

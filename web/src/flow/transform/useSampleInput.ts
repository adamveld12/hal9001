import { useEffect, useMemo, useState } from "react";
import { useFlowSamples, type FlowSample } from "../../api/transform";
import { parseInput, sampleKeys, type ParsedInput } from "./preview";

export const PASTE = "paste";

export interface SampleInput {
  samples: FlowSample[];
  loading: boolean;
  error: unknown;
  keys: string[];
  /** Selected sample key, or `PASTE`. */
  choice: string;
  setChoice: (key: string) => void;
  pasted: string;
  setPasted: (text: string) => void;
  /** Parse result of the pasted text; null while it is empty. */
  parsed: ParsedInput | null;
  /** The JSON the preview and field pickers work on; undefined when there is none. */
  input: unknown;
}

/**
 * The sample the editor works on: one of the node's recent real inputs (the
 * newest until the user picks another) or JSON pasted by the user.
 */
export function useSampleInput(flowId: string, nodeId: string): SampleInput {
  const query = useFlowSamples(flowId, nodeId);
  const samples = useMemo(() => query.data ?? [], [query.data]);
  const keys = useMemo(() => sampleKeys(samples), [samples]);
  const [picked, setPicked] = useState<string | null>(null);
  const [pasted, setPasted] = useState("");

  const choice =
    picked === PASTE
      ? PASTE
      : picked !== null && keys.includes(picked)
        ? picked
        : (keys[0] ?? PASTE);

  const parsed = useMemo(() => parseInput(pasted), [pasted]);
  const input = useMemo(() => {
    if (choice === PASTE) return parsed?.ok ? parsed.value : undefined;
    return samples[keys.indexOf(choice)]?.payload;
  }, [choice, parsed, samples, keys]);

  return {
    samples,
    loading: query.isLoading,
    error: query.error,
    keys,
    choice,
    setChoice: setPicked,
    pasted,
    setPasted,
    parsed,
    input,
  };
}

/** `flag`, but only true once it has stayed true for `ms`: avoids flicker on fast updates. */
export function useDelayedFlag(flag: boolean, ms: number): boolean {
  const [ready, setReady] = useState(false);
  useEffect(() => {
    if (!flag) return;
    const t = setTimeout(() => setReady(true), ms);
    return () => {
      clearTimeout(t);
      setReady(false);
    };
  }, [flag, ms]);
  return flag && ready;
}

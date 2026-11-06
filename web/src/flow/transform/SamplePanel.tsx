// Sample picker: one of the node's recent real inputs or pasted JSON. The
// choice is the preview input and drives the field pickers.

import { useMemo } from "react";
import { SensorItemsTable } from "../../components/SensorItemsTable";
import { Select, TextArea } from "../../components/ui";
import { sampleLabel } from "./preview";
import { PASTE, type SampleInput } from "./useSampleInput";

export function SamplePanel({ sample }: { sample: SampleInput }) {
  const items = useMemo(
    () => (sample.input === undefined ? [] : [{ item: sample.input }]),
    [sample.input],
  );
  const pasting = sample.choice === PASTE;
  const noSamples = sample.samples.length === 0;

  return (
    <section className="space-y-2" aria-label="Sample input">
      <h3 className="m-0 text-xs font-semibold">Sample input</h3>
      <Select
        aria-label="Sample"
        value={sample.choice}
        onChange={(e) => sample.setChoice(e.target.value)}
      >
        {sample.samples.map((s, i) => (
          <option key={sample.keys[i]} value={sample.keys[i]}>
            {sampleLabel(s)}
          </option>
        ))}
        <option value={PASTE}>Paste JSON…</option>
      </Select>
      {pasting && (
        <>
          <TextArea
            rows={5}
            aria-label="Pasted JSON"
            spellCheck={false}
            placeholder='{"title": "Fix login", "labels": [{"name": "bug"}]}'
            value={sample.pasted}
            invalid={sample.parsed?.ok === false}
            onChange={(e) => sample.setPasted(e.target.value)}
          />
          {sample.parsed?.ok === false && (
            <p role="alert" className="m-0 text-[11px] text-bad">
              Not valid JSON: {sample.parsed.error}
            </p>
          )}
        </>
      )}
      {noSamples && !sample.loading && (
        <p className="m-0 text-[11px] text-muted">
          No sample yet: paste JSON or let the flow run once.
          {sample.error != null &&
            " (Save the flow to see samples of a new node.)"}
        </p>
      )}
      {sample.input !== undefined && (
        <details>
          <summary className="cursor-pointer select-none text-[11px] font-medium text-muted">
            Show the input
          </summary>
          <div className="mt-1.5">
            <SensorItemsTable items={items} maxHeight="14rem" />
          </div>
        </details>
      )}
    </section>
  );
}

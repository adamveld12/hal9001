// Inspector body of a transform node: sample, step cards and live preview.

import { useMemo } from "react";
import { usePreviewTransform } from "../../api/transform";
import { Field, TextInput } from "../../components/ui";
import { useDebounced } from "../../pages/files/useEditState";
import type { Diagnostic, FlowNode } from "../model";
import { diagnosticField } from "../validate";
import { groupStepDiagnostics } from "./diagnostics";
import { discoverFields } from "./fields";
import { transformPorts } from "./ports";
import { freshStepCount, previewableSteps, toCommandNode } from "./preview";
import { PreviewPanel } from "./PreviewPanel";
import { SamplePanel } from "./SamplePanel";
import { fieldsBeforeEachStep } from "./shape";
import { StepList } from "./StepList";
import { summarizeStep } from "./steps";
import type { TransformNode, TransformStep } from "./types";
import { useDelayedFlag, useSampleInput } from "./useSampleInput";

const DEBOUNCE_MS = 300;
const EMPTY: TransformStep[] = [];

export function TransformForm({
  node,
  flowId,
  diags,
  onChange,
}: {
  node: TransformNode;
  flowId: string;
  /** Diagnostics of this node: client checks and the server's flow validation. */
  diags: Diagnostic[];
  onChange: (patch: (n: FlowNode) => FlowNode, coalesce?: string) => void;
}) {
  const steps = node.steps ?? EMPTY;
  const sample = useSampleInput(flowId, node.id);

  const inputFields = useMemo(
    () => (sample.input === undefined ? [] : discoverFields(sample.input)),
    [sample.input],
  );
  const fieldLists = useMemo(
    () => fieldsBeforeEachStep(inputFields, steps),
    [inputFields, steps],
  );

  const previewable = useMemo(() => previewableSteps(steps), [steps]);
  const request = useMemo(
    () =>
      sample.input !== undefined && previewable.steps.length > 0
        ? { steps: previewable.steps, input: sample.input }
        : null,
    [sample.input, previewable.steps],
  );
  const debounced = useDebounced(request, DEBOUNCE_MS);
  const query = usePreviewTransform(debounced);
  const answer = request === null ? undefined : query.data;

  const fresh = freshStepCount(answer?.request, steps, sample.input);
  const answeredAll =
    answer !== undefined &&
    fresh === answer.request.steps.length &&
    fresh === previewable.steps.length;
  const updating = useDelayedFlag(
    request !== null && (request !== debounced || query.isFetching),
    250,
  );

  const grouped = useMemo(() => {
    const previewDiags = (answer?.preview.diagnostics ?? []).filter((d) => {
      const m = /^steps\[(\d+)\]/.exec(d.path ?? "");
      return m === null || Number(m[1]) < fresh;
    });
    return groupStepDiagnostics([...diags, ...previewDiags]);
  }, [diags, answer, fresh]);

  const results = steps.map((_, i) =>
    i < fresh ? answer?.preview.step_results[i] : undefined,
  );
  const describe = (i: number) =>
    (i < fresh ? answer?.preview.description[i] : undefined) ||
    summarizeStep(steps[i]);

  const stepsMsgs = diags.filter((d) => diagnosticField(d) === "steps");
  const timeoutMsgs = diags.filter((d) => diagnosticField(d) === "timeout");

  return (
    <div className="space-y-4">
      <SamplePanel sample={sample} />

      <section className="space-y-2" aria-label="Steps">
        <h3 className="m-0 text-xs font-semibold">Steps</h3>
        {stepsMsgs.map((d) => (
          <p key={d.message} className="m-0 text-[11px] text-bad">
            {d.message}
          </p>
        ))}
        <StepList
          steps={steps}
          fieldLists={fieldLists}
          results={results}
          describe={describe}
          messages={grouped.byStep}
          onChange={(next, key) =>
            onChange(
              (n) => (n.type === "transform" ? { ...n, steps: next } : n),
              key,
            )
          }
        />
      </section>

      <PreviewPanel
        steps={steps}
        hasInput={sample.input !== undefined}
        answer={answer}
        stale={!answeredAll}
        updating={updating}
        error={query.error}
        blockedAt={previewable.blockedAt}
        ports={transformPorts(steps)}
        onConvert={(jq) =>
          onChange((n) => (n.type === "transform" ? toCommandNode(n, jq) : n))
        }
      />

      <Field
        label="Timeout"
        hint="Default 60s"
        error={timeoutMsgs.map((d) => d.message)}
      >
        <TextInput
          value={node.timeout ?? ""}
          placeholder="60s"
          onChange={(e) =>
            onChange((n) => {
              if (n.type !== "transform") return n;
              const next = { ...n };
              if (e.target.value === "") delete next.timeout;
              else next.timeout = e.target.value;
              return next;
            }, "timeout")
          }
        />
      </Field>
    </div>
  );
}

import { useState } from "react";
import { useAssistSession, type AssistDraft } from "../../api/assist";
import { CodeEditor } from "../../components/CodeEditor";
import { Badge, Button } from "../../components/ui";
import { DiagnosticsList } from "../files/DiagnosticsList";
import { describeTestFailure } from "./flowTest";
import { TestResultView } from "./TestResultView";
import { useTestDraftSensor } from "./useTestDraft";

type DraftSensor = AssistDraft["sensors"][number];

function SensorDraftCard({
  sessionId,
  sensor,
}: {
  sessionId: string;
  sensor: DraftSensor;
}) {
  const test = useTestDraftSensor(sessionId, sensor.id);
  const [open, setOpen] = useState(false);
  const isNew = sensor.base_hash === null;
  const errors = sensor.diagnostics.filter((d) => d.severity === "error");
  const warnings = sensor.diagnostics.filter((d) => d.severity === "warning");
  const failure = test.error ? describeTestFailure(test.error) : null;

  return (
    <details
      aria-label={`Draft sensor ${sensor.id}`}
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
      className="rounded-md border border-line bg-panel text-xs"
    >
      <summary className="flex cursor-pointer select-none flex-wrap items-center gap-2 px-2 py-1.5">
        <span className="font-medium">Sensor</span>
        <span className="font-mono">{sensor.id}</span>
        <Badge tone={isNew ? "ok" : "info"}>{isNew ? "new" : "edit"}</Badge>
        {errors.length > 0 && (
          <Badge tone="bad">
            {errors.length} error{errors.length === 1 ? "" : "s"}
          </Badge>
        )}
        {warnings.length > 0 && (
          <Badge tone="warn">
            {warnings.length} warning{warnings.length === 1 ? "" : "s"}
          </Badge>
        )}
        {test.isPending && <span className="text-muted">running…</span>}
      </summary>
      <div className="space-y-2 border-t border-line p-2">
        {open && (
          <CodeEditor
            lang="yaml"
            label={`Draft sensor ${sensor.id} YAML`}
            value={sensor.yaml}
            readOnly
            minHeight="3rem"
            maxHeight="14rem"
          />
        )}
        <DiagnosticsList
          diagnostics={sensor.diagnostics}
          source={sensor.yaml}
        />
        <div className="flex items-center gap-2">
          <Button
            icon="play"
            className="shrink-0 whitespace-nowrap"
            disabled={test.isPending}
            onClick={test.run}
          >
            {test.isPending ? "Testing…" : "Test sensor"}
          </Button>
          <span className="text-[11px] text-muted">
            Runs the draft like Poll now. Nothing is recorded; Save writes the
            sensor.
          </span>
        </div>
        {failure && (
          <div role="alert" className="space-y-1 text-bad">
            <div>{failure.message}</div>
            {failure.diagnostics.length > 0 && (
              <ul className="m-0 list-disc space-y-0.5 pl-4">
                {failure.diagnostics.map((d) => (
                  <li
                    key={`${d.severity}|${d.path ?? ""}|${d.message}`}
                    className={d.severity === "error" ? undefined : "text-warn"}
                  >
                    {d.line ? `line ${d.line}: ` : ""}
                    {d.message}
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
        {test.result && (
          <details open className="rounded-md border border-line">
            <summary className="cursor-pointer select-none px-2 py-1.5 font-medium">
              Last test
            </summary>
            <div className="border-t border-line p-2">
              <TestResultView result={test.result} />
            </div>
          </details>
        )}
      </div>
    </details>
  );
}

/**
 * The sensors the agent drafted in a flow session, one collapsible card each
 * after the conversation: new or edit, its findings, the YAML, and "Test
 * sensor", whose result stays on the card. Save writes them before the flow.
 */
export function SensorDraftCards({ sessionId }: { sessionId: string }) {
  const sensors = useAssistSession(sessionId).data?.draft.sensors ?? [];
  return (
    <>
      {sensors.map((s) => (
        <SensorDraftCard key={s.id} sessionId={sessionId} sensor={s} />
      ))}
    </>
  );
}

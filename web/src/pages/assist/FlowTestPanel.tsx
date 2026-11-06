import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useAssistSamples, useAssistSession } from "../../api/assist";
import { errorMessage } from "../../api/client";
import { flowDraftParseQuery, useRecentEvents } from "../../api/flows";
import { Button, Field, Select, TextArea } from "../../components/ui";
import { relativeTime } from "../../lib/format";
import { FlowTestResultView } from "./FlowTestResultView";
import {
  buildFlowTestRequest,
  describeTestFailure,
  INITIAL_TEST_INPUT,
  type TestInputState,
  type TestSource,
} from "./flowTest";
import { useTestDraft } from "./useTestDraft";

const PREVIEW_CHARS = 70;

function preview(payload: unknown): string {
  const s = JSON.stringify(payload) ?? "";
  return s.length > PREVIEW_CHARS ? `${s.slice(0, PREVIEW_CHARS)}…` : s;
}

/**
 * "Test flow" in the drawer: pick an input (a sample of the session, a recent
 * event of a subscribed sensor, or pasted JSON), run the draft once, and read
 * what every node did. The canvas shows the same run as an overlay.
 */
export function FlowTestPanel({ sessionId }: { sessionId: string }) {
  const test = useTestDraft(sessionId);
  const [input, setInput] = useState<TestInputState>(INITIAL_TEST_INPUT);
  const [sensorChoice, setSensor] = useState("");
  const set = (patch: Partial<TestInputState>) =>
    setInput((s) => ({ ...s, ...patch }));

  const samples = useAssistSamples(sessionId, input.source === "sample");
  const draft = useAssistSession(sessionId).data?.draft;
  const draftFlow = useQuery({
    ...flowDraftParseQuery(draft?.hash ?? "", draft?.yaml ?? ""),
    enabled: input.source === "event" && draft !== undefined,
  });
  const subscribed = draftFlow.data?.flow?.subscribe ?? [];
  const sensor = sensorChoice !== "" ? sensorChoice : (subscribed[0] ?? "");
  const events = useRecentEvents(
    sensor === "" ? undefined : sensor,
    input.source === "event" && sensor !== "",
  );

  // Without a choice the first sample is used, as the daemon does.
  const firstSample = samples.data?.[0]?.index;
  const effective: TestInputState =
    input.source === "sample" &&
    input.sampleIndex === "" &&
    firstSample !== undefined
      ? { ...input, sampleIndex: String(firstSample) }
      : input;
  const built = buildFlowTestRequest(effective);
  const failure = test.error ? describeTestFailure(test.error) : null;
  const result = test.result?.kind === "flow" ? test.result : undefined;

  return (
    <section
      aria-label="Test flow"
      className="max-h-[55vh] space-y-2 overflow-auto border-t border-line px-3 py-2"
    >
      <div className="flex items-center gap-2">
        <Button
          icon="play"
          className="shrink-0 whitespace-nowrap"
          disabled={!built.ok || test.isPending}
          onClick={() => built.ok && void test.run(built.body)}
        >
          {test.isPending ? "Testing…" : "Test flow"}
        </Button>
        <span className="text-[11px] text-muted">
          Runs the draft once on one input. Agent nodes are not run; inbox nodes
          show what they would create.
        </span>
      </div>

      <Field label="Input">
        <Select
          value={input.source}
          onChange={(e) => set({ source: e.target.value as TestSource })}
        >
          <option value="sample">Session sample</option>
          <option value="event">Recent event</option>
          <option value="json">Pasted JSON</option>
        </Select>
      </Field>

      {input.source === "sample" && (
        <Field
          label="Sample"
          error={samples.isError ? [errorMessage(samples.error)] : undefined}
        >
          <Select
            value={effective.sampleIndex}
            onChange={(e) => set({ sampleIndex: e.target.value })}
          >
            {samples.data?.length === 0 && (
              <option value="">(no samples yet)</option>
            )}
            {samples.isPending && <option value="">Loading…</option>}
            {(samples.data ?? []).map((s) => (
              <option key={s.index} value={s.index}>
                {s.sensor_id} · {preview(s.payload)}
              </option>
            ))}
          </Select>
        </Field>
      )}

      {input.source === "event" && (
        <>
          <Field label="Sensor">
            <Select
              value={sensor}
              disabled={subscribed.length === 0}
              onChange={(e) => setSensor(e.target.value)}
            >
              {subscribed.length === 0 && (
                <option value="">(the draft subscribes to no sensor)</option>
              )}
              {subscribed.map((s) => (
                <option key={s}>{s}</option>
              ))}
            </Select>
          </Field>
          <Field
            label="Event"
            error={events.isError ? [errorMessage(events.error)] : undefined}
          >
            <Select
              value={input.eventId}
              onChange={(e) => set({ eventId: e.target.value })}
            >
              <option value="">
                {events.isPending && sensor !== ""
                  ? "Loading…"
                  : "(choose an event)"}
              </option>
              {(events.data ?? []).map((ev) => (
                <option key={ev.id} value={ev.id}>
                  {relativeTime(ev.created_at)} ·{" "}
                  {ev.payload_preview.slice(0, PREVIEW_CHARS)}
                </option>
              ))}
            </Select>
          </Field>
          {events.data?.length === 0 && (
            <p className="m-0 text-[11px] text-muted">
              No events for this sensor yet.
            </p>
          )}
        </>
      )}

      {input.source === "json" && (
        <TextArea
          rows={5}
          aria-label="Input JSON"
          value={input.json}
          spellCheck={false}
          onChange={(e) => set({ json: e.target.value })}
        />
      )}

      {!built.ok && <p className="m-0 text-[11px] text-muted">{built.error}</p>}

      {failure && (
        <div role="alert" className="space-y-1 text-xs text-bad">
          <div>{failure.message}</div>
          {failure.diagnostics.length > 0 && (
            <ul className="m-0 list-disc space-y-0.5 pl-4">
              {failure.diagnostics.map((d) => (
                <li
                  key={`${d.severity}|${d.node_id ?? ""}|${d.path ?? ""}|${d.message}`}
                  className={d.severity === "error" ? undefined : "text-warn"}
                >
                  {d.node_id ? `${d.node_id}: ` : ""}
                  {d.message}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {result && (
        <details open className="rounded-md border border-line">
          <summary className="cursor-pointer select-none px-2 py-1.5 text-xs font-medium">
            Last test
          </summary>
          <div className="border-t border-line p-2">
            <FlowTestResultView result={result} />
          </div>
        </details>
      )}
    </section>
  );
}

import { useState } from "react";
import { useSearchParams } from "react-router";
import { usePromptRender } from "../../api/prompts";
import { useEvents } from "../../api/events";
import { CodeEditor } from "../../components/CodeEditor";
import { CopyButton } from "../../components/CopyButton";
import { Markdown } from "../../components/Markdown";
import { Tabs } from "../../components/Tabs";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  ErrorState,
  Field,
  LoadingState,
  Spinner,
  TextArea,
  TextInput,
} from "../../components/ui";
import { cx } from "../../lib/cx";
import { shortId } from "../../lib/format";
import { DiagnosticsList } from "../files/DiagnosticsList";
import { useDebounced } from "../files/useEditState";
import { planRender } from "./preview";

type SampleKind = "event" | "json";
type ResultView = "rendered" | "raw";

const SAMPLE_TABS = [
  { key: "event", label: "Stored event" },
  { key: "json", label: "Pasted JSON" },
] as const;

const RESULT_TABS = [
  { key: "rendered", label: "Rendered" },
  { key: "raw", label: "Raw text" },
] as const;

/** Search and pick one stored event. */
function EventPicker({
  selected,
  onSelect,
}: {
  selected: string | null;
  onSelect: (id: string | null) => void;
}) {
  const [search, setSearch] = useState("");
  const q = useDebounced(search.trim(), 300);
  const events = useEvents(q === "" ? {} : { q });
  const items = events.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <div className="space-y-2">
      <TextInput
        type="search"
        aria-label="Search event payloads"
        placeholder="Search event payloads"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
      />
      {selected && (
        <div className="flex items-center gap-2 text-xs">
          <span className="text-muted">Selected</span>
          <span className="font-mono">{shortId(selected)}</span>
          <Button variant="ghost" onClick={() => onSelect(null)}>
            Clear
          </Button>
        </div>
      )}
      {events.isPending && <LoadingState label="Loading events" />}
      {events.isError && (
        <ErrorState
          error={events.error}
          onRetry={() => void events.refetch()}
        />
      )}
      {events.isSuccess && items.length === 0 && (
        <div className="rounded-md border border-line p-4 text-center text-xs text-muted">
          {q === ""
            ? "No events yet. Paste JSON instead, or wait for a sensor to fire."
            : "No events match this search."}
        </div>
      )}
      {items.length > 0 && (
        <ul
          aria-label="Events"
          className="max-h-72 divide-y divide-line overflow-auto rounded-md border border-line"
        >
          {items.map((ev) => (
            <li key={ev.id}>
              <button
                type="button"
                aria-pressed={ev.id === selected}
                onClick={() => onSelect(ev.id)}
                className={cx(
                  "block w-full space-y-0.5 px-2 py-1.5 text-left text-xs hover:bg-raised",
                  ev.id === selected && "bg-raised",
                )}
              >
                <span className="flex items-center gap-2">
                  <Badge>{ev.sensor_id}</Badge>
                  <Time ts={ev.created_at} />
                  {ev.injected && <Badge tone="info">injected</Badge>}
                  <span className="ml-auto font-mono text-[11px] text-muted">
                    {shortId(ev.id)}
                  </span>
                </span>
                <span className="block truncate font-mono text-[11px] text-muted">
                  {ev.payload_preview}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {events.hasNextPage && (
        <Button
          onClick={() => void events.fetchNextPage()}
          disabled={events.isFetchingNextPage}
        >
          {events.isFetchingNextPage ? "Loading…" : "Load older events"}
        </Button>
      )}
    </div>
  );
}

/** The prompt text (saved or not) rendered against a stored event or pasted JSON. */
export function PromptPreview({ markdown }: { markdown: string }) {
  const [params, setParams] = useSearchParams();
  const kind: SampleKind = params.get("sample") === "json" ? "json" : "event";
  const eventId = params.get("event");
  const [jsonText, setJsonText] = useState("");
  const [extra, setExtra] = useState("");
  const [view, setView] = useState<ResultView>("rendered");

  const setParam = (key: string, value: string | null) =>
    setParams(
      (p) => {
        const next = new URLSearchParams(p);
        if (value === null) next.delete(key);
        else next.set(key, value);
        return next;
      },
      { replace: true },
    );

  const debouncedMarkdown = useDebounced(markdown, 400);
  const debouncedJson = useDebounced(jsonText, 400);
  const debouncedExtra = useDebounced(extra, 400);
  const plan = planRender(
    debouncedMarkdown,
    kind === "event" ? { kind, eventId } : { kind, text: debouncedJson },
    debouncedExtra,
  );
  const render = usePromptRender(plan.ok ? plan.request : null);

  let result;
  if (!plan.ok) {
    result = (
      <div
        role={plan.reason === "invalid-json" ? "alert" : undefined}
        className={cx(
          "rounded-md border border-line p-4 text-xs",
          plan.reason === "invalid-json" ? "text-bad" : "text-muted",
        )}
      >
        {plan.message}
      </div>
    );
  } else if (render.isError) {
    result = (
      <ErrorState error={render.error} onRetry={() => void render.refetch()} />
    );
  } else if (render.isPending) {
    result = <LoadingState label="Rendering" />;
  } else {
    const { rendered, diagnostics } = render.data;
    result = (
      <div className="space-y-3">
        {diagnostics.length > 0 && (
          <DiagnosticsList diagnostics={diagnostics} />
        )}
        {rendered !== null ? (
          <div className="rounded-md border border-line bg-panel">
            <div className="flex items-center">
              <div className="flex-1">
                <Tabs tabs={RESULT_TABS} active={view} onChange={setView} />
              </div>
              <div className="px-2">
                <CopyButton text={rendered} />
              </div>
            </div>
            <div className="max-h-[32rem] overflow-auto p-3">
              {view === "rendered" ? (
                <Markdown>{rendered}</Markdown>
              ) : (
                <pre className="whitespace-pre-wrap break-words font-mono text-[12px]">
                  {rendered}
                </pre>
              )}
            </div>
          </div>
        ) : (
          diagnostics.length === 0 && (
            <div className="text-xs text-muted">Nothing was rendered.</div>
          )
        )}
      </div>
    );
  }

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
      <div className="space-y-3">
        <div className="-mx-3">
          <Tabs
            tabs={SAMPLE_TABS}
            active={kind}
            onChange={(k) => setParam("sample", k === "json" ? "json" : null)}
          />
        </div>
        {kind === "event" ? (
          <EventPicker
            selected={eventId}
            onSelect={(id) => setParam("event", id)}
          />
        ) : (
          <div className="space-y-1">
            <div className="text-[11px] font-medium text-muted">
              Sensor item (bound to <code>.input</code>)
            </div>
            <CodeEditor
              lang="json"
              label="Sample JSON"
              value={jsonText}
              onChange={setJsonText}
              minHeight="10rem"
              maxHeight="20rem"
            />
          </div>
        )}
        <Field
          label="Extra prompt"
          hint="Appended like an agent node's extra prompt. Optional."
        >
          <TextArea
            rows={2}
            value={extra}
            onChange={(e) => setExtra(e.target.value)}
          />
        </Field>
      </div>
      <div className="min-w-0 space-y-2">
        <div className="flex h-5 items-center gap-2 text-xs text-muted">
          Preview
          {plan.ok && render.isFetching && !render.isPending && (
            <Spinner label="Rendering" />
          )}
        </div>
        {result}
      </div>
    </div>
  );
}

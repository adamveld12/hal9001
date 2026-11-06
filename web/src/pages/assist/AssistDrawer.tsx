import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import {
  useAssistSession,
  useAssistSessions,
  useCancelAssistTurn,
  useCreateAssistSession,
  useEndAssistSession,
  useSendAssistMessage,
  type AssistMessage,
  type AssistSession,
  type AssistStatus,
} from "../../api/assist";
import { ApiError } from "../../api/client";
import { useRuntimes } from "../../api/settings";
import { Icon } from "../../components/icons";
import {
  Badge,
  Button,
  LoadingState,
  Select,
  TextInput,
} from "../../components/ui";
import { cx } from "../../lib/cx";
import { relativeTime } from "../../lib/format";
import type { Tone } from "../../lib/status";
import { ConfirmButton } from "../files/ConfirmButton";
import { Composer } from "./Composer";
import { useAssistPage } from "./context";
import { MessageView } from "./MessageView";
import { traceLinks } from "./messages";
import {
  clampDrawerWidth,
  isBusy,
  parseStoredWidth,
  selectResumable,
  starterPrompts,
} from "./sessions";
import { TestResultView } from "./TestResultView";
import { useAssistLive } from "./useAssistLive";
import { useTestDraft } from "./useTestDraft";

const WIDTH_STORAGE_ITEM = "hal9001.assist.drawerWidth";

const STATUS: Record<AssistStatus, { label: string; tone: Tone }> = {
  idle: { label: "idle", tone: "muted" },
  running: { label: "running", tone: "run" },
  awaiting_approval: { label: "awaiting approval", tone: "warn" },
  failed: { label: "failed", tone: "bad" },
  closed: { label: "ended", tone: "muted" },
};

function StatusPill({ status }: { status: AssistStatus }) {
  const s = STATUS[status];
  return (
    <Badge tone={s.tone} className="gap-1.5">
      {status === "running" && (
        <span className="size-2 animate-spin rounded-full border border-run/40 border-t-run" />
      )}
      {s.label}
    </Badge>
  );
}

function storedWidth(): number {
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(WIDTH_STORAGE_ITEM);
  } catch {
    // storage unavailable: use the default
  }
  return clampDrawerWidth(parseStoredWidth(raw), window.innerWidth);
}

function saveWidth(w: number) {
  try {
    localStorage.setItem(WIDTH_STORAGE_ITEM, String(w));
  } catch {
    // not persisted
  }
}

/** Drag handle on the drawer's left edge; arrow keys resize too. */
function Resizer({
  width,
  onChange,
}: {
  width: number;
  onChange: (w: number) => void;
}) {
  const dragging = useRef(false);
  const widthAt = (clientX: number) =>
    clampDrawerWidth(window.innerWidth - clientX, window.innerWidth);
  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize assistant"
      aria-valuenow={width}
      tabIndex={0}
      className="absolute inset-y-0 -left-1 z-10 hidden w-2 cursor-col-resize hover:bg-accent/30 focus-visible:bg-accent/30 lg:block"
      onPointerDown={(e) => {
        dragging.current = true;
        e.currentTarget.setPointerCapture(e.pointerId);
      }}
      onPointerMove={(e) => {
        if (dragging.current) onChange(widthAt(e.clientX));
      }}
      onPointerUp={(e) => {
        dragging.current = false;
        e.currentTarget.releasePointerCapture(e.pointerId);
        saveWidth(widthAt(e.clientX));
      }}
      onKeyDown={(e) => {
        const step =
          e.key === "ArrowLeft" ? 24 : e.key === "ArrowRight" ? -24 : 0;
        if (step === 0) return;
        e.preventDefault();
        const next = clampDrawerWidth(width + step, window.innerWidth);
        onChange(next);
        saveWidth(next);
      }}
    />
  );
}

function Header({
  title,
  badge,
  children,
  actions,
}: {
  title: ReactNode;
  badge?: ReactNode;
  children?: ReactNode;
  actions?: ReactNode;
}) {
  const { close } = useAssistPage();
  return (
    <header className="space-y-2 border-b border-line px-3 py-2">
      <div className="flex items-center gap-2">
        <Icon name="agent" size={15} className="shrink-0 text-accent" />
        <h2 className="m-0 min-w-0 flex-1 truncate text-[13px] font-semibold">
          {title}
        </h2>
        {badge}
        <button
          type="button"
          aria-label="Close assistant"
          title="Close (Esc). The session keeps running."
          className="rounded-md p-1 text-muted hover:bg-raised hover:text-fg"
          onClick={close}
        >
          <Icon name="close" size={15} />
        </button>
      </div>
      {children}
      {actions && (
        <div className="flex flex-wrap items-center gap-2">{actions}</div>
      )}
    </header>
  );
}

function RuntimeControls({
  runtime,
  model,
  locked,
  onRuntime,
  onModel,
}: {
  runtime: string;
  model: string;
  locked: boolean;
  onRuntime?: (r: string) => void;
  onModel?: (m: string) => void;
}) {
  const runtimes = useRuntimes();
  const names = runtimes.data?.map((r) => r.name) ?? [];
  const options =
    runtime !== "" && !names.includes(runtime) ? [runtime, ...names] : names;
  return (
    <div className="flex gap-2">
      <Select
        aria-label="Runtime"
        className="w-36"
        value={runtime}
        disabled={locked || options.length === 0}
        title={
          locked
            ? "The runtime is fixed once the conversation has started."
            : undefined
        }
        onChange={(e) => onRuntime?.(e.target.value)}
      >
        {options.length === 0 && <option value="">no runtimes</option>}
        {options.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </Select>
      <TextInput
        aria-label="Model"
        className="min-w-0 flex-1"
        placeholder="model (optional)"
        value={model}
        disabled={locked}
        onChange={(e) => onModel?.(e.target.value)}
      />
    </div>
  );
}

function StarterPrompts({
  targetId,
  onPick,
}: {
  targetId: string | null;
  onPick: (text: string) => void;
}) {
  return (
    <div className="space-y-1.5">
      <div className="text-[11px] font-medium text-muted">Try asking</div>
      <div className="flex flex-wrap gap-1.5">
        {starterPrompts(targetId).map((p) => (
          <button
            key={p}
            type="button"
            className="rounded-full border border-line bg-panel px-2.5 py-1 text-xs hover:bg-raised"
            onClick={() => onPick(p)}
          >
            {p}
          </button>
        ))}
      </div>
    </div>
  );
}

/** Before a session exists: pick runtime and model, resume an earlier session, or send the first message. */
function StartView() {
  const page = useAssistPage();
  const { sensorId } = page;
  const runtimes = useRuntimes();
  const sessions = useAssistSessions(sensorId);
  const create = useCreateAssistSession();
  const send = useSendAssistMessage();
  const [runtimeChoice, setRuntime] = useState<string | null>(null);
  const [model, setModel] = useState("");
  const [prefill, setPrefill] = useState<{ text: string; n: number } | null>(
    null,
  );
  const runtime = runtimeChoice ?? runtimes.data?.[0]?.name ?? "";
  const resumable = selectResumable(sessions.data ?? [], sensorId);

  const start = async (text: string) => {
    const session = await create.mutateAsync({
      target:
        sensorId === null
          ? { kind: "sensor" }
          : { kind: "sensor", id: sensorId },
      runtime,
      model: model.trim() === "" ? undefined : model.trim(),
      yaml: page.editor()?.getText(),
    });
    page.bind(session.id);
    await send.mutateAsync({ id: session.id, text });
  };

  return (
    <>
      <Header
        title={
          sensorId === null
            ? "Create a sensor with AI"
            : `Ask AI about ${sensorId}`
        }
      >
        <RuntimeControls
          runtime={runtime}
          model={model}
          locked={false}
          onRuntime={setRuntime}
          onModel={setModel}
        />
      </Header>
      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-auto p-3">
        <p className="m-0 text-xs text-muted">
          The agent edits a draft of the sensor, which appears in the editor.
          Nothing is saved until you press Save. It never sees your secrets; it
          tests the draft through the daemon.
        </p>
        {runtimes.isSuccess && runtimes.data.length === 0 && (
          <div role="alert" className="text-xs text-bad">
            No ACP runtimes are configured. Add one under <code>runtimes</code>{" "}
            in config.yaml.
          </div>
        )}
        {resumable.length > 0 && (
          <div className="space-y-1.5">
            <div className="text-[11px] font-medium text-muted">
              Resume a session
            </div>
            <ul className="m-0 list-none space-y-1 p-0">
              {resumable.map((s) => (
                <li key={s.id}>
                  <ResumeRow session={s} onResume={() => page.bind(s.id)} />
                </li>
              ))}
            </ul>
          </div>
        )}
        <StarterPrompts
          targetId={sensorId}
          onPick={(text) => setPrefill((p) => ({ text, n: (p?.n ?? 0) + 1 }))}
        />
      </div>
      <div className="border-t border-line p-3">
        <Composer
          disabled={runtime === ""}
          placeholder={
            sensorId === null
              ? "Describe the sensor you want…"
              : "What should change?"
          }
          prefill={prefill}
          onSend={start}
        />
      </div>
    </>
  );
}

function ResumeRow({
  session,
  onResume,
}: {
  session: AssistSession;
  onResume: () => void;
}) {
  return (
    <button
      type="button"
      className="flex w-full items-center gap-2 rounded-md border border-line bg-panel px-2.5 py-1.5 text-left text-xs hover:bg-raised"
      onClick={onResume}
    >
      <span className="font-medium">{session.runtime}</span>
      <StatusPill status={session.status} />
      <span className="ml-auto text-muted">
        {relativeTime(session.updated_at)}
      </span>
    </button>
  );
}

/**
 * Messages, scrolled to the end unless the user has scrolled up. The content
 * also grows without a new message (a pending approval loads its prompt after
 * the tool call appeared) and the box shrinks when the test panel opens, so
 * the end is followed by observing both sizes.
 */
function MessageList({
  messages,
  runtime,
}: {
  messages: readonly AssistMessage[];
  runtime: string;
}) {
  const box = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  useEffect(() => {
    const el = box.current;
    const inner = content.current;
    if (!el || !inner) return;
    const follow = () => {
      if (atBottom.current) el.scrollTop = el.scrollHeight;
    };
    const ro = new ResizeObserver(follow);
    ro.observe(inner);
    ro.observe(el); // the panel under the list can shrink it
    follow();
    return () => ro.disconnect();
  }, []);
  const traces = traceLinks(messages);
  return (
    <div
      ref={box}
      role="log"
      aria-label="Conversation"
      className="min-h-0 flex-1 overflow-auto p-3"
      onScroll={(e) => {
        const el = e.currentTarget;
        atBottom.current =
          el.scrollHeight - el.scrollTop - el.clientHeight < 40;
      }}
    >
      <div ref={content} className="space-y-3">
        {messages.map((m) => (
          <MessageView
            key={m.id}
            message={m}
            runtime={runtime}
            traceRunId={traces.get(m.id)}
          />
        ))}
      </div>
    </div>
  );
}

function TestPanel({ sessionId }: { sessionId: string }) {
  const test = useTestDraft(sessionId);
  return (
    <div className="space-y-2 border-t border-line px-3 py-2">
      <div className="flex items-center gap-2">
        <Button
          icon="play"
          className="shrink-0 whitespace-nowrap"
          disabled={test.isPending}
          onClick={() => void test.run()}
        >
          {test.isPending ? "Testing…" : "Test draft"}
        </Button>
        <span className="text-[11px] text-muted">
          Runs the draft like Poll now. No run, events or dedup keys are
          recorded.
        </span>
      </div>
      {test.error && (
        <div role="alert" className="text-xs text-bad">
          {test.error.message}
        </div>
      )}
      {test.result && (
        <details open className="rounded-md border border-line">
          <summary className="cursor-pointer select-none px-2 py-1.5 text-xs font-medium">
            Last test
          </summary>
          <div className="max-h-[45vh] overflow-auto border-t border-line p-2">
            <TestResultView result={test.result} />
          </div>
        </details>
      )}
    </div>
  );
}

function SessionView({ id }: { id: string }) {
  const page = useAssistPage();
  const q = useAssistSession(id);
  const send = useSendAssistMessage();
  const cancel = useCancelAssistTurn(id);
  const end = useEndAssistSession();
  const [prefill, setPrefill] = useState<{ text: string; n: number } | null>(
    null,
  );
  const s = q.data;

  if (q.isPending) {
    return (
      <>
        <Header title="AI assistant" />
        <LoadingState label="Loading session" />
      </>
    );
  }
  if (!s) {
    const gone = q.error instanceof ApiError && q.error.status === 404;
    return (
      <>
        <Header title="AI assistant" />
        <div className="space-y-3 p-3 text-xs">
          <div role="alert" className="text-bad">
            {gone ? "This session no longer exists." : q.error?.message}
          </div>
          <Button onClick={page.open}>Start a new session</Button>
        </div>
      </>
    );
  }

  const busy = isBusy(s.status);
  const closed = s.status === "closed";
  const otherTarget =
    s.target.id !== null && s.target.id !== page.sensorId ? s.target.id : null;

  return (
    <>
      <Header
        title={
          (s.target.id ?? page.sensorId)
            ? `Sensor ${s.target.id ?? page.sensorId}`
            : "New sensor"
        }
        badge={<StatusPill status={s.status} />}
        actions={
          <>
            {busy && (
              <Button
                variant="danger"
                icon="stop"
                disabled={cancel.isPending}
                onClick={() => cancel.mutate()}
              >
                Stop
              </Button>
            )}
            <ConfirmButton
              label="End session"
              confirmLabel="End and discard"
              disabled={end.isPending}
              onConfirm={() =>
                end.mutate(id, { onSuccess: () => page.close() })
              }
            />
          </>
        }
      >
        <RuntimeControls runtime={s.runtime} model={s.model ?? ""} locked />
      </Header>
      {otherTarget !== null && (
        <div
          role="status"
          className="border-b border-line bg-raised px-3 py-1.5 text-xs"
        >
          This session edits sensor{" "}
          <Link
            className="text-accent underline"
            to={`/sensors/${otherTarget}?assist=${id}`}
          >
            {otherTarget}
          </Link>
          , not the one open here, so the editor is not synced with it.
        </div>
      )}
      <MessageList messages={s.messages} runtime={s.runtime} />
      {s.messages.length === 0 && !closed && (
        <div className="px-3 pb-3">
          <StarterPrompts
            targetId={s.target.id}
            onPick={(text) => setPrefill((p) => ({ text, n: (p?.n ?? 0) + 1 }))}
          />
        </div>
      )}
      {s.status === "failed" && s.error && (
        <div
          role="alert"
          className="border-t border-bad/30 bg-bad/5 px-3 py-2 text-xs text-bad"
        >
          The last turn failed: {s.error}
        </div>
      )}
      {closed ? (
        <div className="space-y-2 border-t border-line p-3 text-xs text-muted">
          This session has ended.
          <div>
            <Button onClick={page.open}>Start a new session</Button>
          </div>
        </div>
      ) : (
        <>
          <TestPanel sessionId={id} />
          <div className="border-t border-line p-3">
            <Composer
              disabled={busy}
              placeholder={
                busy ? "Waiting for the agent…" : "Message the agent…"
              }
              prefill={prefill}
              onSend={async (text) => {
                await page.editor()?.flush();
                await send.mutateAsync({
                  id,
                  text,
                  yaml: page.editor()?.getText(),
                });
              }}
            />
          </div>
        </>
      )}
    </>
  );
}

/** The chat drawer: beside the editor on wide screens, over it on narrow ones. */
export function AssistDrawer() {
  const { sessionId, close } = useAssistPage();
  useAssistLive(sessionId);
  const [width, setWidth] = useState(storedWidth);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented) close();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [close]);

  useEffect(() => {
    const onResize = () =>
      setWidth((w) => clampDrawerWidth(w, window.innerWidth));
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  return (
    <>
      <div
        aria-hidden="true"
        className="fixed inset-0 z-20 bg-black/30 lg:hidden"
        onClick={close}
      />
      <aside
        aria-label="AI assistant"
        style={{ "--assist-w": `${width}px` } as React.CSSProperties}
        className={cx(
          "assist-drawer flex flex-col border-l border-line bg-bg",
          "fixed inset-y-0 right-0 z-30 w-full max-w-md shadow-xl",
          "lg:relative lg:z-auto lg:w-[var(--assist-w)] lg:max-w-none lg:shrink-0 lg:shadow-none",
        )}
      >
        <Resizer width={width} onChange={setWidth} />
        {sessionId === null ? <StartView /> : <SessionView id={sessionId} />}
      </aside>
    </>
  );
}

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
  type AssistTargetKind,
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
import { startSession } from "./startSession";
import { useAssistPage } from "./context";
import { FlowSavePanel } from "./FlowSavePanel";
import { FlowTestPanel } from "./FlowTestPanel";
import { SensorDraftCards } from "./SensorDraftCards";
import { MessageView } from "./MessageView";
import { traceLinks } from "./messages";
import {
  bindSession,
  clampDrawerWidth,
  isBusy,
  parseStoredWidth,
} from "./sessions";
import { selectResumable, starterPrompts, targetConfig } from "./targets";
import {
  pendingApprovalMessage,
  isBelowView,
  shouldFollow,
} from "./scrollFollow";
import { TestResultView } from "./TestResultView";
import { ToolResultView } from "../tools/ToolResultView";
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
  kind,
  targetId,
  onPick,
}: {
  kind: AssistTargetKind;
  targetId: string | null;
  onPick: (text: string) => void;
}) {
  return (
    <div className="space-y-1.5">
      <div className="text-[11px] font-medium text-muted">Try asking</div>
      <div className="flex flex-wrap gap-1.5">
        {starterPrompts(kind, targetId).map((p) => (
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
  const { kind, targetId } = page;
  const cfg = targetConfig(kind);
  const runtimes = useRuntimes();
  const sessions = useAssistSessions(kind, targetId);
  const create = useCreateAssistSession();
  const send = useSendAssistMessage();
  const end = useEndAssistSession();
  const [runtimeChoice, setRuntime] = useState<string | null>(null);
  const [model, setModel] = useState("");
  const [prefill, setPrefill] = useState<{ text: string; n: number } | null>(
    null,
  );
  const runtime = runtimeChoice ?? runtimes.data?.[0]?.name ?? "";
  const resumable = selectResumable(sessions.data ?? [], kind, targetId);

  const start = async (text: string) => {
    const yaml = await page.editor()?.getText();
    await startSession({
      create: () =>
        create.mutateAsync({
          target: targetId === null ? { kind } : { kind, id: targetId },
          runtime,
          model: model.trim() === "" ? undefined : model.trim(),
          yaml,
        }),
      send: (id) => send.mutateAsync({ id, text }),
      end: (id) => end.mutateAsync(id),
      bind: page.bind,
    });
  };

  return (
    <>
      <Header
        title={targetId === null ? cfg.newTitle : cfg.existingTitle(targetId)}
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
        <p className="m-0 text-xs text-muted">{cfg.intro}</p>
        {runtimes.isError && (
          <div role="alert" className="text-xs text-bad">
            Could not load the runtimes: {runtimes.error.message}
          </div>
        )}
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
          kind={kind}
          targetId={targetId}
          onPick={(text) => setPrefill((p) => ({ text, n: (p?.n ?? 0) + 1 }))}
        />
      </div>
      <div className="border-t border-line p-3">
        <Composer
          disabled={runtime === ""}
          placeholder={
            targetId === null
              ? cfg.composerPlaceholder.new
              : cfg.composerPlaceholder.existing
          }
          prefill={prefill}
          autoFocus
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
 * Messages, scrolled to the end while the user is near it. The content also
 * grows without a new message (a pending approval loads its prompt after the
 * tool call appeared, a test result arrives), so the end is followed by
 * observing both sizes. Approvals render inline; when one is out of view a
 * pill jumps to it. `tail` renders after the last message (draft sensor and
 * test cards).
 */
function MessageList({
  messages,
  runtime,
  tail,
}: {
  messages: readonly AssistMessage[];
  runtime: string;
  tail: ReactNode;
}) {
  const box = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const [pillVisible, setPillVisible] = useState(false);
  const pendingId = pendingApprovalMessage(messages)?.id ?? null;

  const updatePill = () => {
    const el = box.current;
    const target = el?.querySelector("[data-pending-approval]");
    setPillVisible(
      !!el &&
        !!target &&
        isBelowView(
          target.getBoundingClientRect().top,
          el.getBoundingClientRect().bottom,
        ),
    );
  };

  useEffect(() => {
    const el = box.current;
    const inner = content.current;
    if (!el || !inner) return;
    const follow = () => {
      if (atBottom.current) el.scrollTop = el.scrollHeight;
      updatePill();
    };
    const ro = new ResizeObserver(follow);
    ro.observe(inner);
    ro.observe(el);
    follow();
    return () => ro.disconnect();
  }, []);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(updatePill, [pendingId]);

  const traces = traceLinks(messages);
  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div
        ref={box}
        role="log"
        aria-label="Conversation"
        className="min-h-0 flex-1 overflow-auto p-3"
        onScroll={(e) => {
          const el = e.currentTarget;
          atBottom.current = shouldFollow(
            el.scrollHeight,
            el.scrollTop,
            el.clientHeight,
          );
          updatePill();
        }}
      >
        <div ref={content} className="space-y-3">
          {messages.map((m) => (
            <div
              key={m.id}
              data-pending-approval={m.id === pendingId ? "" : undefined}
            >
              <MessageView
                message={m}
                runtime={runtime}
                traceRunId={traces.get(m.id)}
              />
            </div>
          ))}
          {tail}
        </div>
      </div>
      {pillVisible && (
        <button
          type="button"
          className="absolute bottom-2 left-1/2 -translate-x-1/2 rounded-full border border-warn/50 bg-panel px-3 py-1 text-xs font-medium text-warn shadow-md hover:bg-raised"
          onClick={() =>
            box.current
              ?.querySelector("[data-pending-approval]")
              ?.scrollIntoView({ block: "center", behavior: "smooth" })
          }
        >
          Pending approval ↓
        </button>
      )}
    </div>
  );
}

/** "Test draft" as a card at the end of the conversation; the result opens under the button. */
function TestPanel({ sessionId }: { sessionId: string }) {
  const test = useTestDraft(sessionId);
  const isTool = useAssistPage().kind === "tool";
  const [argsText, setArgsText] = useState("{}");
  let args: Record<string, unknown> | undefined;
  try {
    const v: unknown = JSON.parse(argsText);
    if (typeof v === "object" && v !== null && !Array.isArray(v)) {
      args = v as Record<string, unknown>;
    }
  } catch {
    // reported below
  }
  return (
    <div className="space-y-2 rounded-md border border-line bg-panel p-2 text-xs">
      <div className="flex items-center gap-2">
        <Button
          icon="play"
          className="shrink-0 whitespace-nowrap"
          disabled={test.isPending || (isTool && args === undefined)}
          onClick={() => void test.run(isTool ? { args } : undefined)}
        >
          {test.isPending ? "Testing…" : "Test draft"}
        </Button>
        <span className="text-[11px] text-muted">
          {isTool
            ? "Calls the draft once with these arguments. Nothing is recorded."
            : "Runs the draft like Poll now. No run, events or dedup keys are recorded."}
        </span>
      </div>
      {isTool && (
        <TextInput
          aria-label="Test arguments (JSON)"
          className="font-mono"
          value={argsText}
          invalid={args === undefined}
          onChange={(e) => setArgsText(e.target.value)}
        />
      )}
      {test.error && (
        <div role="alert" className="text-bad">
          {test.error.message}
        </div>
      )}
      {test.result?.kind === "tool" && (
        <details open className="rounded-md border border-line">
          <summary className="cursor-pointer select-none px-2 py-1.5 font-medium">
            Last test
          </summary>
          <div className="border-t border-line p-2">
            <ToolResultView result={test.result} />
          </div>
        </details>
      )}
      {test.result?.kind === "sensor" && (
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
  const cfg = targetConfig(s.target.kind);
  const binding = bindSession(s, page.kind, page.targetId);
  const elsewhere =
    s.target.kind !== page.kind
      ? cfg.draftPath(id)
      : binding.otherTarget !== null
        ? cfg.editorPath(binding.otherTarget, id)
        : null;

  return (
    <>
      <Header
        title={cfg.sessionTitle(s.target.id ?? page.targetId)}
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
      {elsewhere !== null && (
        <div
          role="status"
          className="border-b border-line bg-raised px-3 py-1.5 text-xs"
        >
          This session edits{" "}
          {binding.otherTarget !== null
            ? `${cfg.noun} `
            : `a ${cfg.noun} elsewhere `}
          <Link className="text-accent underline" to={elsewhere}>
            {binding.otherTarget ?? "open it"}
          </Link>
          , not the one open here, so the editor is not synced with it.
        </div>
      )}
      <MessageList
        messages={s.messages}
        runtime={s.runtime}
        tail={
          closed ? null : s.target.kind === "flow" ? (
            <>
              <SensorDraftCards sessionId={id} />
              <FlowTestPanel sessionId={id} />
            </>
          ) : (
            <TestPanel sessionId={id} />
          )
        }
      />
      {s.messages.length === 0 && !closed && (
        <div className="px-3 pb-3">
          <StarterPrompts
            kind={s.target.kind}
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
          {s.target.kind === "flow" && (
            <div className="max-h-48 overflow-auto">
              <FlowSavePanel sessionId={id} />
            </div>
          )}
          <div className="border-t border-line p-3">
            <Composer
              disabled={busy}
              placeholder={
                busy ? "Waiting for the agent…" : "Message the agent…"
              }
              prefill={prefill}
              autoFocus
              onSend={async (text) => {
                await page.editor()?.flush();
                await send.mutateAsync({
                  id,
                  text,
                  yaml: await page.editor()?.getText(),
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

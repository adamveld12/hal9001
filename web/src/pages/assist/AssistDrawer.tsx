import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import {
  useAssistSession,
  useCancelAssistTurn,
  useCreateAssistSession,
  useEndAssistSession,
  useSendAssistMessage,
  type AssistMessage,
  type AssistTargetKind,
} from "../../api/assist";
import { ApiError } from "../../api/client";
import { useRuntimeOptions, useRuntimes } from "../../api/settings";
import { Icon } from "../../components/icons";
import { Button, LoadingState, Select, TextInput } from "../../components/ui";
import { cx } from "../../lib/cx";
import { noFocusOnClick } from "../../lib/focus";
import { runtimeOptions } from "../../lib/runtimeStatus";
import { ConfirmButton } from "../files/ConfirmButton";
import { Composer } from "./Composer";
import { useAssist } from "./context";
import { startSession } from "./startSession";
import { FlowSavePanel } from "./FlowSavePanel";
import { FlowTestPanel } from "./FlowTestPanel";
import { HistoryPanel } from "./HistoryPanel";
import { SensorDraftCards } from "./SensorDraftCards";
import { ConversationTailApprovals } from "../approvals/ConversationApprovals";
import { MessageView } from "./MessageView";
import { traceLinks } from "./messages";
import { clampDrawerWidth, isBusy, parseStoredWidth } from "./sessions";
import { StatusPill } from "./StatusPill";
import { starterPrompts, targetConfig, targetPath } from "./targets";
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

const RUNTIME_STORAGE_ITEM = "hal9001.assist.runtime";

/** The runtime of the last chat the user started, so a new chat does not reset it to the first one. */
function storedRuntime(): string | null {
  try {
    return localStorage.getItem(RUNTIME_STORAGE_ITEM);
  } catch {
    return null;
  }
}

function saveRuntime(name: string) {
  try {
    localStorage.setItem(RUNTIME_STORAGE_ITEM, name);
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

/** The drawer's own toolbar: always shown, whatever the view. */
function DrawerBar({
  historyOpen,
  onHistory,
}: {
  historyOpen: boolean;
  onHistory: () => void;
}) {
  const { close, newChat } = useAssist();
  return (
    <div className="flex items-center gap-1.5 border-b border-line px-3 py-2">
      <Icon name="agent" size={15} className="shrink-0 text-accent" />
      <span className="min-w-0 flex-1 truncate text-[13px] font-semibold">
        Ask AI
      </span>
      <Button
        icon="timeline"
        aria-pressed={historyOpen}
        title="Recent chats"
        onClick={onHistory}
      >
        History
      </Button>
      <Button icon="plus" title="Start a new chat (/new)" onClick={newChat}>
        New chat
      </Button>
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
  return (
    <header className="space-y-2 border-b border-line px-3 py-2">
      <div className="flex items-center gap-2">
        <h2 className="m-0 min-w-0 flex-1 truncate text-[13px] font-semibold">
          {title}
        </h2>
        {badge}
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
  const runtimes = useRuntimeOptions();
  const options = runtimeOptions(runtimes.data ?? [], runtime);
  return (
    <div className="flex gap-2">
      <Select
        aria-label="Runtime"
        className="w-48"
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
        {options.map((o) => (
          <option key={o.name} value={o.name}>
            {o.label}
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
}: {
  kind: AssistTargetKind;
  targetId: string | null;
}) {
  const { seedText } = useAssist();
  return (
    <div className="space-y-1.5">
      <div className="text-[11px] font-medium text-muted">Try asking</div>
      <div className="flex flex-wrap gap-1.5">
        {starterPrompts(kind, targetId).map((p) => (
          <button
            key={p}
            type="button"
            className="rounded-full border border-line bg-panel px-2.5 py-1 text-xs hover:bg-raised"
            onClick={() => seedText(p)}
          >
            {p}
          </button>
        ))}
      </div>
    </div>
  );
}

/** Before a session exists: pick runtime and model, or send the first message. */
function StartView() {
  const assist = useAssist();
  const { kind, id: targetId } = assist.target;
  const cfg = targetConfig(kind);
  const runtimes = useRuntimes();
  const create = useCreateAssistSession();
  const send = useSendAssistMessage();
  const end = useEndAssistSession();
  const [runtimeChoice, setRuntime] = useState<string | null>(null);
  const [model, setModel] = useState("");
  const names = runtimes.data?.map((r) => r.name) ?? [];
  const remembered = storedRuntime();
  const runtime =
    runtimeChoice ??
    (remembered !== null && names.includes(remembered)
      ? remembered
      : (names[0] ?? ""));

  const start = async (text: string) => {
    saveRuntime(runtime);
    const yaml = await assist.editor()?.getText();
    await startSession({
      create: () =>
        create.mutateAsync({
          target: targetId === null ? { kind } : { kind, id: targetId },
          runtime,
          model: model.trim() === "" ? undefined : model.trim(),
          yaml,
        }),
      send: (id) =>
        send.mutateAsync({ id, text, page_context: assist.pageContext() }),
      end: (id) => end.mutateAsync(id),
      bind: (id) => assist.bind(id),
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
        <StarterPrompts kind={kind} targetId={targetId} />
      </div>
      <div className="border-t border-line p-3">
        <Composer
          disabled={runtime === ""}
          placeholder={
            targetId === null
              ? cfg.composerPlaceholder.new
              : cfg.composerPlaceholder.existing
          }
          prefill={assist.seed}
          onPrefillApplied={assist.consumeSeed}
          autoFocus
          onSend={start}
          onNewChat={assist.newChat}
        />
      </div>
    </>
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
  sessionId,
  messages,
  tail,
}: {
  sessionId: string;
  messages: readonly AssistMessage[];
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
  const linkedIds = useMemo(
    () =>
      new Set(
        messages.flatMap((m) =>
          m.approval_id !== null ? [m.approval_id] : [],
        ),
      ),
    [messages],
  );
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
              <MessageView message={m} traceRunId={traces.get(m.id)} />
            </div>
          ))}
          <ConversationTailApprovals
            conversationId={sessionId}
            linkedIds={linkedIds}
          />
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
function TestPanel({
  sessionId,
  kind,
}: {
  sessionId: string;
  kind: AssistTargetKind;
}) {
  const test = useTestDraft(sessionId);
  const isTool = kind === "tool";
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
  const assist = useAssist();
  const q = useAssistSession(id);
  const send = useSendAssistMessage();
  const cancel = useCancelAssistTurn(id);
  const end = useEndAssistSession();
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
          <Button onClick={assist.newChat}>Start a new chat</Button>
        </div>
      </>
    );
  }

  const busy = isBusy(s.status);
  const closed = s.status === "closed";
  const kind = s.target.kind;
  const cfg = targetConfig(kind);
  // The saved id once a new target was saved; the session's own until then.
  const targetId = assist.target.kind === kind ? assist.target.id : s.target.id;
  const editorPath = targetPath(kind, targetId);
  const elsewhere = !closed && !assist.editorActive ? editorPath : null;

  return (
    <>
      <Header
        title={
          kind === "general"
            ? (s.title ?? "New chat")
            : cfg.sessionTitle(targetId)
        }
        badge={<StatusPill status={s.status} />}
        actions={
          <>
            {busy && (
              <Button
                variant="danger"
                icon="stop"
                disabled={cancel.isPending}
                {...noFocusOnClick}
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
                end.mutate(id, { onSuccess: () => assist.newChat() })
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
          {targetId === null ? `a new ${cfg.noun}` : `${cfg.noun} ${targetId}`}.{" "}
          <Link className="text-accent underline" to={elsewhere}>
            Open {targetId === null ? `the new ${cfg.noun} draft` : targetId}
          </Link>{" "}
          to see its draft in the editor.
        </div>
      )}
      <MessageList
        sessionId={id}
        messages={s.messages}
        tail={
          closed || kind === "general" ? null : kind === "flow" ? (
            <>
              <SensorDraftCards sessionId={id} />
              <FlowTestPanel sessionId={id} />
            </>
          ) : (
            <TestPanel sessionId={id} kind={kind} />
          )
        }
      />
      {s.messages.length === 0 && !closed && (
        <div className="px-3 pb-3">
          <StarterPrompts kind={kind} targetId={s.target.id} />
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
            <Button onClick={assist.newChat}>Start a new chat</Button>
          </div>
        </div>
      ) : (
        <>
          {kind === "flow" && (
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
              prefill={assist.seed}
              onPrefillApplied={assist.consumeSeed}
              autoFocus
              onNewChat={assist.newChat}
              onSend={async (text) => {
                await assist.editor()?.flush();
                await send.mutateAsync({
                  id,
                  text,
                  yaml: await assist.editor()?.getText(),
                  page_context: assist.pageContext(),
                });
              }}
            />
          </div>
        </>
      )}
    </>
  );
}

/**
 * The global chat drawer, docked at the right of the app shell: beside the page
 * on wide screens, over it on narrow ones. Mounted while open; the shell keeps
 * it across route changes.
 */
export function AssistDrawer() {
  const assist = useAssist();
  const { sessionId, target, close, notice, dismissNotice } = assist;
  useAssistLive(sessionId);
  const [width, setWidth] = useState(storedWidth);
  const [historyOpen, setHistoryOpen] = useState(false);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (historyOpen) setHistoryOpen(false);
      else close();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [close, historyOpen]);

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
        <DrawerBar
          historyOpen={historyOpen}
          onHistory={() => setHistoryOpen((o) => !o)}
        />
        {historyOpen && <HistoryPanel onPicked={() => setHistoryOpen(false)} />}
        {notice !== null && (
          <div
            role="alert"
            className="flex items-start gap-2 border-b border-bad/30 bg-bad/5 px-3 py-2 text-xs text-bad"
          >
            <span className="min-w-0 flex-1">{notice}</span>
            <button
              type="button"
              aria-label="Dismiss"
              className="shrink-0 hover:text-fg"
              onClick={dismissNotice}
            >
              <Icon name="close" size={13} />
            </button>
          </div>
        )}
        {sessionId === null ? (
          <StartView key={`${target.kind}:${target.id ?? ""}`} />
        ) : (
          <SessionView key={sessionId} id={sessionId} />
        )}
      </aside>
    </>
  );
}

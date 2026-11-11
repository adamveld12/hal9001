import { useEffect, useRef, useState } from "react";
import {
  useAssistSession,
  useCancelAssistTurn,
  useEndAssistSession,
} from "../../api/assist";
import { ApiError } from "../../api/client";
import { Icon } from "../../components/icons";
import { Button, LoadingState } from "../../components/ui";
import { cx } from "../../lib/cx";
import { noFocusOnClick } from "../../lib/focus";
import { ConfirmButton } from "../files/ConfirmButton";
import { ChatStart } from "./ChatStart";
import { ChatThread } from "./ChatThread";
import { Header, RuntimeControls } from "./ChatParts";
import { useAssist } from "./context";
import { HistoryPanel } from "./HistoryPanel";
import { clampDrawerWidth, isBusy, parseStoredWidth } from "./sessions";
import { StatusPill } from "./StatusPill";
import { targetConfig } from "./targets";
import { useAssistLive } from "./useAssistLive";

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
  const { close, newChat, openFullPage } = useAssist();
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
      <Button
        icon="external"
        aria-label="Open full page"
        title="Open this chat on the Chat page"
        onClick={openFullPage}
      >
        Full page
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

function SessionView({ id }: { id: string }) {
  const assist = useAssist();
  const q = useAssistSession(id);
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
  const kind = s.target.kind;
  const cfg = targetConfig(kind);
  // The saved id once a new target was saved; the session's own until then.
  const targetId = assist.target.kind === kind ? assist.target.id : s.target.id;

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
      <ChatThread session={s} placement="drawer" onNewChat={assist.newChat} />
    </>
  );
}

/**
 * The global chat drawer, docked at the right of the app shell: beside the page
 * on wide screens, over it on narrow ones. Mounted while open; the shell keeps
 * it across route changes (and does not mount it on the Chat page).
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
          <ChatStart
            key={`${target.kind}:${target.id ?? ""}`}
            target={target}
            onStarted={(id) => assist.bind(id)}
            onNewChat={assist.newChat}
          />
        ) : (
          <SessionView key={sessionId} id={sessionId} />
        )}
      </aside>
    </>
  );
}

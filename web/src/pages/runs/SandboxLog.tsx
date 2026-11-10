import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  sandboxLogUrl,
  SANDBOX_LOG_MAX,
  useAgentRunSandboxLog,
  useNodeRunSandboxLog,
  type SandboxScope,
} from "../../api/runs";
import { errorMessage } from "../../api/client";
import { Icon, type IconName } from "../../components/icons";
import { Badge, Button, ErrorState, Panel, Toggle } from "../../components/ui";
import { cx } from "../../lib/cx";
import {
  durationBetween,
  formatBytes,
  formatDuration,
  shortId,
} from "../../lib/format";
import { useNow } from "../approvals/timeLeft";
import {
  DENIAL_LABEL,
  denialKind,
  exitBadge,
  isLiveSession,
  outputOpen,
  provisioningMs,
  type SandboxLogData,
  type SandboxLogEntry,
  type SandboxSession,
} from "./sandboxEntries";

const KIND_ICON: Record<SandboxLogEntry["kind"], IconName> = {
  lifecycle: "play",
  exec: "command",
  fs: "note",
  denied: "stop",
  backend: "box",
};

const LEVEL_ROW: Record<SandboxLogEntry["level"], string> = {
  info: "",
  warn: "bg-warn/5",
  error: "bg-bad/10",
};

function clock(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return ts;
  const p = (n: number, w = 2) => String(n).padStart(w, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
}

function Output({
  label,
  text,
  open,
  truncated,
  tone,
}: {
  label: string;
  text: string;
  open: boolean;
  truncated: boolean;
  tone?: "bad";
}) {
  return (
    <details open={open} className="mt-1">
      <summary className="cursor-pointer select-none text-[11px] text-muted">
        {label} <span>({formatBytes(text.length)})</span>
        {truncated && <span className="ml-1 text-warn">truncated</span>}
      </summary>
      <pre
        className={cx(
          "m-0 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded border border-line bg-raised p-2 font-mono text-[11px]",
          tone === "bad" && "text-bad",
        )}
      >
        {text}
      </pre>
    </details>
  );
}

function EntryBody({ e }: { e: SandboxLogEntry }) {
  const badge = exitBadge(e);
  const truncated = e.truncated === true;
  const fields = e.fields ? Object.entries(e.fields) : [];
  return (
    <div className="min-w-0 flex-1">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        <span className={cx("break-words", e.level === "error" && "text-bad")}>
          {e.title}
        </span>
        {e.kind === "denied" && (
          <Badge tone="bad">{DENIAL_LABEL[denialKind(e)]}</Badge>
        )}
        {badge && <Badge tone={badge.tone}>{badge.label}</Badge>}
        {e.duration_ms !== undefined && (
          <span className="text-muted">{formatDuration(e.duration_ms)}</span>
        )}
        {e.kind === "fs" && e.bytes !== undefined && (
          <span className="text-muted">{formatBytes(e.bytes)}</span>
        )}
        {truncated && !e.stdout && !e.stderr && (
          <Badge tone="warn">truncated</Badge>
        )}
      </div>
      {e.kind === "fs" && (e.tool || e.path) && (
        <div className="font-mono text-[11px] text-muted">
          {e.tool} {e.path}
        </div>
      )}
      {e.command && (
        <pre className="m-0 whitespace-pre-wrap break-words font-mono text-[11px]">
          {e.cwd && <span className="text-muted">{e.cwd} </span>}
          <span className="text-muted">$ </span>
          {e.command}
        </pre>
      )}
      {e.kind !== "exec" && e.kind !== "fs" && e.path && (
        <div className="font-mono text-[11px] text-muted">{e.path}</div>
      )}
      {e.approval && <div className="text-[11px] text-muted">{e.approval}</div>}
      {fields.length > 0 && (
        <dl className="m-0 mt-0.5 flex flex-wrap gap-x-3 text-[11px] text-muted">
          {fields.map(([k, v]) => (
            <div key={k}>
              <dt className="inline">{k}: </dt>
              <dd className="m-0 inline font-mono">{v}</dd>
            </div>
          ))}
        </dl>
      )}
      {e.error && (
        <div className="whitespace-pre-wrap break-words text-[11px] text-bad">
          {e.error}
        </div>
      )}
      {e.stdout && (
        <Output
          label="stdout"
          text={e.stdout}
          open={outputOpen(e, "stdout")}
          truncated={truncated}
        />
      )}
      {e.stderr && (
        <Output
          label="stderr"
          text={e.stderr}
          open={outputOpen(e, "stderr")}
          truncated={truncated}
          tone={
            e.exit_code !== undefined && e.exit_code !== 0 ? "bad" : undefined
          }
        />
      )}
    </div>
  );
}

function EntryRow({
  e,
  sessionLabel,
}: {
  e: SandboxLogEntry;
  sessionLabel: string | null;
}) {
  return (
    <li
      className={cx(
        "flex gap-2 border-b border-line px-2 py-1.5 text-xs last:border-b-0",
        LEVEL_ROW[e.level],
      )}
    >
      <time
        dateTime={e.ts}
        className="w-[5.5rem] shrink-0 font-mono text-[11px] text-muted"
      >
        {clock(e.ts)}
      </time>
      <span
        title={e.kind}
        className={cx(
          "mt-0.5 flex shrink-0 items-center gap-1 text-muted",
          e.kind === "denied" && "text-bad",
          e.level === "warn" && "text-warn",
          e.level === "error" && "text-bad",
        )}
      >
        <Icon name={KIND_ICON[e.kind]} size={13} />
      </span>
      {sessionLabel && (
        <span className="shrink-0 font-mono text-[11px] text-muted">
          {sessionLabel}
        </span>
      )}
      <EntryBody e={e} />
    </li>
  );
}

function SessionHeader({
  s,
  index,
  count,
}: {
  s: SandboxSession;
  index: number;
  count: number;
}) {
  const now = useNow();
  const prov = provisioningMs(s);
  const status = s.status;
  const tone =
    status === "ready"
      ? "ok"
      : status === "provisioning"
        ? "run"
        : status === "closed"
          ? "muted"
          : status === "failed"
            ? "bad"
            : "warn";
  const origin = [s.repo, s.ref].filter(Boolean).join("@");
  return (
    <div
      className={cx(
        "rounded-md border bg-raised p-2 text-xs",
        s.error ? "border-bad/50" : "border-line",
      )}
    >
      <div className="flex flex-wrap items-center gap-2">
        {count > 1 && (
          <span className="font-mono text-muted">#{index + 1}</span>
        )}
        <span className="font-semibold">{s.backend}</span>
        <Badge tone={tone}>{status}</Badge>
        {s.lifecycle && <Badge>{s.lifecycle.replace("_", " ")}</Badge>}
        {s.truncated && <Badge tone="warn">log truncated</Badge>}
        <span className="ml-auto text-muted">
          {prov !== null && <>provisioned in {formatDuration(prov)} · </>}
          {durationBetween(s.started_at, s.ended_at, now)} total
        </span>
      </div>
      <dl className="m-0 mt-1 grid grid-cols-[5rem_1fr] gap-x-2 gap-y-0.5">
        {s.sandbox_name && <Row label="Sandbox" value={s.sandbox_name} />}
        {s.sandbox_id && <Row label="Sandbox id" value={s.sandbox_id} />}
        {s.image && <Row label="Image" value={s.image} />}
        {origin && <Row label="Repo" value={origin} />}
        {s.workdir && <Row label="Workdir" value={s.workdir} />}
      </dl>
      {s.env_names.length > 0 && (
        <details className="mt-1">
          <summary className="cursor-pointer select-none text-muted">
            {s.env_names.length} environment variable
            {s.env_names.length === 1 ? "" : "s"}
          </summary>
          <div className="mt-1 flex flex-wrap gap-1">
            {s.env_names.map((n) => (
              <Badge key={n} className="font-mono">
                {n}
              </Badge>
            ))}
          </div>
        </details>
      )}
      {s.error && (
        <div className="mt-1 whitespace-pre-wrap break-words rounded border border-bad/40 bg-bad/10 p-1.5 text-bad">
          {s.error}
        </div>
      )}
    </div>
  );
}

function Row({ label, value }: { label: string; value: ReactNode }) {
  return (
    <>
      <dt className="text-muted">{label}</dt>
      <dd className="m-0 min-w-0 break-words font-mono text-[11px]">{value}</dd>
    </>
  );
}

function CopyRawButton({ scope, id }: { scope: SandboxScope; id: string }) {
  const [state, setState] = useState<"idle" | "busy" | "copied" | "failed">(
    "idle",
  );
  const copy = async () => {
    setState("busy");
    try {
      const res = await fetch(sandboxLogUrl(scope, id, true));
      if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
      await navigator.clipboard.writeText(await res.text());
      setState("copied");
    } catch {
      setState("failed");
    }
    setTimeout(() => setState("idle"), 1500);
  };
  return (
    <Button disabled={state === "busy"} onClick={() => void copy()}>
      {
        {
          idle: "Copy raw log",
          busy: "Copying",
          copied: "Copied",
          failed: "Copy failed",
        }[state]
      }
    </Button>
  );
}

/** Distance from the bottom, in pixels, within which the view counts as following the tail. */
const FOLLOW_SLACK = 24;

function LogBody({
  data,
  scope,
  id,
  loadEarlier,
}: {
  data: SandboxLogData;
  scope: SandboxScope;
  id: string;
  loadEarlier: () => void;
}) {
  const live = data.sessions.some(isLiveSession);
  const [follow, setFollow] = useState(true);
  const scroller = useRef<HTMLDivElement>(null);
  const { sessions, entries } = data;

  useEffect(() => {
    const el = scroller.current;
    if (follow && live && el) el.scrollTop = el.scrollHeight;
  }, [entries, follow, live]);

  const labels = new Map(sessions.map((s, i) => [s.id, `#${i + 1}`]));
  const multi = sessions.length > 1;
  const atCap = entries.length >= SANDBOX_LOG_MAX;
  return (
    <div className="space-y-2">
      {sessions.map((s, i) => (
        <SessionHeader key={s.id} s={s} index={i} count={sessions.length} />
      ))}
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs text-muted">
          {sessions.reduce((n, s) => n + s.entry_count, 0)} entries
        </span>
        <span className="ml-auto flex items-center gap-2">
          {live && (
            <Toggle checked={follow} onChange={setFollow} label="Follow" />
          )}
          <CopyRawButton scope={scope} id={id} />
          <a
            href={sandboxLogUrl(scope, id, true)}
            download
            className="inline-flex h-7 items-center rounded-md border border-line bg-panel px-2.5 text-xs font-medium hover:bg-raised"
          >
            Download
          </a>
        </span>
      </div>
      {data.hasMore && (
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted">
          {atCap ? (
            <span>
              Showing the newest {entries.length} entries. Download has the
              whole log.
            </span>
          ) : (
            <>
              <span>Older entries are not shown.</span>
              <Button onClick={loadEarlier}>Load earlier</Button>
            </>
          )}
        </div>
      )}
      <div
        ref={scroller}
        onScroll={(ev) => {
          const el = ev.currentTarget;
          const atBottom =
            el.scrollHeight - el.scrollTop - el.clientHeight <= FOLLOW_SLACK;
          if (atBottom !== follow) setFollow(atBottom);
        }}
        className="max-h-[32rem] overflow-auto rounded-md border border-line"
      >
        {entries.length === 0 ? (
          <div className="p-3 text-xs text-muted">No log entries yet.</div>
        ) : (
          <ul className="m-0 list-none p-0">
            {entries.map((e) => (
              <EntryRow
                key={e.id}
                e={e}
                sessionLabel={
                  multi
                    ? (labels.get(e.session_id) ?? shortId(e.session_id))
                    : null
                }
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

/**
 * The sandbox run log of an agent run (`agent`) or of a node run (`node`).
 * Renders nothing until the run has at least one sandbox session, so callers
 * can place it unconditionally.
 */
export function SandboxLogSection({
  scope,
  id,
  variant,
}: {
  scope: SandboxScope;
  id: string | undefined;
  variant: "panel" | "section";
}) {
  const agent = useAgentRunSandboxLog(scope === "agent" ? id : undefined);
  const node = useNodeRunSandboxLog(scope === "node" ? id : undefined);
  const q = scope === "agent" ? agent : node;
  if (id === undefined || !q.data) {
    // A failed fetch must not look like "no sandbox": say so, but stay quiet while loading.
    if (q.isError)
      return (
        <ErrorState
          error={new Error(`Sandbox log: ${errorMessage(q.error)}`)}
          onRetry={() => void q.refetch()}
        />
      );
    return null;
  }
  if (q.data.sessions.length === 0) return null;
  const body = (
    <LogBody data={q.data} scope={scope} id={id} loadEarlier={q.loadEarlier} />
  );
  const count = q.data.sessions.reduce((n, s) => n + s.entry_count, 0);
  if (variant === "panel")
    return (
      <Panel
        title={
          <>
            Sandbox <Badge>{count}</Badge>
          </>
        }
      >
        {body}
      </Panel>
    );
  return (
    <section>
      <h3 className="m-0 mb-1.5 text-[11px] font-semibold uppercase tracking-wide text-muted">
        Sandbox <Badge>{count}</Badge>
      </h3>
      {body}
    </section>
  );
}

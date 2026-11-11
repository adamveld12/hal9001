import type { AgentRun, NodeRun } from "../../api/runs";
import { BlobView } from "../../components/BlobView";
import { CodeEditor } from "../../components/CodeEditor";
import { JsonView } from "../../components/JsonView";
import { Badge, Button, StatusBadge } from "../../components/ui";
import { Time } from "../../components/Time";
import { cx } from "../../lib/cx";
import { langForCommand } from "../../lib/codeLang";
import { shortId } from "../../lib/format";
import {
  GATE_OUTCOME_TEXT,
  gateOutcome,
  VISUAL_LABEL,
  VISUAL_TONE,
} from "../../flow/overlay";
import { runVisual } from "../../flow/runOverlay";
import { summarizeStep } from "../../flow/transform/steps";
import type { TransformNode } from "../../flow/transform/types";
import { Meta, Mono, SectionTitle, TextLink } from "./Meta";
import { ContinueInChatButton, ForksSection } from "./ContinueInChat";
import { SEED_LABEL, seedKind } from "./continue";
import { SandboxLogSection } from "./SandboxLog";
import {
  awaitedApprovalId,
  commandSnapshot,
  indexText,
  runDuration,
  type RunLabel,
} from "./runView";

function Capture({ label, blob }: { label: string; blob: NodeRun["stdout"] }) {
  return (
    <details open={blob.size > 0} className="group">
      <summary className="cursor-pointer select-none text-[11px] font-semibold uppercase tracking-wide text-muted">
        {label}
        {blob.size === 0 && (
          <span className="ml-2 font-normal normal-case">empty</span>
        )}
      </summary>
      <div className="mt-1.5">
        <BlobView blob={blob} />
      </div>
    </details>
  );
}

function RunList({
  runs,
  labels,
  selectedId,
  onSelect,
}: {
  runs: readonly NodeRun[];
  labels: ReadonlyMap<string, RunLabel>;
  selectedId: string;
  onSelect: (id: string) => void;
}) {
  return (
    <ul className="max-h-44 divide-y divide-line overflow-y-auto rounded-md border border-line">
      {runs.map((nr) => {
        const label = labels.get(nr.id);
        const visual = runVisual(nr);
        return (
          <li key={nr.id}>
            <button
              type="button"
              onClick={() => onSelect(nr.id)}
              aria-current={nr.id === selectedId}
              className={cx(
                "flex w-full items-center gap-2 px-2 py-1 text-left text-xs hover:bg-raised",
                nr.id === selectedId && "bg-raised",
              )}
            >
              <span className="w-10 shrink-0 font-mono text-muted">
                {label ? indexText(label) : shortId(nr.id)}
              </span>
              <Badge tone={VISUAL_TONE[visual]}>{VISUAL_LABEL[visual]}</Badge>
              {seedKind(nr.reason) === "reused" && <Badge>reused</Badge>}
              <span className="min-w-0 flex-1 truncate text-muted">
                {nr.reason ?? nr.error ?? ""}
              </span>
              <span className="shrink-0 text-muted">{runDuration(nr)}</span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

/** A transform node run: its steps in plain language, then the jq that ran. */
function TransformSnapshot({
  node,
  script,
}: {
  node: TransformNode;
  script: string | undefined;
}) {
  const steps = node.steps ?? [];
  return (
    <section className="space-y-2">
      <SectionTitle>Steps</SectionTitle>
      {steps.length === 0 ? (
        <p className="m-0 text-muted">No steps.</p>
      ) : (
        <ol className="m-0 space-y-0.5 pl-5">
          {steps.map((s, i) => (
            <li key={i}>{summarizeStep(s)}</li>
          ))}
        </ol>
      )}
      <SectionTitle>Compiled jq</SectionTitle>
      {script ? (
        <CodeEditor
          readOnly
          value={script}
          lang={langForCommand("jq")}
          minHeight="3rem"
          maxHeight="16rem"
        />
      ) : (
        <p className="m-0 text-muted">The node did not run a program.</p>
      )}
    </section>
  );
}

function Snapshot({ run }: { run: NodeRun }) {
  if (run.node.type === "transform") {
    return <TransformSnapshot node={run.node} script={run.script} />;
  }
  const snap = commandSnapshot(run.node);
  return (
    <section>
      <SectionTitle>
        {run.node.type === "command" ? "Script snapshot" : "Node snapshot"}
      </SectionTitle>
      {snap ? (
        <CodeEditor
          readOnly
          value={snap.code}
          lang={langForCommand(snap.lang)}
          minHeight="3rem"
          maxHeight="16rem"
        />
      ) : (
        <JsonView value={run.node} maxHeight="16rem" />
      )}
    </section>
  );
}

function AgentRunLinks({ runs }: { runs: readonly AgentRun[] }) {
  if (runs.length === 0) return null;
  return (
    <section>
      <SectionTitle>Agent runs</SectionTitle>
      <ul className="m-0 list-none space-y-1 p-0 text-xs">
        {runs.map((a) => (
          <li key={a.id} className="flex items-center gap-2">
            <TextLink to={`/agent-runs/${a.id}`}>
              <Mono>{shortId(a.id)}</Mono>
            </TextLink>
            <span className="text-muted">{a.phase}</span>
            <StatusBadge status={a.status} />
            <span className="text-muted">{a.runtime}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function NodeRunPanel({
  nodeId,
  runs,
  labels,
  agentRuns,
  selectedRunId,
  onSelectRun,
  onClose,
}: {
  nodeId: string;
  runs: readonly NodeRun[];
  labels: ReadonlyMap<string, RunLabel>;
  agentRuns: readonly AgentRun[];
  selectedRunId: string | null;
  onSelectRun: (id: string) => void;
  onClose: () => void;
}) {
  const run = runs.find((r) => r.id === selectedRunId) ?? runs[0];
  const parent = run ? labels.get(run.id)?.parent : null;
  const approvalId = run ? awaitedApprovalId(run) : null;
  const outcome = run ? gateOutcome(run) : null;
  const seed = run ? seedKind(run.reason) : null;
  const nodeAgentRuns = run
    ? agentRuns
        .filter((a) => a.node_run_id === run.id)
        .sort((a, b) => a.created_at.localeCompare(b.created_at))
    : [];
  const lastAgentRun = nodeAgentRuns[nodeAgentRuns.length - 1];
  return (
    <div className="space-y-4 p-3 text-xs">
      <div className="flex items-center gap-2">
        <div className="min-w-0">
          <div className="truncate text-sm font-semibold">{nodeId}</div>
          {run && <div className="text-muted">{run.node.type} node</div>}
        </div>
        <Button className="ml-auto" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </div>

      {!run ? (
        <p className="m-0 text-muted">
          This node did not run in this flow run.
        </p>
      ) : (
        <>
          {runs.length > 1 && (
            <RunList
              runs={runs}
              labels={labels}
              selectedId={run.id}
              onSelect={onSelectRun}
            />
          )}
          <Meta
            rows={[
              {
                label: "Status",
                value: (
                  <span className="inline-flex flex-wrap items-center gap-1.5">
                    <StatusBadge status={run.status} />
                    {outcome ? (
                      <Badge tone="warn">{GATE_OUTCOME_TEXT[outcome]}</Badge>
                    ) : (
                      run.decision && <StatusBadge status={run.decision} />
                    )}
                    {seed && <Badge>{SEED_LABEL[seed]}</Badge>}
                    {approvalId && (
                      <TextLink to={`/inbox/approval/${approvalId}`}>
                        waiting for approval
                      </TextLink>
                    )}
                  </span>
                ),
              },
              { label: "Reason", value: run.reason },
              {
                label: "Error",
                value: run.error ? <Mono>{run.error}</Mono> : null,
              },
              {
                label: "Ports",
                value:
                  run.ports.length > 0 ? (
                    <span className="inline-flex flex-wrap gap-1">
                      {run.ports.map((p) => (
                        <Badge key={p}>{p}</Badge>
                      ))}
                      <span className="text-muted">
                        {run.emitted_count} emitted
                      </span>
                    </span>
                  ) : (
                    <span className="text-muted">none</span>
                  ),
              },
              {
                label: "Exit code",
                value:
                  run.exit_code === null ? null : <Mono>{run.exit_code}</Mono>,
              },
              { label: "Duration", value: runDuration(run) },
              { label: "Started", value: <Time ts={run.started_at} /> },
              { label: "Ended", value: <Time ts={run.ended_at} /> },
              {
                label: "Fed by",
                value: parent ? (
                  <span>
                    {parent.nodeId} {indexText(parent)}
                  </span>
                ) : null,
              },
              {
                label: "Env keys",
                value:
                  run.env_keys.length > 0 ? (
                    <span className="inline-flex flex-wrap gap-1">
                      {run.env_keys.map((k) => (
                        <Badge key={k}>{k}</Badge>
                      ))}
                    </span>
                  ) : null,
              },
              {
                label: "Argv",
                value:
                  run.argv && run.argv.length > 0 ? (
                    <Mono>{run.argv.join(" ")}</Mono>
                  ) : null,
              },
            ]}
          />
          <AgentRunLinks runs={nodeAgentRuns} />
          {run.node.type === "agent" && lastAgentRun && (
            <>
              <ContinueInChatButton run={lastAgentRun} />
              <ForksSection agentRunId={lastAgentRun.id} />
            </>
          )}
          <Snapshot run={run} />
          {run.node.type === "agent" && (
            <SandboxLogSection scope="node" id={run.id} variant="section" />
          )}
          <section className="space-y-3">
            <Capture label="stdin" blob={run.stdin} />
            <Capture label="stdout" blob={run.stdout} />
            <Capture label="stderr" blob={run.stderr} />
          </section>
        </>
      )}
    </div>
  );
}

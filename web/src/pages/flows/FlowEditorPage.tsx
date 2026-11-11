import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { InlineEdit } from "../../components/InlineEdit";
import { useRenameFlow } from "../../api/rename";
import { validateName } from "../../lib/inlineEdit";
import { withDescription } from "../files/description";
import { ApiError, errorMessage } from "../../api/client";
import {
  renderFlowYaml,
  useFlow,
  useFlows,
  useUpdateFlow,
  useValidateFlow,
  type FlowDetail,
  type FlowRunNowResult,
} from "../../api/flows";
import { usePromptNames } from "../../api/prompts";
import { useSensorIds } from "../../api/sensors";
import { useRuntimeOptions } from "../../api/settings";
import { CodeEditor } from "../../components/CodeEditor";
import {
  Button,
  ErrorState,
  LoadingState,
  PageHeader,
} from "../../components/ui";
import { Tabs } from "../../components/Tabs";
import { flowFromApi, flowToApi } from "../../flow/apiAdapter";
import { FlowEditor } from "../../flow/FlowEditor";
import type { Diagnostic, Flow, FlowNode } from "../../flow/model";
import { RunNowPanel } from "../../components/RunNowPanel";
import { FlowRunNow } from "./FlowRunNow";
import { usePageContext } from "../../layout/pageContext";
import { useAssist } from "../assist/context";
import { flowContext } from "../assist/pageContexts";
import { useBoundSession } from "../assist/useDraftSync";
import { FlowRunsList } from "../runs/FlowRunsList";
import { NO_FLOW_RUN_FILTERS } from "../runs/runList";
import { FlowDraftView } from "./FlowDraftView";
import { TestNodePanel } from "./TestNodePanel";

type View = "visual" | "yaml" | "runs";

function problemDiagnostics(e: unknown): Diagnostic[] | undefined {
  if (e instanceof ApiError && e.kind === "validation-failed") {
    return e.problem?.diagnostics as Diagnostic[] | undefined;
  }
  return undefined;
}

function saveMessage(e: unknown): string {
  if (e instanceof ApiError && e.kind === "conflict") {
    return "The file changed on disk since you opened it. Reload to see the new version.";
  }
  if (problemDiagnostics(e))
    return "The server rejected the flow; see Problems.";
  return errorMessage(e);
}

function DiagnosticList({ diags }: { diags: Diagnostic[] }) {
  if (diags.length === 0) return null;
  return (
    <ul className="space-y-1 border-t border-line px-4 py-2 text-xs">
      {diags.map((d) => (
        <li
          key={`${d.path}|${d.message}`}
          className={d.severity === "error" ? "text-bad" : "text-warn"}
        >
          {d.line ? `line ${d.line}: ` : ""}
          {d.message}
        </li>
      ))}
    </ul>
  );
}

/** Reports the node whose form is open (rendered in the node form, so it exists only while one is selected). */
function ReportSelection({
  node,
  onSelect,
}: {
  node: FlowNode;
  onSelect: (node: { id: string; type: string } | null) => void;
}) {
  const { id, type } = node;
  useEffect(() => {
    onSelect({ id, type });
    return () => onSelect(null);
  }, [id, type, onSelect]);
  return null;
}

function Loaded({
  initial,
  refetched,
}: {
  initial: FlowDetail;
  refetched: FlowDetail;
}) {
  const [loaded, setLoaded] = useState(initial);
  const navigate = useNavigate();
  const rename = useRenameFlow();
  const otherIds = (useFlows().data ?? [])
    .map((x) => x.id)
    .filter((x) => x !== initial.id);
  const [generation, setGeneration] = useState(0);
  const [view, setView] = useState<View>(initial.flow ? "visual" : "yaml");
  const [dirty, setDirty] = useState(false);
  const [runFilters, setRunFilters] = useState(NO_FLOW_RUN_FILTERS);
  const [yamlText, setYamlText] = useState(initial.yaml);
  const [serverDiags, setServerDiags] = useState<Diagnostic[]>(
    initial.diagnostics,
  );
  const [current, setCurrent] = useState<Flow | undefined>();

  // With the assistant open, edits the canvas holds travel with the first message.
  const assist = useAssist();
  const edits = useRef<{ flow: Flow | undefined; dirty: boolean }>({
    flow: undefined,
    dirty: false,
  });
  useEffect(() => {
    edits.current = { flow: current, dirty };
  });
  const { registerEditor } = assist;
  useEffect(() => {
    registerEditor({
      kind: "flow",
      id: initial.id,
      flush: async () => undefined,
      dirty: () => edits.current.dirty,
      getText: async () => {
        const { flow, dirty: unsaved } = edits.current;
        return unsaved && flow
          ? ((await renderFlowYaml(flow)) ?? undefined)
          : undefined;
      },
    });
    return () => registerEditor(null);
  }, [registerEditor, initial.id]);
  const [selected, setSelected] = useState<{
    id: string;
    type: string;
  } | null>(null);
  const [previewYaml, setPreviewYaml] = useState<string | undefined>();
  const [runNow, setRunNow] = useState<{
    result: FlowRunNowResult;
    note?: string;
  } | null>(null);

  const update = useUpdateFlow(loaded.id);
  const validate = useValidateFlow();
  const sensors = useSensorIds();
  const prompts = usePromptNames();
  const runtimes = useRuntimeOptions();

  const changedOnDisk = refetched.hash !== loaded.hash;

  const { reset: resetUpdate } = update;
  const adopt = useCallback(
    (d: FlowDetail) => {
      setLoaded(d);
      setYamlText(d.yaml);
      setServerDiags(d.diagnostics);
      setDirty(false);
      setGeneration((g) => g + 1);
      // A rejected save refers to the version that was just discarded.
      resetUpdate();
    },
    [resetUpdate],
  );

  // Pick up edits made on disk right away unless there is unsaved work.
  // (State adjusted during render; the condition is false again after adopt.)
  if (changedOnDisk && !dirty) adopt(refetched);

  const onChange = useCallback((flow: Flow, isDirty: boolean) => {
    setCurrent(flow);
    setDirty(isDirty);
  }, []);

  // Server-side checks (sensor/prompt/runtime references, templates), debounced.
  const latest = useRef(0);
  const { mutate: runValidate } = validate;
  useEffect(() => {
    if (!current || !dirty) return;
    const token = ++latest.current;
    const t = setTimeout(() => {
      runValidate(
        { flow: flowToApi(current) },
        {
          onSuccess: (r) => {
            if (token !== latest.current) return;
            setServerDiags(r.diagnostics);
            setPreviewYaml(r.yaml ?? undefined);
          },
        },
      );
    }, 600);
    return () => clearTimeout(t);
  }, [current, dirty, runValidate]);

  const save = async (flow: Flow) => {
    try {
      const saved = await update.mutateAsync({
        expectedHash: loaded.hash,
        flow: flowToApi(flow),
      });
      setLoaded(saved);
      setYamlText(saved.yaml);
      setServerDiags(saved.diagnostics);
    } catch (e) {
      const d = problemDiagnostics(e);
      if (d) setServerDiags(d);
      throw e;
    }
  };

  const saveYaml = () => {
    update.mutate(
      { expectedHash: loaded.hash, yaml: yamlText },
      {
        onSuccess: (saved) => adopt(saved),
        onError: (e) => {
          const d = problemDiagnostics(e);
          if (d) setServerDiags(d);
        },
      },
    );
  };

  const flow = loaded.flow ? flowFromApi(loaded.flow) : undefined;

  usePageContext(
    flowContext({
      id: loaded.id,
      description: loaded.flow?.description,
      enabled: loaded.flow?.enabled ?? false,
      nodeCount: (current ?? flow)?.nodes.length,
      view,
      selected: view === "visual" ? selected : null,
      diagnostics: serverDiags,
      dirty: dirty || yamlText !== loaded.yaml,
      drafting: false,
    }),
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader
        title={
          <InlineEdit
            mono
            label="Flow name"
            value={loaded.id}
            validate={(v) => validateName(v, loaded.id, otherIds, "flow")}
            disabledReason={
              dirty || yamlText !== loaded.yaml
                ? "Save or discard your edits before renaming"
                : null
            }
            onSave={(newId) =>
              rename.mutateAsync(
                { id: loaded.id, newId, expectedHash: loaded.hash },
                {
                  onSuccess: () =>
                    void navigate(
                      {
                        pathname: `/flows/${newId}`,
                        search: window.location.search,
                      },
                      { replace: true },
                    ),
                },
              )
            }
          />
        }
        subtitle={
          <span className="flex items-center gap-2">
            <span>{loaded.path}</span>
            <InlineEdit
              muted
              label="Flow description"
              placeholder="Add a description"
              value={loaded.flow?.description ?? ""}
              validate={() => null}
              disabledReason={
                dirty || yamlText !== loaded.yaml
                  ? "Save or discard your edits first"
                  : null
              }
              onSave={(text) =>
                update
                  .mutateAsync({
                    expectedHash: loaded.hash,
                    yaml: withDescription(loaded.yaml, text),
                  })
                  .then((saved) => adopt(saved))
              }
            />
          </span>
        }
        actions={
          <>
            <FlowRunNow
              flowId={loaded.id}
              enabled={loaded.flow?.enabled ?? false}
              subscribeCount={loaded.flow?.subscribe?.length ?? 0}
              dirty={dirty || yamlText !== loaded.yaml}
              onResult={(result, note) => setRunNow({ result, note })}
            />
            <Link
              to={`/timeline?flow=${loaded.id}`}
              className="text-xs text-accent underline"
            >
              Timeline
            </Link>
            <Link
              to={`/flows/${loaded.id}/versions/${loaded.hash}`}
              className="text-xs text-accent underline"
            >
              Saved version
            </Link>
            <Link to="/flows" className="text-xs text-muted underline">
              All flows
            </Link>
            <Button
              icon="agent"
              disabled={
                assist.open &&
                assist.target.kind === "flow" &&
                assist.target.id === loaded.id
              }
              title="Chat with an AI agent that edits this flow's draft"
              onClick={() => assist.openTarget({ kind: "flow", id: loaded.id })}
            >
              Ask AI
            </Button>
          </>
        }
      />
      {runNow && (
        <RunNowPanel
          result={runNow.result}
          note={runNow.note}
          onClose={() => setRunNow(null)}
        />
      )}
      {changedOnDisk && dirty && (
        <div
          role="alert"
          className="flex items-center gap-3 border-b border-warn/40 bg-warn/10 px-4 py-1.5 text-xs text-warn"
        >
          This flow changed on disk. Saving will be rejected until you reload.
          <Button onClick={() => adopt(refetched)}>
            Reload and discard my edits
          </Button>
        </div>
      )}
      <Tabs
        tabs={[
          { key: "visual", label: "Visual" },
          { key: "yaml", label: "YAML" },
          { key: "runs", label: "Runs" },
        ]}
        active={view}
        onChange={(v) => (v === "visual" && !flow ? undefined : setView(v))}
      />
      <div className="min-h-0 flex-1">
        {view === "visual" && flow && (
          <FlowEditor
            key={`${loaded.id}:${generation}`}
            flow={flow}
            sensors={sensors.data ?? []}
            prompts={prompts.data ?? []}
            runtimes={runtimes.data ?? []}
            serverDiagnostics={serverDiags}
            saving={update.isPending}
            saveError={update.isError ? saveMessage(update.error) : undefined}
            onSave={save}
            onChange={onChange}
            nodeActions={(node, f) => (
              <>
                <ReportSelection node={node} onSelect={setSelected} />
                <TestNodePanel flowId={loaded.id} node={node} flow={f} />
              </>
            )}
          />
        )}
        {view === "runs" && (
          <div className="flex h-full flex-col">
            <FlowRunsList
              flowId={loaded.id}
              filters={runFilters}
              onChange={setRunFilters}
            />
          </div>
        )}
        {view === "yaml" && (
          <div className="flex h-full flex-col overflow-auto">
            {!flow && (
              <div
                role="alert"
                className="border-b border-bad/40 bg-bad/10 px-4 py-2 text-xs text-bad"
              >
                This file does not parse, so the visual editor is unavailable.
                Fix the YAML below.
              </div>
            )}
            {dirty ? (
              <div className="space-y-2 p-4">
                <p className="text-xs text-muted">
                  You have unsaved visual edits. This is the YAML they would
                  write; save or discard them in the Visual tab to edit YAML
                  directly.
                </p>
                <CodeEditor
                  lang="yaml"
                  value={previewYaml ?? "(validating…)"}
                  readOnly
                  minHeight="20rem"
                  maxHeight="70vh"
                />
              </div>
            ) : (
              <div className="space-y-2 p-4">
                <CodeEditor
                  lang="yaml"
                  label="Flow YAML"
                  value={yamlText}
                  onChange={setYamlText}
                  minHeight="20rem"
                  maxHeight="70vh"
                />
                <div className="flex items-center gap-2">
                  <Button
                    onClick={() =>
                      validate.mutate(
                        { yaml: yamlText },
                        { onSuccess: (r) => setServerDiags(r.diagnostics) },
                      )
                    }
                    disabled={validate.isPending}
                  >
                    Validate
                  </Button>
                  <Button
                    variant="primary"
                    icon="save"
                    disabled={yamlText === loaded.yaml || update.isPending}
                    onClick={saveYaml}
                  >
                    Save YAML
                  </Button>
                  {update.isError && (
                    <span role="alert" className="text-xs text-bad">
                      {saveMessage(update.error)}
                    </span>
                  )}
                </div>
              </div>
            )}
            <DiagnosticList diags={serverDiags} />
          </div>
        )}
      </div>
    </div>
  );
}

/** The flow under an open session: its canvas shows the session's draft. */
function DraftEditor({
  detail,
  sessionId,
}: {
  detail: FlowDetail;
  sessionId: string;
}) {
  usePageContext(
    flowContext({
      id: detail.id,
      description: detail.flow?.description,
      enabled: detail.flow?.enabled ?? false,
      view: "visual",
      selected: null,
      diagnostics: [],
      dirty: false,
      drafting: true,
    }),
  );
  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader
        title={detail.id}
        subtitle={`${detail.path} · draft`}
        actions={
          <>
            <Link
              to={`/timeline?flow=${detail.id}`}
              className="text-xs text-accent underline"
            >
              Timeline
            </Link>
            <Link to="/flows" className="text-xs text-muted underline">
              All flows
            </Link>
          </>
        }
      />
      <div className="min-h-0 flex-1">
        <FlowDraftView targetId={detail.id} sessionId={sessionId} />
      </div>
    </div>
  );
}

function FlowEditorBody({ id }: { id: string }) {
  const q = useFlow(id);
  const bound = useBoundSession("flow", id);
  if (q.isPending) return <LoadingState label="Loading flow" />;
  if (q.isError)
    return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (bound.sessionId !== null) {
    return <DraftEditor detail={q.data} sessionId={bound.sessionId} />;
  }
  return <Loaded key={id} initial={q.data} refetched={q.data} />;
}

export function FlowEditorPage() {
  const { id = "" } = useParams();
  return (
    <div className="h-full min-h-0 min-w-0">
      <FlowEditorBody id={id} />
    </div>
  );
}

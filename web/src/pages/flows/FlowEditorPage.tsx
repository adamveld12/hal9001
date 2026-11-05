import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router";
import { ApiError, errorMessage } from "../../api/client";
import {
  useFlow,
  useUpdateFlow,
  useValidateFlow,
  type FlowDetail,
} from "../../api/flows";
import { usePromptNames } from "../../api/prompts";
import { useSensorIds } from "../../api/sensors";
import { useRuntimeNames } from "../../api/settings";
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
import type { Diagnostic, Flow } from "../../flow/model";
import { TestNodePanel } from "./TestNodePanel";

type View = "visual" | "yaml";

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

function Loaded({
  initial,
  refetched,
}: {
  initial: FlowDetail;
  refetched: FlowDetail;
}) {
  const [loaded, setLoaded] = useState(initial);
  const [generation, setGeneration] = useState(0);
  const [view, setView] = useState<View>(initial.flow ? "visual" : "yaml");
  const [dirty, setDirty] = useState(false);
  const [yamlText, setYamlText] = useState(initial.yaml);
  const [serverDiags, setServerDiags] = useState<Diagnostic[]>(
    initial.diagnostics,
  );
  const [current, setCurrent] = useState<Flow | undefined>();
  const [previewYaml, setPreviewYaml] = useState<string | undefined>();

  const update = useUpdateFlow(loaded.id);
  const validate = useValidateFlow();
  const sensors = useSensorIds();
  const prompts = usePromptNames();
  const runtimes = useRuntimeNames();

  const changedOnDisk = refetched.hash !== loaded.hash;

  const adopt = useCallback((d: FlowDetail) => {
    setLoaded(d);
    setYamlText(d.yaml);
    setServerDiags(d.diagnostics);
    setDirty(false);
    setGeneration((g) => g + 1);
  }, []);

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

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader
        title={loaded.id}
        subtitle={loaded.path}
        actions={
          <>
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
          </>
        }
      />
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
              <TestNodePanel flowId={loaded.id} node={node} flow={f} />
            )}
          />
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

export function FlowEditorPage() {
  const { id = "" } = useParams();
  const q = useFlow(id);
  if (q.isPending) return <LoadingState label="Loading flow" />;
  if (q.isError)
    return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  return <Loaded key={id} initial={q.data} refetched={q.data} />;
}

import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useState,
  type ReactNode,
} from "react";
import { Icon } from "../components/icons";
import { Badge, Button, Field, Kbd, TextInput, Toggle } from "../components/ui";
import { cx } from "../lib/cx";
import {
  canConnect,
  editorReducer,
  initState,
  isDirty,
  selectedFlowNode,
  serialize,
  subscribedSensors,
} from "./editorState";
import { FlowCanvas, type PaletteDrag } from "./FlowCanvas";
import { Inspector } from "./Inspector";
import { type Diagnostic, type Flow, type FlowNode, type UIPos } from "./model";
import { Palette } from "./Palette";
import {
  cycleEdgeKeys,
  hasErrors,
  indexDiagnostics,
  validateFlow,
} from "./validate";

export interface FlowEditorProps {
  /** Initial flow; remount (change `key`) to load a different one. */
  flow: Flow;
  sensors: string[];
  prompts: string[];
  runtimes: string[];
  /** Diagnostics from the server's last validate/save. */
  serverDiagnostics?: Diagnostic[];
  saving?: boolean;
  /** Save failure shown in the toolbar (e.g. a conflict). */
  saveError?: string;
  /** Resolves when the server accepted the flow; reject to keep it dirty. */
  onSave: (flow: Flow) => Promise<unknown>;
  /** Extra controls under the node form (e.g. "Run node on sample"). */
  nodeActions?: (node: FlowNode, flow: Flow) => ReactNode;
}

function dedupe(diags: Diagnostic[]): Diagnostic[] {
  const seen = new Set<string>();
  return diags.filter((d) => {
    const key = `${d.severity}|${d.node_id ?? ""}|${d.path ?? ""}|${d.message}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export function FlowEditor(props: FlowEditorProps) {
  const [state, dispatch] = useReducer(editorReducer, props.flow, initState);
  const [problemsOpen, setProblemsOpen] = useState(true);

  const flow = useMemo(() => serialize(state), [state]);
  const dirty = isDirty(state);
  const clientDiagnostics = useMemo(() => validateFlow(flow), [flow]);
  const allDiagnostics = useMemo(
    () => dedupe([...clientDiagnostics, ...(props.serverDiagnostics ?? [])]),
    [clientDiagnostics, props.serverDiagnostics],
  );
  const diagIndex = useMemo(
    () => indexDiagnostics(allDiagnostics, flow),
    [allDiagnostics, flow],
  );
  const cycleKeys = useMemo(() => cycleEdgeKeys(flow), [flow]);
  const subscribed = useMemo(() => new Set(subscribedSensors(state)), [state]);
  const knownSensors = useMemo(() => new Set(props.sensors), [props.sensors]);
  const selected = selectedFlowNode(state);
  const blocked = hasErrors(clientDiagnostics);

  const save = useCallback(() => {
    if (blocked || !dirty || props.saving) return;
    props.onSave(flow).then(
      () => dispatch({ type: "markSaved", flow }),
      () => undefined, // the caller surfaces the failure through `saveError`
    );
  }, [blocked, dirty, flow, props]);

  // Warn before leaving with unsaved edits.
  useEffect(() => {
    if (!dirty) return;
    const handler = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [dirty]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (!mod) return;
      const inEditable =
        e.target instanceof HTMLElement &&
        (e.target.closest(".cm-editor") !== null ||
          e.target instanceof HTMLInputElement ||
          e.target instanceof HTMLTextAreaElement);
      if (e.key === "s") {
        e.preventDefault();
        save();
      } else if (e.key === "z" && !inEditable) {
        e.preventDefault();
        dispatch({ type: e.shiftKey ? "redo" : "undo" });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [save]);

  const onDropPalette = useCallback((item: PaletteDrag, position: UIPos) => {
    if (item.kind === "node")
      dispatch({ type: "addNode", nodeType: item.type, position });
    else dispatch({ type: "addSensor", sensor: item.id, position });
  }, []);

  const selectNode = (id: string) =>
    dispatch({
      type: "nodeChanges",
      changes: state.nodes.map((n) => ({
        type: "select" as const,
        id: n.id,
        selected: n.id === id,
      })),
    });

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="flow-editor">
      <div className="flex flex-wrap items-center gap-3 border-b border-line px-3 py-2">
        <Toggle
          label="Enabled"
          checked={state.meta.enabled}
          onChange={(v) =>
            dispatch({ type: "updateMeta", patch: { enabled: v } })
          }
        />
        <Toggle
          label="Dry run"
          checked={state.meta.dry_run === true}
          onChange={(v) =>
            dispatch({ type: "updateMeta", patch: { dry_run: v } })
          }
        />
        <div className="flex items-center gap-1.5 text-xs text-muted">
          Max concurrent
          <TextInput
            type="number"
            min={1}
            className="!w-14"
            aria-label="Max concurrent runs"
            value={state.meta.max_concurrent ?? ""}
            placeholder="2"
            onChange={(e) =>
              dispatch({
                type: "updateMeta",
                patch: {
                  max_concurrent:
                    e.target.value === "" ? undefined : Number(e.target.value),
                },
              })
            }
          />
        </div>
        <div className="ml-auto flex items-center gap-2">
          {dirty && <Badge tone="warn">unsaved</Badge>}
          {props.saveError && (
            <span role="alert" className="text-xs text-bad">
              {props.saveError}
            </span>
          )}
          <Button
            icon="undo"
            aria-label="Undo"
            title="Undo (⌘Z)"
            disabled={state.past.length === 0}
            onClick={() => dispatch({ type: "undo" })}
          />
          <Button
            icon="redo"
            aria-label="Redo"
            title="Redo (⇧⌘Z)"
            disabled={state.future.length === 0}
            onClick={() => dispatch({ type: "redo" })}
          />
          <Button
            variant="primary"
            icon="save"
            disabled={!dirty || blocked || props.saving}
            title={blocked ? "Fix the errors first" : "Save to YAML (⌘S)"}
            onClick={save}
          >
            {props.saving ? "Saving…" : "Save"}
            <Kbd>⌘S</Kbd>
          </Button>
        </div>
      </div>

      <div className="flex min-h-0 flex-1">
        <div className="relative min-w-0 flex-1">
          <FlowCanvas
            nodes={state.nodes}
            edges={state.edges}
            diagnostics={diagIndex.byNode}
            knownSensors={knownSensors}
            cycleKeys={cycleKeys}
            onNodesChange={(changes) =>
              dispatch({ type: "nodeChanges", changes })
            }
            onEdgesChange={(changes) =>
              dispatch({ type: "edgeChanges", changes })
            }
            onConnect={(connection) =>
              dispatch({ type: "connect", connection })
            }
            isValidConnection={(c) =>
              "source" in c &&
              canConnect(state, {
                source: c.source,
                sourceHandle: c.sourceHandle,
                target: c.target,
              })
            }
            onDropPalette={onDropPalette}
          >
            <Palette
              sensors={props.sensors}
              subscribed={subscribed}
              onAdd={onDropPalette}
            />
          </FlowCanvas>

          {allDiagnostics.length > 0 && (
            <div className="absolute inset-x-2 bottom-2 z-10 max-w-xl rounded-md border border-line bg-panel/95 text-xs shadow-md">
              <button
                type="button"
                className="flex w-full items-center gap-2 px-2.5 py-1.5 text-left font-medium"
                aria-expanded={problemsOpen}
                onClick={() => setProblemsOpen((v) => !v)}
              >
                <Icon
                  name={hasErrors(allDiagnostics) ? "error" : "warning"}
                  size={14}
                />
                Problems
                <Badge tone={hasErrors(allDiagnostics) ? "bad" : "warn"}>
                  {allDiagnostics.length}
                </Badge>
                <span className="ml-auto text-muted">
                  {problemsOpen ? "hide" : "show"}
                </span>
              </button>
              {problemsOpen && (
                <ul className="max-h-36 divide-y divide-line overflow-y-auto border-t border-line">
                  {allDiagnostics.map((d) => (
                    <li
                      key={`${d.severity}|${d.node_id}|${d.path}|${d.message}`}
                    >
                      <button
                        type="button"
                        disabled={
                          !d.node_id ||
                          !state.nodes.some((n) => n.id === d.node_id)
                        }
                        onClick={() => d.node_id && selectNode(d.node_id)}
                        className="flex w-full items-start gap-1.5 px-2.5 py-1 text-left enabled:hover:bg-raised"
                      >
                        <Icon
                          name={d.severity === "error" ? "error" : "warning"}
                          size={13}
                          className={cx(
                            "mt-px shrink-0",
                            d.severity === "error" ? "text-bad" : "text-warn",
                          )}
                        />
                        <span>
                          {d.node_id && (
                            <span className="font-mono text-muted">
                              {d.node_id}:{" "}
                            </span>
                          )}
                          {d.message}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>

        <aside
          className="w-96 shrink-0 overflow-y-auto border-l border-line bg-panel"
          aria-label="Node settings"
        >
          {selected ? (
            <>
              <Inspector
                key={selected.id}
                node={selected}
                diagnostics={diagIndex.byNode.get(selected.id) ?? []}
                prompts={props.prompts}
                runtimes={props.runtimes}
                idTaken={(id) => state.nodes.some((n) => n.id === id)}
                onChange={(patch) =>
                  dispatch({ type: "updateNode", id: selected.id, patch })
                }
                onRename={(to) =>
                  dispatch({ type: "renameNode", id: selected.id, to })
                }
                onDelete={() =>
                  dispatch({
                    type: "nodeChanges",
                    changes: [{ type: "remove", id: selected.id }],
                  })
                }
              />
              {props.nodeActions && (
                <div className="border-t border-line p-3">
                  {props.nodeActions(selected, flow)}
                </div>
              )}
            </>
          ) : (
            <div className="space-y-3 p-3 text-xs text-muted">
              <div className="text-sm font-medium text-fg">{flow.id}</div>
              <Field label="Subscribed sensors">
                <div className="flex flex-wrap gap-1">
                  {flow.subscribe.length === 0 && (
                    <span>None yet. Drag a sensor from the palette.</span>
                  )}
                  {flow.subscribe.map((s) => (
                    <Badge key={s} tone={knownSensors.has(s) ? "ok" : "bad"}>
                      {s}
                    </Badge>
                  ))}
                </div>
              </Field>
              {diagIndex.flow.length > 0 && (
                <ul className="space-y-1">
                  {diagIndex.flow.map((d) => (
                    <li
                      key={d.message}
                      className={
                        d.severity === "error" ? "text-bad" : "text-warn"
                      }
                    >
                      {d.message}
                    </li>
                  ))}
                </ul>
              )}
              <p>
                Select a node to edit it. Drag from a node's output port to
                another node to connect. Press <Kbd>Backspace</Kbd> to delete
                the selection.
              </p>
            </div>
          )}
        </aside>
      </div>
    </div>
  );
}

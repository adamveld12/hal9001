import { useEffect, useState, type ReactNode } from "react";
import { ApiError, errorMessage } from "../../api/client";
import {
  useDeleteSensor,
  usePollSensor,
  useSensor,
  useUpdateSensor,
  useValidateSensor,
  type SensorDetail,
} from "../../api/sensors";
import { CodeEditor } from "../../components/CodeEditor";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  Panel,
} from "../../components/ui";
import { ConfirmButton } from "../files/ConfirmButton";
import { ConflictBanner } from "../files/ConflictBanner";
import {
  conflictHash,
  diagnosticsOf,
  hasErrors,
  isConflict,
  visibleDiagnostics,
  type Checked,
} from "../files/diagnostics";
import { DiagnosticsList } from "../files/DiagnosticsList";
import { useEditState } from "../files/useEditState";
import { SensorRuns } from "./SensorRuns";
import { pollErrorMessage, scheduleLabel, sensorStateTone } from "./view";

function Editor({
  detail,
  onDirtyChange,
  onDeleted,
}: {
  detail: SensorDetail;
  onDirtyChange: (dirty: boolean) => void;
  onDeleted: () => void;
}) {
  const id = detail.id;
  const { state, dispatch, dirty } = useEditState({
    hash: detail.hash,
    text: detail.yaml,
  });
  const update = useUpdateSensor();
  const validate = useValidateSensor();
  const del = useDeleteSensor();
  const poll = usePollSensor();
  const sensor = useSensor(id);
  const [checked, setChecked] = useState<Checked | null>(null);
  const [expandedRun, setExpandedRun] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);

  const shown = visibleDiagnostics(
    state.draft,
    { text: detail.yaml, diagnostics: detail.diagnostics },
    checked,
  );
  const busy = update.isPending || del.isPending;

  const save = (expectedHash: string) => {
    const text = state.draft;
    setNotice(null);
    update.mutate(
      { id, expectedHash, yaml: text },
      {
        onSuccess: (file) => {
          dispatch({
            type: "saved",
            submitted: text,
            file: { hash: file.hash, text: file.yaml },
          });
          setChecked(null);
          setNotice("Saved.");
        },
        onError: (e) => {
          if (isConflict(e)) {
            dispatch({ type: "conflict", currentHash: conflictHash(e) });
            return;
          }
          const ds = diagnosticsOf(e);
          if (ds) setChecked({ text, diagnostics: ds });
        },
      },
    );
  };

  /** Re-read the file to learn its current hash, keep the draft, and save it over. */
  const overwrite = async () => {
    const fresh = await sensor.refetch();
    if (!fresh.data) return;
    dispatch({
      type: "rebase",
      file: { hash: fresh.data.hash, text: fresh.data.yaml },
    });
    save(fresh.data.hash);
  };

  const remove = () =>
    del.mutate(
      { id, expectedHash: state.base.hash },
      {
        onSuccess: onDeleted,
        onError: (e) => {
          if (isConflict(e)) {
            dispatch({ type: "conflict", currentHash: conflictHash(e) });
          }
        },
      },
    );

  const status = detail.status;
  const saveError =
    update.error && !isConflict(update.error) && !diagnosticsOf(update.error)
      ? errorMessage(update.error)
      : null;
  const deleteError =
    del.error && !isConflict(del.error) ? errorMessage(del.error) : null;

  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="m-0 font-mono text-sm font-semibold">{id}</h2>
        <Badge tone={sensorStateTone(status.state)}>{status.state}</Badge>
        <span className="text-xs text-muted">{detail.path}</span>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <Button
            icon="play"
            disabled={poll.isPending || status.state === "invalid"}
            onClick={() =>
              poll.mutate(id, { onSuccess: (run) => setExpandedRun(run.id) })
            }
          >
            {poll.isPending ? "Starting…" : "Poll now"}
          </Button>
          <ConfirmButton
            label="Delete"
            confirmLabel="Delete sensor"
            disabled={busy}
            onConfirm={remove}
          />
        </div>
      </div>

      <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs">
        <Meta label="Schedule" value={scheduleLabel(detail.sensor)} />
        <Meta
          label="Last success"
          value={<Time ts={status.last_success_at} />}
        />
        <Meta label="Next poll" value={<Time ts={status.next_poll_at} />} />
        <Meta label="New items (24h)" value={status.new_items_24h} />
      </dl>

      {poll.isError && (
        <div role="alert" className="text-xs text-bad">
          {pollErrorMessage(poll.error)}
        </div>
      )}

      {state.conflict && (
        <ConflictBanner
          conflict={state.conflict}
          noun="sensor"
          busy={busy || sensor.isFetching}
          onReload={() => {
            void sensor.refetch().then((r) => {
              if (r.data) {
                dispatch({
                  type: "reload",
                  file: { hash: r.data.hash, text: r.data.yaml },
                });
                setChecked(null);
                setNotice(null);
              }
            });
          }}
          onOverwrite={() => void overwrite()}
        />
      )}

      <Panel
        title="Definition"
        actions={
          <>
            {notice && !dirty && <span className="text-ok">{notice}</span>}
            {dirty && <Badge tone="warn">unsaved</Badge>}
            <Button
              disabled={validate.isPending}
              onClick={() => {
                const text = state.draft;
                validate.mutate(text, {
                  onSuccess: (r) =>
                    setChecked({ text, diagnostics: r.diagnostics }),
                });
              }}
            >
              {validate.isPending ? "Validating…" : "Validate"}
            </Button>
            <Button
              variant="primary"
              icon="save"
              disabled={!dirty || busy || state.conflict !== null}
              onClick={() => save(state.base.hash)}
            >
              {update.isPending ? "Saving…" : "Save"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <CodeEditor
            lang="yaml"
            label={`Sensor ${id} YAML`}
            value={state.draft}
            onChange={(v) => dispatch({ type: "edit", draft: v })}
            invalid={!shown.stale && hasErrors(shown.diagnostics)}
            minHeight="12rem"
            maxHeight="32rem"
          />
          {saveError && (
            <div role="alert" className="text-xs text-bad">
              {saveError}
            </div>
          )}
          {deleteError && (
            <div role="alert" className="text-xs text-bad">
              {deleteError}
            </div>
          )}
          {validate.isError && (
            <div role="alert" className="text-xs text-bad">
              {errorMessage(validate.error)}
            </div>
          )}
          <DiagnosticsList
            diagnostics={shown.diagnostics}
            source={shown.stale ? undefined : state.draft}
            stale={shown.stale}
            emptyText={
              shown.origin === "checked"
                ? "Valid: no problems found."
                : undefined
            }
          />
        </div>
      </Panel>

      <Panel title="Recent polls" className="overflow-hidden">
        <div className="-m-3">
          <SensorRuns
            sensorId={id}
            expandedId={expandedRun}
            onToggle={(r) => setExpandedRun((cur) => (cur === r ? null : r))}
          />
        </div>
      </Panel>
    </div>
  );
}

function Meta({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex gap-1.5">
      <dt className="text-muted">{label}</dt>
      <dd className="m-0">{value}</dd>
    </div>
  );
}

/** Loads one sensor and renders its editor, with loading, not-found and error states. */
export function SensorEditorPane({
  id,
  onDirtyChange,
  onDeleted,
}: {
  id: string;
  onDirtyChange: (dirty: boolean) => void;
  onDeleted: () => void;
}) {
  const sensor = useSensor(id);
  if (sensor.isPending) return <LoadingState label="Loading sensor" />;
  if (sensor.isError) {
    if (sensor.error instanceof ApiError && sensor.error.status === 404) {
      return (
        <EmptyState
          title="Sensor not found"
          hint={`There is no sensors/${id}.yaml.`}
        />
      );
    }
    return (
      <ErrorState error={sensor.error} onRetry={() => void sensor.refetch()} />
    );
  }
  return (
    <Editor
      key={id}
      detail={sensor.data}
      onDirtyChange={onDirtyChange}
      onDeleted={onDeleted}
    />
  );
}

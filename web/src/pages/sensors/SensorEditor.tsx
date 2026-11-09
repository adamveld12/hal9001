import { useEffect, useState, type ReactNode } from "react";
import { InlineEdit } from "../../components/InlineEdit";
import { useRenameSensor } from "../../api/rename";
import { validateName } from "../../lib/inlineEdit";
import { withDescription } from "../files/description";
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
import {
  MenuItem,
  OverflowMenu,
  RunNowButton,
} from "../../components/RunNowButton";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  Panel,
} from "../../components/ui";
import { useAssistPage } from "../assist/context";
import { DraftBanners } from "../assist/DraftBanners";
import { pickChecked } from "../assist/draftSync";
import { useBoundSession, useDraftSync } from "../assist/useDraftSync";
import { useTestDraft } from "../assist/useTestDraft";
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
import { PauseToggle } from "./PauseToggle";
import { SensorEnvSection } from "./SensorEnvSection";
import { SensorRuns } from "./SensorRuns";
import { ResetDialog, RunNowResult, type RunNowOutcome } from "./SensorRunNow";
import {
  UNSCHEDULED_TITLE,
  pausedTitle,
  pollErrorMessage,
  scheduleLabel,
  sensorStateTone,
  unscheduledLabel,
} from "./view";

function Editor({
  detail,
  onDirtyChange,
  onDeleted,
  onRenamed,
  otherIds,
}: {
  detail: SensorDetail;
  onDirtyChange: (dirty: boolean) => void;
  onDeleted: () => void;
  onRenamed: (newId: string) => void;
  otherIds: readonly string[];
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
  const rename = useRenameSensor();
  const sensor = useSensor(id);
  const [checked, setChecked] = useState<Checked | null>(null);
  const [expandedRun, setExpandedRun] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<RunNowOutcome | null>(null);
  const [resetOpen, setResetOpen] = useState(false);

  // AI assistant: while a session for this sensor is open the editor shows and feeds its draft.
  const assist = useAssistPage();
  const bound = useBoundSession(id);
  const sync = useDraftSync({
    sessionId: bound.sessionId,
    text: state.draft,
    setText: (v) => dispatch({ type: "edit", draft: v }),
    dirty,
  });
  const test = useTestDraft(bound.sessionId);
  const assistChecked: Checked | null = sync.draft
    ? { text: sync.draft.yaml, diagnostics: sync.draft.diagnostics }
    : null;

  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);

  const shown = visibleDiagnostics(
    state.draft,
    { text: detail.yaml, diagnostics: detail.diagnostics },
    pickChecked(state.draft, checked, assistChecked),
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
        <InlineEdit
          mono
          label="Sensor name"
          value={id}
          validate={(v) => validateName(v, id, otherIds, "sensor")}
          disabledReason={
            dirty ? "Save or discard your edits before renaming" : null
          }
          onSave={(newId) =>
            rename.mutateAsync(
              { id, newId, expectedHash: state.base.hash },
              { onSuccess: () => onRenamed(newId) },
            )
          }
        />
        <span title={pausedTitle(status) ?? undefined}>
          <Badge tone={sensorStateTone(status.state)}>{status.state}</Badge>
        </span>
        <span className="text-xs text-muted">{detail.path}</span>
        <div className="ml-auto flex flex-wrap items-start gap-2">
          <PauseToggle
            sensorId={id}
            paused={status.paused}
            disabled={status.state === "invalid"}
          />
          <RunNowButton
            storageKey={id}
            pending={poll.isPending}
            disabled={status.state === "invalid"}
            title="Run this sensor once now and send new items to its flows"
            onRun={(rerunSeen) => {
              setOutcome(null);
              poll.mutate(
                { id, rerunSeen },
                { onSuccess: (run) => setOutcome({ run }) },
              );
            }}
          />
          <OverflowMenu>
            {(close) => (
              <MenuItem
                danger
                onClick={() => {
                  close();
                  setResetOpen(true);
                }}
              >
                Reset dedup state…
              </MenuItem>
            )}
          </OverflowMenu>
          <ConfirmButton
            label="Delete"
            confirmLabel="Delete sensor"
            disabled={busy}
            onConfirm={remove}
          />
        </div>
      </div>

      <InlineEdit
        muted
        label="Sensor description"
        placeholder="Add a description"
        value={detail.sensor?.description ?? ""}
        validate={() => null}
        onSave={(text) => {
          const next = withDescription(dirty ? state.draft : detail.yaml, text);
          if (dirty) {
            dispatch({ type: "edit", draft: next });
            return Promise.resolve();
          }
          return update.mutateAsync({
            id,
            expectedHash: state.base.hash,
            yaml: next,
          });
        }}
      />

      <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs">
        <Meta
          label="Schedule"
          value={status.manual ? "manual" : scheduleLabel(detail.sensor)}
        />
        <Meta
          label="Last success"
          value={<Time ts={status.last_success_at} />}
        />
        {!status.manual && (
          <Meta
            label="Next poll"
            value={
              unscheduledLabel(status.state, status.next_poll_at) ? (
                <span className="text-muted" title={UNSCHEDULED_TITLE}>
                  not scheduled
                </span>
              ) : (
                <Time ts={status.next_poll_at} />
              )
            }
          />
        )}
        <Meta label="New items (24h)" value={status.new_items_24h} />
        {status.reset_at && (
          <Meta
            label="Reset"
            value={
              <>
                <Time ts={status.reset_at} />
                {status.reset_by ? ` by ${status.reset_by}` : ""}
              </>
            }
          />
        )}
      </dl>

      {status.paused && (
        <div
          role="status"
          className="rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn"
        >
          Paused
          {status.paused_by ? ` by ${status.paused_by}` : ""}
          {status.paused_at ? (
            <>
              {" "}
              <Time ts={status.paused_at} />
            </>
          ) : null}
          . The scheduler does not poll this sensor; Run now and event injection
          still work.
        </div>
      )}

      {poll.isError && (
        <div role="alert" className="text-xs text-bad">
          {pollErrorMessage(poll.error)}
        </div>
      )}

      {outcome && (
        <RunNowResult outcome={outcome} onClose={() => setOutcome(null)} />
      )}

      {resetOpen && (
        <ResetDialog
          sensorId={id}
          onClose={() => setResetOpen(false)}
          onDone={setOutcome}
        />
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
            {bound.sessionId !== null && (
              <Button
                icon="play"
                disabled={test.isPending}
                onClick={() => void test.run()}
              >
                {test.isPending ? "Testing…" : "Test draft"}
              </Button>
            )}
            <Button
              icon="agent"
              disabled={assist.param !== null}
              title="Chat with an AI agent that edits this sensor's draft"
              onClick={assist.open}
            >
              Ask AI
            </Button>
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
          <DraftBanners
            pending={sync.pending}
            markedLines={sync.marks}
            onTakeAi={sync.takeAi}
            onKeepMine={sync.keepMine}
            onClearMarks={sync.clearMarks}
          />
          <CodeEditor
            lang="yaml"
            label={`Sensor ${id} YAML`}
            value={state.draft}
            onChange={(v) => dispatch({ type: "edit", draft: v })}
            invalid={!shown.stale && hasErrors(shown.diagnostics)}
            markedLines={sync.marks}
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
          {test.error && (
            <div role="alert" className="text-xs text-bad">
              {test.error.message}
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

      <SensorEnvSection sensorId={id} />

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
  onRenamed,
  otherIds,
}: {
  id: string;
  onDirtyChange: (dirty: boolean) => void;
  onDeleted: () => void;
  onRenamed: (newId: string) => void;
  otherIds: readonly string[];
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
      onRenamed={onRenamed}
      otherIds={otherIds}
    />
  );
}

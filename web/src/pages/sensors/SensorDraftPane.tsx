import { useEffect, useState } from "react";
import { errorMessage } from "../../api/client";
import { useCreateSensor, useValidateSensor } from "../../api/sensors";
import { CodeEditor } from "../../components/CodeEditor";
import { Button, EmptyState, Panel } from "../../components/ui";
import { useAssistPage } from "../assist/context";
import { DraftBanners } from "../assist/DraftBanners";
import { pickChecked } from "../assist/draftSync";
import { useBoundSession, useDraftSync } from "../assist/useDraftSync";
import { useTestDraft } from "../assist/useTestDraft";
import {
  diagnosticsOf,
  hasErrors,
  visibleDiagnostics,
  type Checked,
} from "../files/diagnostics";
import { DiagnosticsList } from "../files/DiagnosticsList";

/** What a new sensor's editor holds before the agent writes anything. */
export const NEW_SENSOR_SKELETON = `# Describe the sensor to the AI assistant, or write it here.
id: my-sensor
interval: 5m
lang: bash
script: |
  echo '[]'
key: "{{.id}}"
first_run: baseline
`;

/**
 * Editor for a sensor that does not exist yet, shown for "Create with AI". The
 * text is the session's draft once there is one; Save creates the sensor file
 * from it and moves to the sensor's page, keeping the session.
 */
export function SensorDraftPane({
  onDirtyChange,
  onCreated,
}: {
  onDirtyChange: (dirty: boolean) => void;
  onCreated: (id: string, sessionId: string | null) => void;
}) {
  const page = useAssistPage();
  const bound = useBoundSession(null);
  const [text, setText] = useState(NEW_SENSOR_SKELETON);
  const [checked, setChecked] = useState<Checked | null>(null);
  const dirty = text !== NEW_SENSOR_SKELETON;
  const sync = useDraftSync({
    sessionId: bound.sessionId,
    text,
    setText,
    dirty,
  });
  const create = useCreateSensor();
  const validate = useValidateSensor();
  const test = useTestDraft(bound.sessionId);

  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);

  const assistChecked: Checked | null = sync.draft
    ? { text: sync.draft.yaml, diagnostics: sync.draft.diagnostics }
    : null;
  const shown = visibleDiagnostics(
    text,
    assistChecked ?? { text: "", diagnostics: [] },
    pickChecked(text, checked),
  );
  const createDiagnostics = diagnosticsOf(create.error);
  const otherError =
    create.error && !createDiagnostics ? errorMessage(create.error) : null;

  if (bound.otherTarget !== null) {
    return (
      <EmptyState
        title="Opening the sensor"
        hint="This session belongs to an existing sensor."
      />
    );
  }

  return (
    <div className="space-y-4 p-4">
      <Panel
        title={
          <>
            New sensor
            <span className="font-normal text-muted">draft, not saved yet</span>
          </>
        }
        actions={
          <>
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
              disabled={validate.isPending}
              onClick={() => {
                const t = text;
                validate.mutate(t, {
                  onSuccess: (r) =>
                    setChecked({ text: t, diagnostics: r.diagnostics }),
                });
              }}
            >
              {validate.isPending ? "Validating…" : "Validate"}
            </Button>
            <Button
              variant="primary"
              icon="save"
              disabled={create.isPending || text.trim() === ""}
              onClick={() => {
                const t = text;
                create.mutate(t, {
                  onSuccess: (d) => onCreated(d.id, page.sessionId),
                  onError: (e) => {
                    const ds = diagnosticsOf(e);
                    if (ds) setChecked({ text: t, diagnostics: ds });
                  },
                });
              }}
            >
              {create.isPending ? "Creating…" : "Save"}
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
            label="New sensor YAML"
            value={text}
            onChange={setText}
            invalid={!shown.stale && hasErrors(shown.diagnostics)}
            markedLines={sync.marks}
            minHeight="14rem"
            maxHeight="36rem"
          />
          {otherError && (
            <div role="alert" className="text-xs text-bad">
              {otherError}
            </div>
          )}
          {test.error && (
            <div role="alert" className="text-xs text-bad">
              {test.error.message}
            </div>
          )}
          <DiagnosticsList
            diagnostics={shown.diagnostics}
            source={shown.stale ? undefined : text}
            stale={shown.stale}
            emptyText={
              !shown.stale && (shown.origin === "checked" || sync.draft)
                ? "Valid: no problems found."
                : undefined
            }
          />
        </div>
      </Panel>
    </div>
  );
}

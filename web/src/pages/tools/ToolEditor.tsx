import { InlineEdit } from "../../components/InlineEdit";
import { useRenameTool } from "../../api/rename";
import { useTools } from "../../api/tools";
import { validateName } from "../../lib/inlineEdit";
import { withDescription } from "../files/description";
import { useEffect, useState } from "react";
import { ApiError, errorMessage } from "../../api/client";
import {
  useDeleteTool,
  useTool,
  useUpdateTool,
  type ToolDetail,
} from "../../api/tools";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  Panel,
} from "../../components/ui";
import { usePageContext } from "../../layout/pageContext";
import { useAssist } from "../assist/context";
import { DraftBanners } from "../assist/DraftBanners";
import { toolContext } from "../assist/pageContexts";
import { useBoundSession, useDraftSync } from "../assist/useDraftSync";
import { useTestDraft } from "../assist/useTestDraft";
import { ConfirmButton } from "../files/ConfirmButton";
import { ConflictBanner } from "../files/ConflictBanner";
import { conflictHash, diagnosticsOf, isConflict } from "../files/diagnostics";
import { useEditState } from "../files/useEditState";
import { ToolDefinition } from "./ToolDefinition";
import { ToolEnvSection } from "./ToolEnvSection";
import { ToolResultView } from "./ToolResultView";
import { ToolTestPanel } from "./ToolTestPanel";

function Editor({
  detail,
  onDirtyChange,
  onDeleted,
  onRenamed,
}: {
  detail: ToolDetail;
  onDirtyChange: (dirty: boolean) => void;
  onDeleted: () => void;
  onRenamed: (newId: string) => void;
}) {
  const id = detail.id;
  const rename = useRenameTool();
  const otherIds = (useTools().data?.items ?? [])
    .map((x) => x.id)
    .filter((x) => x !== id);
  const { state, dispatch, dirty } = useEditState({
    hash: detail.hash,
    text: detail.yaml,
  });
  const update = useUpdateTool();
  const del = useDeleteTool();
  const tool = useTool(id);
  const [notice, setNotice] = useState<string | null>(null);

  const assist = useAssist();
  const bound = useBoundSession("tool", id);
  const sync = useDraftSync({
    kind: "tool",
    targetId: id,
    sessionId: bound.sessionId,
    text: state.draft,
    setText: (v) => dispatch({ type: "edit", draft: v }),
    dirty,
  });
  const draftTest = useTestDraft(bound.sessionId);

  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);
  const busy = update.isPending || del.isPending;
  usePageContext(
    toolContext({
      id,
      description: detail.tool?.description,
      paramNames: Object.keys(detail.tool?.params ?? {}),
      approve: detail.tool?.approve,
      dirty,
      diagnostics: sync.draft?.diagnostics ?? detail.diagnostics,
      drafting: bound.sessionId !== null,
    }),
  );

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
          setNotice("Saved.");
        },
        onError: (e) => {
          if (isConflict(e))
            dispatch({ type: "conflict", currentHash: conflictHash(e) });
        },
      },
    );
  };
  const overwrite = async () => {
    const fresh = await tool.refetch();
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
          if (isConflict(e))
            dispatch({ type: "conflict", currentHash: conflictHash(e) });
        },
      },
    );

  const saveDiagnostics = diagnosticsOf(update.error);
  const saveError =
    update.error && !isConflict(update.error) && !saveDiagnostics
      ? errorMessage(update.error)
      : null;
  const t = detail.tool;

  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <InlineEdit
          mono
          label="Tool name"
          value={id}
          validate={(v) => validateName(v, id, otherIds, "tool")}
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
        {t && <Badge tone="muted">{t.lang}</Badge>}
        {t && (
          <Badge tone={t.approve === "never" ? "ok" : "warn"}>
            {t.approve === "never"
              ? "no approval"
              : typeof t.approve === "object"
                ? "approval by rules"
                : "approval"}
          </Badge>
        )}
        <span className="text-xs text-muted">{detail.path}</span>
        <div className="ml-auto">
          <ConfirmButton
            label="Delete"
            confirmLabel="Delete tool"
            disabled={busy}
            onConfirm={remove}
          />
        </div>
      </div>

      <InlineEdit
        muted
        label="Tool description"
        placeholder="Add a description"
        value={detail.tool?.description ?? ""}
        validate={(v) => (v === "" ? "The agent needs a description." : null)}
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

      {state.conflict && (
        <ConflictBanner
          conflict={state.conflict}
          noun="tool"
          busy={busy || tool.isFetching}
          onReload={() => {
            void tool.refetch().then((r) => {
              if (r.data) {
                dispatch({
                  type: "reload",
                  file: { hash: r.data.hash, text: r.data.yaml },
                });
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
                disabled={draftTest.isPending}
                onClick={() => void draftTest.run()}
              >
                {draftTest.isPending ? "Testing…" : "Test draft"}
              </Button>
            )}
            <Button
              icon="agent"
              disabled={
                assist.open &&
                assist.target.kind === "tool" &&
                assist.target.id === id
              }
              title="Chat with an AI agent that edits this tool's draft"
              onClick={() => assist.openTarget({ kind: "tool", id })}
            >
              Ask AI
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
          <ToolDefinition
            text={state.draft}
            setText={(v) => dispatch({ type: "edit", draft: v })}
            idLocked
            label={`Tool ${id} YAML`}
            markedLines={sync.marks}
          />
          {saveError && (
            <div role="alert" className="text-xs text-bad">
              {saveError}
            </div>
          )}
          {draftTest.error && (
            <div role="alert" className="text-xs text-bad">
              {draftTest.error.message}
            </div>
          )}
          {draftTest.result?.kind === "tool" && (
            <ToolResultView result={draftTest.result} />
          )}
        </div>
      </Panel>

      <Panel title="Test">
        <ToolTestPanel
          key={detail.hash}
          toolId={id}
          schema={detail.schema}
          disabled={dirty || !t}
        />
      </Panel>

      <ToolEnvSection toolId={id} />
    </div>
  );
}

/** Loads one tool and renders its editor, with loading, not-found and error states. */
export function ToolEditorPane({
  id,
  onDirtyChange,
  onDeleted,
  onRenamed,
}: {
  id: string;
  onDirtyChange: (dirty: boolean) => void;
  onDeleted: () => void;
  onRenamed: (newId: string) => void;
}) {
  const tool = useTool(id);
  if (tool.isPending) return <LoadingState label="Loading tool" />;
  if (tool.isError) {
    if (tool.error instanceof ApiError && tool.error.status === 404) {
      return (
        <EmptyState
          title="Tool not found"
          hint={`There is no tools/${id}.yaml.`}
        />
      );
    }
    return (
      <ErrorState error={tool.error} onRetry={() => void tool.refetch()} />
    );
  }
  return (
    <Editor
      key={id}
      detail={tool.data}
      onDirtyChange={onDirtyChange}
      onDeleted={onDeleted}
      onRenamed={onRenamed}
    />
  );
}

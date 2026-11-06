import { useEffect, useState } from "react";
import { errorMessage } from "../../api/client";
import { useCreateTool } from "../../api/tools";
import { Button, EmptyState, Panel } from "../../components/ui";
import { useAssistPage } from "../assist/context";
import { DraftBanners } from "../assist/DraftBanners";
import { useBoundSession, useDraftSync } from "../assist/useDraftSync";
import { useTestDraft } from "../assist/useTestDraft";
import { ToolDefinition } from "./ToolDefinition";
import { ToolResultView } from "./ToolResultView";
import { NEW_TOOL_YAML } from "./toolModel";

/** Editor of a tool that does not exist yet: "New tool" and "Create with AI". Save creates the file. */
export function ToolDraftPane({
  onDirtyChange,
  onCreated,
}: {
  onDirtyChange: (dirty: boolean) => void;
  onCreated: (id: string, sessionId: string | null) => void;
}) {
  const page = useAssistPage();
  const bound = useBoundSession(null);
  const [text, setText] = useState(NEW_TOOL_YAML);
  const dirty = text !== NEW_TOOL_YAML;
  const sync = useDraftSync({
    sessionId: bound.sessionId,
    text,
    setText,
    dirty,
  });
  const create = useCreateTool();
  const test = useTestDraft(bound.sessionId);

  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);

  if (bound.otherTarget !== null) {
    return (
      <EmptyState
        title="Opening the tool"
        hint="This session belongs to an existing tool."
      />
    );
  }
  return (
    <div className="space-y-4 p-4">
      <Panel
        title={
          <>
            New tool{" "}
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
              variant="primary"
              icon="save"
              disabled={create.isPending || text.trim() === ""}
              onClick={() =>
                create.mutate(text, {
                  onSuccess: (d) => onCreated(d.id, page.sessionId),
                })
              }
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
          <ToolDefinition
            text={text}
            setText={setText}
            idLocked={false}
            label="New tool YAML"
            markedLines={sync.marks}
          />
          {create.isError && (
            <div role="alert" className="text-xs text-bad">
              {errorMessage(create.error)}
            </div>
          )}
          {test.error && (
            <div role="alert" className="text-xs text-bad">
              {test.error.message}
            </div>
          )}
          {test.result?.kind === "tool" && (
            <ToolResultView result={test.result} />
          )}
        </div>
      </Panel>
    </div>
  );
}

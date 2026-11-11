import { useState } from "react";
import type { AssistTargetKind } from "../../api/assist";
import { Button, TextInput } from "../../components/ui";
import { ToolResultView } from "../tools/ToolResultView";
import { TestResultView } from "./TestResultView";
import { useTestDraft } from "./useTestDraft";

/** "Test draft" as a card at the end of the conversation; the result opens under the button. */
export function TestPanel({
  sessionId,
  kind,
}: {
  sessionId: string;
  kind: AssistTargetKind;
}) {
  const test = useTestDraft(sessionId);
  const isTool = kind === "tool";
  const [argsText, setArgsText] = useState("{}");
  let args: Record<string, unknown> | undefined;
  try {
    const v: unknown = JSON.parse(argsText);
    if (typeof v === "object" && v !== null && !Array.isArray(v)) {
      args = v as Record<string, unknown>;
    }
  } catch {
    // reported below
  }
  return (
    <div className="space-y-2 rounded-md border border-line bg-panel p-2 text-xs">
      <div className="flex items-center gap-2">
        <Button
          icon="play"
          className="shrink-0 whitespace-nowrap"
          disabled={test.isPending || (isTool && args === undefined)}
          onClick={() => void test.run(isTool ? { args } : undefined)}
        >
          {test.isPending ? "Testing…" : "Test draft"}
        </Button>
        <span className="text-[11px] text-muted">
          {isTool
            ? "Calls the draft once with these arguments. Nothing is recorded."
            : "Runs the draft like Poll now. No run, events or dedup keys are recorded."}
        </span>
      </div>
      {isTool && (
        <TextInput
          aria-label="Test arguments (JSON)"
          className="font-mono"
          value={argsText}
          invalid={args === undefined}
          onChange={(e) => setArgsText(e.target.value)}
        />
      )}
      {test.error && (
        <div role="alert" className="text-bad">
          {test.error.message}
        </div>
      )}
      {test.result?.kind === "tool" && (
        <details open className="rounded-md border border-line">
          <summary className="cursor-pointer select-none px-2 py-1.5 font-medium">
            Last test
          </summary>
          <div className="border-t border-line p-2">
            <ToolResultView result={test.result} />
          </div>
        </details>
      )}
      {test.result?.kind === "sensor" && (
        <details open className="rounded-md border border-line">
          <summary className="cursor-pointer select-none px-2 py-1.5 font-medium">
            Last test
          </summary>
          <div className="border-t border-line p-2">
            <TestResultView result={test.result} />
          </div>
        </details>
      )}
    </div>
  );
}

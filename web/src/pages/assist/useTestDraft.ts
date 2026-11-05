import { useAssistTestResult, useTestAssistDraft } from "../../api/assist";
import { useAssistPage } from "./context";

/** Run the session's draft as a test: pending edits are pushed first so the run sees the editor's text. */
export function useTestDraft(sessionId: string | null) {
  const page = useAssistPage();
  const test = useTestAssistDraft(sessionId);
  const result = useAssistTestResult(sessionId);
  const { mutate } = test;
  return {
    run: async () => {
      await page.editor()?.flush();
      mutate();
    },
    isPending: test.isPending,
    error: test.error,
    result: result.data,
  };
}

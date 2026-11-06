import {
  useAssistTestResult,
  useTestAssistDraft,
  type AssistTestRequest,
} from "../../api/assist";
import { useAssistPage } from "./context";

/**
 * Run the session's draft as a test: pending edits are pushed first so the run
 * sees the editor's content. `body` picks the input of a flow test.
 */
export function useTestDraft(sessionId: string | null) {
  const page = useAssistPage();
  const test = useTestAssistDraft(sessionId);
  const result = useAssistTestResult(sessionId);
  const { mutate, reset } = test;
  return {
    run: async (body?: AssistTestRequest) => {
      await page.editor()?.flush();
      mutate(body);
    },
    reset,
    isPending: test.isPending,
    error: test.error,
    result: result.data,
  };
}

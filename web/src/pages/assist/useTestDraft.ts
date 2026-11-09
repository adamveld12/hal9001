import {
  useAssistSensorTestResult,
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

/**
 * Run one draft sensor of a flow session (the "Test sensor" of its card). The
 * agent alone writes sensor drafts, so there is nothing to push first. Its
 * result is kept per sensor and does not replace the flow test's.
 */
export function useTestDraftSensor(sessionId: string | null, sensorId: string) {
  const test = useTestAssistDraft(sessionId);
  const result = useAssistSensorTestResult(sessionId, sensorId);
  const { mutate } = test;
  return {
    run: () => mutate({ sensor: sensorId }),
    isPending: test.isPending,
    error: test.error,
    result: result.data,
  };
}

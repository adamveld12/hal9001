import {
  skipToken,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api, unwrap, unwrapVoid, type Schemas } from "./client";
import { qk } from "./keys";

export type AssistSession = Schemas["AssistSession"];
export type AssistSessionDetail = Schemas["AssistSessionDetail"];
export type AssistMessage = Schemas["AssistMessage"];
/** An `assist.message` event's data: the message and its session. */
export type AssistStreamMessage = Schemas["AssistStreamMessage"];
export type AssistDraft = Schemas["AssistDraft"];
export type AssistStatus = Schemas["AssistStatus"];
export type AssistTestResult = Schemas["AssistTestResult"];
export type AssistSensorTestResult = Schemas["AssistSensorTestResult"];
export type AssistFlowTestResult = Schemas["AssistFlowTestResult"];
export type AssistFlowTestNode = Schemas["AssistFlowTestNode"];
export type AssistTestRequest = Schemas["AssistTestRequest"];
export type AssistTargetKind = Schemas["AssistTargetKind"];
export type AssistSample = Schemas["AssistSample"];

/** Sessions that edit one target of `kind` (`targetId` set) or all sessions of that kind; closed ones are not listed. */
export function useAssistSessions(
  kind: AssistTargetKind,
  targetId: string | null | undefined,
) {
  return useQuery({
    queryKey: qk("assist-sessions", kind, targetId ?? null),
    queryFn: () =>
      unwrap(
        api.GET("/api/assist/sessions", {
          params: {
            query: {
              target_kind: kind,
              target_id: targetId ?? undefined,
            },
          },
        }),
      ),
    select: (d) => d.items,
  });
}

const sessionKey = (id: string) => qk("assist-session", id);

/** One session with its conversation. Kept current by the stream (pages/assist/useAssistLive.ts). */
export function useAssistSession(id: string | null) {
  return useQuery({
    queryKey: sessionKey(id ?? ""),
    queryFn:
      id === null
        ? skipToken
        : () =>
            unwrap(
              api.GET("/api/assist/sessions/{id}", {
                params: { path: { id } },
              }),
            ),
  });
}

export function useCreateAssistSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: Schemas["AssistCreateRequest"]) =>
      unwrap(api.POST("/api/assist/sessions", { body: req })),
    onSuccess: (session) => {
      qc.setQueryData<AssistSessionDetail>(sessionKey(session.id), {
        ...session,
        messages: [],
      });
      void qc.invalidateQueries({ queryKey: qk("assist-sessions") });
    },
  });
}

/** Sends the user's message and starts a turn. `yaml` first replaces the draft with the editor's text. */
export function useSendAssistMessage() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      ...req
    }: Schemas["AssistSendRequest"] & { id: string }) =>
      unwrap(
        api.POST("/api/assist/sessions/{id}/messages", {
          params: { path: { id } },
          body: req,
        }),
      ),
    onSettled: (_data, _err, { id }) =>
      qc.invalidateQueries({ queryKey: sessionKey(id) }),
  });
}

/** Replaces the draft with the editor's text; 409 `conflict` when `expected_hash` is not the current draft. */
export function usePutAssistDraft(id: string | null) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: Schemas["AssistDraftRequest"]) =>
      unwrap(
        api.PUT("/api/assist/sessions/{id}/draft", {
          params: { path: { id: id as string } },
          body: req,
        }),
      ),
    onSuccess: (draft) =>
      qc.setQueryData<AssistSessionDetail>(sessionKey(id ?? ""), (d) =>
        d ? { ...d, draft } : d,
      ),
  });
}

const testKey = (id: string) => qk("assist-test", id);

/** A draft sensor's test result in a flow session; kept apart from the session's latest test, so it never replaces the flow test's result (the canvas overlay). */
const sensorTestKey = (id: string, sensorId: string) =>
  qk("assist-test", id, "sensor", sensorId);

/**
 * Runs the draft once: a sensor like Poll now, a flow on one input (`body`, see
 * `AssistTestRequest`). In a flow session `body.sensor` runs one of its draft
 * sensors; that result is kept for `useAssistSensorTestResult`, any other for
 * `useAssistTestResult`. Nothing is recorded as a live run.
 */
export function useTestAssistDraft(id: string | null) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body?: AssistTestRequest) =>
      unwrap(
        api.POST("/api/assist/sessions/{id}/test", {
          params: { path: { id: id as string } },
          body,
        }),
      ),
    onSuccess: (result, body) =>
      qc.setQueryData(
        body?.sensor === undefined
          ? testKey(id ?? "")
          : sensorTestKey(id ?? "", body.sensor),
        result,
      ),
  });
}

/** Sample inputs of a flow session (recent payloads of the known sensors), refreshed by the daemon every turn. */
export function useAssistSamples(id: string | null, enabled: boolean) {
  return useQuery({
    queryKey: qk("assist-test", id ?? "", "samples"),
    enabled: enabled && id !== null,
    queryFn: async () =>
      (
        await unwrap(
          api.GET("/api/assist/sessions/{id}/samples", {
            params: { path: { id: id as string } },
          }),
        )
      ).items,
  });
}

/** The latest test result of a session, from any component that ran it. */
export function useAssistTestResult(id: string | null) {
  return useQuery<AssistTestResult>({
    queryKey: testKey(id ?? ""),
    queryFn: skipToken,
    staleTime: Infinity,
    gcTime: Infinity,
  });
}

/** The latest test result of one draft sensor of a flow session ("Test sensor" on its card). */
export function useAssistSensorTestResult(id: string | null, sensorId: string) {
  return useQuery<AssistSensorTestResult>({
    queryKey: sensorTestKey(id ?? "", sensorId),
    queryFn: skipToken,
    staleTime: Infinity,
    gcTime: Infinity,
  });
}

export function useCancelAssistTurn(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/api/assist/sessions/{id}/cancel", {
          params: { path: { id } },
        }),
      ),
    onSuccess: (session) =>
      qc.setQueryData<AssistSessionDetail>(sessionKey(id), (d) =>
        d ? { ...session, messages: d.messages } : d,
      ),
  });
}

/** Ends the session: the agent stops and the draft is removed. */
export function useEndAssistSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      unwrapVoid(
        api.DELETE("/api/assist/sessions/{id}", { params: { path: { id } } }),
      ),
    onSuccess: (_void, id) => {
      qc.removeQueries({ queryKey: sessionKey(id) });
      qc.removeQueries({ queryKey: testKey(id) });
      void qc.invalidateQueries({ queryKey: qk("assist-sessions") });
    },
  });
}

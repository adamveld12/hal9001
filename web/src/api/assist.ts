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

/** Sessions that edit one sensor (`targetId` set) or all sensor sessions; closed ones are not listed. */
export function useAssistSessions(targetId: string | null | undefined) {
  return useQuery({
    queryKey: qk("assist-sessions", targetId ?? null),
    queryFn: () =>
      unwrap(
        api.GET("/api/assist/sessions", {
          params: {
            query: {
              target_kind: "sensor",
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

/** Runs the draft like Poll now, without recording a run, events or dedup keys. The result is kept for `useAssistTestResult`. */
export function useTestAssistDraft(id: string | null) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/api/assist/sessions/{id}/test", {
          params: { path: { id: id as string } },
        }),
      ),
    onSuccess: (result) => qc.setQueryData(testKey(id ?? ""), result),
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

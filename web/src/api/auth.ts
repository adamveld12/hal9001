import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, unwrapVoid, type Schemas } from "./client";
import { qk } from "./keys";

export type AuthService = Schemas["AuthService"];
export type AuthResult = Schemas["AuthResult"];
export type AuthSuccess = Schemas["AuthSuccess"];
export type AuthChallenge = Schemas["AuthChallenge"];
export type Session = Schemas["Session"];
export type AuthEvent = Schemas["AuthEvent"];
export type Passkey = Schemas["Passkey"];

// Login exchange. These are plain functions, not hooks: a login is a short
// imperative sequence (begin, maybe answer a challenge) that the caller drives.

/** `POST /api/auth/{service}/begin`. Throws ApiError `authentication-failed` on 401. */
export function beginAuth(
  service: AuthService,
  body: Schemas["AuthBeginRequest"] = {},
): Promise<AuthResult> {
  return unwrap(
    api.POST("/api/auth/{service}/begin", {
      params: { path: { service } },
      body,
    }),
  );
}

/** `POST /api/auth/{service}/continue`. Throws ApiError `login-expired` on 410. */
export function continueAuth(
  service: AuthService,
  body: Schemas["AuthContinueRequest"],
): Promise<AuthResult> {
  return unwrap(
    api.POST("/api/auth/{service}/continue", {
      params: { path: { service } },
      body,
    }),
  );
}

/** `POST /api/auth/webauthn/register/begin`. */
export function beginPasskeyRegistration(
  label?: string,
): Promise<Schemas["PasskeyRegisterBegin"]> {
  return unwrap(
    api.POST("/api/auth/webauthn/register/begin", {
      body: label ? { label } : {},
    }),
  );
}

/** `POST /api/auth/webauthn/register/finish`. */
export function finishPasskeyRegistration(
  body: Schemas["PasskeyRegisterFinishRequest"],
): Promise<Passkey> {
  return unwrap(api.POST("/api/auth/webauthn/register/finish", { body }));
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.POST("/api/auth/logout")),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("whoami") }),
  });
}

export function useAuthSessions() {
  return useQuery({
    queryKey: qk("auth-sessions"),
    queryFn: () => unwrap(api.GET("/api/auth/sessions")),
  });
}

export function useRevokeSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.DELETE("/api/auth/sessions/{id}", { params: { path: { id } } }),
      ),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: qk("auth-sessions") }),
        qc.invalidateQueries({ queryKey: qk("auth-events") }),
        // The revoked session may be this browser's own.
        qc.invalidateQueries({ queryKey: qk("whoami") }),
      ]),
  });
}

/** Revokes every session including the caller's own; the UI falls back to the login page. */
export function useRevokeAllSessions() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.DELETE("/api/auth/sessions")),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("whoami") }),
  });
}

export function usePasskeys() {
  return useQuery({
    queryKey: qk("passkeys"),
    queryFn: () => unwrap(api.GET("/api/auth/webauthn/credentials")),
  });
}

export function useDeletePasskey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      unwrapVoid(
        api.DELETE("/api/auth/webauthn/credentials/{id}", {
          params: { path: { id } },
        }),
      ),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: qk("passkeys") }),
        qc.invalidateQueries({ queryKey: qk("auth-events") }),
      ]),
  });
}

export function useAuthEvents(limit = 100) {
  return useQuery({
    queryKey: qk("auth-events", limit),
    queryFn: () =>
      unwrap(api.GET("/api/auth/events", { params: { query: { limit } } })),
  });
}

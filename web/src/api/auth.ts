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
export type AuthOverview = Schemas["AuthOverview"];
export type AuthMethod = Schemas["AuthMethod"];
export type AuthMethodId = Schemas["AuthMethodId"];
export type AuthCurrent = Schemas["AuthCurrent"];
export type PasswordStatus = Schemas["PasswordStatus"];
export type AuthChangeResult = Schemas["AuthChangeResult"];
export type AuthMethodsRequest = Schemas["AuthMethodsRequest"];
export type AuthPasswordSetRequest = Schemas["AuthPasswordSetRequest"];
export type AuthPasswordRemoveRequest = Schemas["AuthPasswordRemoveRequest"];
export type AuthAllowedLoginsRequest = Schemas["AuthAllowedLoginsRequest"];
export type AuthStacks = Schemas["AuthStacks"];
export type AuthStacksRequest = Schemas["AuthStacksRequest"];
export type AuthStacksPreview = Schemas["AuthStacksPreview"];
export type AuthModuleEntry = Schemas["AuthModuleEntry"];

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

// Sign-in configuration. Every change answers with the overview after it
// (`AuthChangeResult`); a 409 `confirmation-required` is repeated with
// `confirm: true` (see pages/settings/authProblem.ts).

/** `GET /api/auth/overview`: one row per way in, how this request was admitted, password status. Never the hash. */
export function useAuthOverview() {
  return useQuery({
    queryKey: qk("auth-overview"),
    queryFn: () => unwrap(api.GET("/api/auth/overview")),
  });
}

/** What a sign-in change touches: its own views, the session lists, the Tailscale status (`allowed_logins`) and the settings. */
function useAuthChange<V>(call: (vars: V) => Promise<AuthChangeResult>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: call,
    // Variables may hold a password: keep neither them nor the answer around once the call is done.
    gcTime: 0,
    onSuccess: (result) => {
      qc.setQueryData(qk("auth-overview"), result.overview);
      return Promise.all(
        (
          [
            "auth-overview",
            "auth-stacks",
            "auth-sessions",
            "auth-events",
            "passkeys",
            "tailscale",
            "settings",
            // A change may have ended this browser's own session.
            "whoami",
          ] as const
        ).map((root) => qc.invalidateQueries({ queryKey: qk(root) })),
      );
    },
  });
}

/** `PATCH /api/auth/methods`. 409 `confirmation-required` or `invalid-state` ("set a password first"), 422 diagnostics. */
export function useUpdateAuthMethods() {
  return useAuthChange<AuthMethodsRequest>((body) =>
    unwrap(api.PATCH("/api/auth/methods", { body })),
  );
}

/** `PUT /api/auth/password`. 422 with a `current_password` diagnostic, 429 `rate-limited`. */
export function useSetAuthPassword() {
  return useAuthChange<AuthPasswordSetRequest>((body) =>
    unwrap(api.PUT("/api/auth/password", { body })),
  );
}

/** `DELETE /api/auth/password`. Also forgets the password sign-in method; 409 `confirmation-required`, 422, 429. */
export function useRemoveAuthPassword() {
  return useAuthChange<AuthPasswordRemoveRequest>((body) =>
    unwrap(api.DELETE("/api/auth/password", { body })),
  );
}

/** `PUT /api/auth/allowed-logins`. 422 lists every bad login; 409 `confirmation-required`. */
export function useSetAllowedLogins() {
  return useAuthChange<AuthAllowedLoginsRequest>((body) =>
    unwrap(api.PUT("/api/auth/allowed-logins", { body })),
  );
}

/** `GET /api/auth/stacks`: `auth.stacks` and `auth.services` with secrets redacted, and the file hash to send back as `expected_hash`. */
export function useAuthStacks() {
  return useQuery({
    queryKey: qk("auth-stacks"),
    queryFn: () => unwrap(api.GET("/api/auth/stacks")),
  });
}

/** `POST /api/auth/stacks/preview`: validates and diffs, writes nothing. */
export function usePreviewAuthStacks() {
  return useMutation({
    mutationFn: ({ stacks, services }: AuthStacksRequest) =>
      unwrap(
        api.POST("/api/auth/stacks/preview", { body: { stacks, services } }),
      ),
  });
}

/** `PUT /api/auth/stacks`. 409 `conflict` (stale `expected_hash`) or `confirmation-required`, 422 diagnostics. */
export function useSetAuthStacks() {
  return useAuthChange<AuthStacksRequest>((body) =>
    unwrap(api.PUT("/api/auth/stacks", { body })),
  );
}

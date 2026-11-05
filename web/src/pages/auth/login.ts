import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  beginAuth,
  continueAuth,
  type AuthResult,
  type AuthSuccess,
} from "../../api/auth";
import { ApiError } from "../../api/client";
import { qk } from "../../api/keys";
import { methodsFrom } from "./loginErrors";
import {
  decodeRequestOptions,
  encodeAssertion,
  isAssertionStep,
  type AssertionLike,
} from "./webauthn";

/** The `ui` stack's login grants every service its stack granted (no step-up). */
const SERVICE = "ui";

function requireSuccess(r: AuthResult): AuthSuccess {
  if (r.status === "success") return r;
  throw new Error(`Unsupported sign-in step: ${r.step.kind}`);
}

export async function loginWithPassword(
  password: string,
): Promise<AuthSuccess> {
  return requireSuccess(
    await beginAuth(SERVICE, { method: "password", password }),
  );
}

export async function loginWithPasskey(): Promise<AuthSuccess> {
  const first = await beginAuth(SERVICE, { method: "webauthn" });
  if (first.status === "success") return first;
  if (!isAssertionStep(first.step.kind)) return requireSuccess(first);

  const credential = await navigator.credentials.get({
    publicKey: decodeRequestOptions(first.step.data),
  });
  if (!(credential instanceof PublicKeyCredential)) {
    throw new Error("The browser returned no passkey.");
  }
  // The DOM response type is AuthenticatorAssertionResponse for get().
  const response = credential.response as AuthenticatorAssertionResponse;
  const assertion: AssertionLike = {
    id: credential.id,
    rawId: credential.rawId,
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    response,
    getClientExtensionResults: () => credential.getClientExtensionResults(),
  };
  return requireSuccess(
    await continueAuth(SERVICE, {
      login_id: first.login_id,
      response: encodeAssertion(assertion),
    }),
  );
}

/** Password and passkey sign-in. Success refetches whoami so AuthGate swaps the page. */
export function useLogin() {
  const qc = useQueryClient();
  return {
    password: useMutation({
      mutationFn: loginWithPassword,
      onSuccess: () => qc.invalidateQueries({ queryKey: qk("whoami") }),
    }),
    passkey: useMutation({
      mutationFn: loginWithPasskey,
      onSuccess: () => qc.invalidateQueries({ queryKey: qk("whoami") }),
    }),
  };
}

export type MethodsState =
  | { state: "loading" }
  /** `methods` is undefined when the daemon could not be asked. */
  | { state: "ready"; methods: string[] | undefined };

/**
 * Asks the daemon which interactive login methods the `ui` stack offers. An
 * empty `begin` presents no credentials: it either ends in a 401 that lists
 * the methods, or in a passkey challenge (which carries them too).
 */
export function useLoginMethods(): MethodsState {
  const [result, setResult] = useState<MethodsState>({ state: "loading" });
  const qc = useQueryClient();
  useEffect(() => {
    let live = true;
    beginAuth(SERVICE).then(
      (r) => {
        if (!live) return;
        if (r.status === "success") {
          void qc.invalidateQueries({ queryKey: qk("whoami") });
          return;
        }
        setResult({ state: "ready", methods: r.methods });
      },
      (e: unknown) => {
        if (!live) return;
        const methods =
          e instanceof ApiError ? methodsFrom(e.problem) : undefined;
        setResult({ state: "ready", methods });
      },
    );
    return () => {
      live = false;
    };
  }, [qc]);
  return result;
}

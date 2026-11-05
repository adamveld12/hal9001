import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  beginPasskeyRegistration,
  finishPasskeyRegistration,
  type Passkey,
} from "../../api/auth";
import { qk } from "../../api/keys";
import {
  decodeCreationOptions,
  describeWebAuthnError,
  encodeAttestation,
  type AttestationLike,
} from "./webauthn";

/** Registers a passkey for the host name this page was loaded from. */
export async function registerPasskey(label?: string): Promise<Passkey> {
  const begin = await beginPasskeyRegistration(label);
  let credential: Credential | null;
  try {
    credential = await navigator.credentials.create({
      publicKey: decodeCreationOptions(begin.options),
    });
  } catch (e) {
    throw new Error(describeWebAuthnError(e, "register"), { cause: e });
  }
  if (!(credential instanceof PublicKeyCredential)) {
    throw new Error("The browser returned no passkey.");
  }
  // The DOM response type is AuthenticatorAttestationResponse for create().
  const response = credential.response as AuthenticatorAttestationResponse;
  const attestation: AttestationLike = {
    id: credential.id,
    rawId: credential.rawId,
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    response: {
      attestationObject: response.attestationObject,
      clientDataJSON: response.clientDataJSON,
      getTransports: () => response.getTransports(),
    },
    getClientExtensionResults: () => credential.getClientExtensionResults(),
  };
  return finishPasskeyRegistration({
    registration_id: begin.registration_id,
    credential: encodeAttestation(attestation),
  });
}

export function useRegisterPasskey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (label: string) => registerPasskey(label.trim() || undefined),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: qk("passkeys") }),
        qc.invalidateQueries({ queryKey: qk("auth-events") }),
      ]),
  });
}

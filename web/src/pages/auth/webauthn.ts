// Pure conversions between the daemon's WebAuthn JSON (base64url strings, as
// emitted and parsed by go-webauthn) and the ArrayBuffer-based structures that
// navigator.credentials.get/create take and return.

type JsonObject = Record<string, unknown>;

/** Encode bytes as unpadded base64url. */
export function bufferToBase64url(buf: ArrayBuffer | ArrayBufferView): string {
  const bytes =
    buf instanceof ArrayBuffer
      ? new Uint8Array(buf)
      : new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

/** Decode base64url (padded or not). Throws on characters outside the alphabet. */
export function base64urlToBuffer(s: string): ArrayBuffer {
  if (!/^[A-Za-z0-9_-]*={0,2}$/.test(s)) {
    throw new Error("invalid base64url value");
  }
  const std = s.replaceAll("-", "+").replaceAll("_", "/").replace(/=+$/, "");
  const padded = std + "=".repeat((4 - (std.length % 4)) % 4);
  const bin = atob(padded);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

function isObject(v: unknown): v is JsonObject {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function requireString(obj: JsonObject, key: string, where: string): string {
  const v = obj[key];
  if (typeof v !== "string") throw new Error(`${where}: ${key} is missing`);
  return v;
}

/** go-webauthn wraps options in `{publicKey: {...}}`; accept bare options too. */
function publicKeyOptions(data: unknown): JsonObject {
  if (!isObject(data)) throw new Error("WebAuthn options are missing");
  const inner = data.publicKey;
  return isObject(inner) ? inner : data;
}

function decodeDescriptors(list: unknown, where: string): JsonObject[] {
  if (list === undefined || list === null) return [];
  if (!Array.isArray(list)) throw new Error(`${where} must be a list`);
  return list.map((d) => {
    if (!isObject(d)) throw new Error(`${where} entry is invalid`);
    return { ...d, id: base64urlToBuffer(requireString(d, "id", where)) };
  });
}

/** Options for `navigator.credentials.get({publicKey})` from a login challenge's `step.data`. */
export function decodeRequestOptions(
  data: unknown,
): PublicKeyCredentialRequestOptions {
  const o = publicKeyOptions(data);
  const decoded: JsonObject = {
    ...o,
    challenge: base64urlToBuffer(requireString(o, "challenge", "assertion")),
  };
  if (o.allowCredentials !== undefined) {
    decoded.allowCredentials = decodeDescriptors(
      o.allowCredentials,
      "allowCredentials",
    );
  }
  return decoded as unknown as PublicKeyCredentialRequestOptions;
}

/** Options for `navigator.credentials.create({publicKey})` from `PasskeyRegisterBegin.options`. */
export function decodeCreationOptions(
  data: unknown,
): PublicKeyCredentialCreationOptions {
  const o = publicKeyOptions(data);
  const user = o.user;
  if (!isObject(user)) throw new Error("registration: user is missing");
  const decoded: JsonObject = {
    ...o,
    challenge: base64urlToBuffer(requireString(o, "challenge", "registration")),
    user: { ...user, id: base64urlToBuffer(requireString(user, "id", "user")) },
  };
  if (o.excludeCredentials !== undefined) {
    decoded.excludeCredentials = decodeDescriptors(
      o.excludeCredentials,
      "excludeCredentials",
    );
  }
  return decoded as unknown as PublicKeyCredentialCreationOptions;
}

/** The parts of an assertion credential that encodeAssertion reads. */
export interface AssertionLike {
  id: string;
  rawId: ArrayBuffer;
  type: string;
  authenticatorAttachment?: string | null;
  response: {
    authenticatorData: ArrayBuffer;
    clientDataJSON: ArrayBuffer;
    signature: ArrayBuffer;
    userHandle: ArrayBuffer | null;
  };
  getClientExtensionResults?: () => unknown;
}

/** The parts of an attestation credential that encodeAttestation reads. */
export interface AttestationLike {
  id: string;
  rawId: ArrayBuffer;
  type: string;
  authenticatorAttachment?: string | null;
  response: {
    attestationObject: ArrayBuffer;
    clientDataJSON: ArrayBuffer;
    getTransports?: () => string[];
  };
  getClientExtensionResults?: () => unknown;
}

/** JSON for `/continue`'s `response`, in the shape go-webauthn parses. */
export function encodeAssertion(c: AssertionLike): JsonObject {
  const response: JsonObject = {
    authenticatorData: bufferToBase64url(c.response.authenticatorData),
    clientDataJSON: bufferToBase64url(c.response.clientDataJSON),
    signature: bufferToBase64url(c.response.signature),
  };
  const handle = c.response.userHandle;
  if (handle && handle.byteLength > 0) {
    response.userHandle = bufferToBase64url(handle);
  }
  return credentialJson(c, response);
}

/** JSON for `register/finish`'s `credential`, in the shape go-webauthn parses. */
export function encodeAttestation(c: AttestationLike): JsonObject {
  const response: JsonObject = {
    attestationObject: bufferToBase64url(c.response.attestationObject),
    clientDataJSON: bufferToBase64url(c.response.clientDataJSON),
  };
  const transports = c.response.getTransports?.() ?? [];
  if (transports.length > 0) response.transports = transports;
  return credentialJson(c, response);
}

function credentialJson(
  c: AssertionLike | AttestationLike,
  response: JsonObject,
): JsonObject {
  const out: JsonObject = {
    id: c.id,
    rawId: bufferToBase64url(c.rawId),
    type: c.type,
    response,
    clientExtensionResults: c.getClientExtensionResults?.() ?? {},
  };
  if (c.authenticatorAttachment) {
    out.authenticatorAttachment = c.authenticatorAttachment;
  }
  return out;
}

/** True when the challenge step asks for a passkey assertion. */
export function isAssertionStep(kind: string): boolean {
  return kind === "webauthn.assertion";
}

/** True when this browser can create and use passkeys. */
export function passkeysSupported(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.PublicKeyCredential === "function" &&
    typeof navigator.credentials?.get === "function"
  );
}

/** User-facing text for an error thrown by navigator.credentials.get/create. */
export function describeWebAuthnError(
  e: unknown,
  action: "sign in" | "register",
): string {
  const name = e instanceof Error ? e.name : "";
  switch (name) {
    case "NotAllowedError":
      return `Passkey ${action} was cancelled or timed out.`;
    case "InvalidStateError":
      return action === "register"
        ? "This authenticator already has a passkey for this host."
        : "No matching passkey on this device.";
    case "SecurityError":
      return "Passkeys need a secure origin and a host name the daemon allows.";
    case "NotSupportedError":
      return "This browser or device does not support the requested passkey type.";
    default:
      return e instanceof Error ? e.message : String(e);
  }
}

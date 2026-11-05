import { describe, expect, it } from "vitest";
import {
  base64urlToBuffer,
  bufferToBase64url,
  decodeCreationOptions,
  decodeRequestOptions,
  describeWebAuthnError,
  encodeAssertion,
  encodeAttestation,
  isAssertionStep,
} from "./webauthn";

const bytes = (...n: number[]) => new Uint8Array(n).buffer;
const toArray = (b: ArrayBuffer) => Array.from(new Uint8Array(b));

describe("base64url", () => {
  it("round-trips every byte length remainder and the full byte range", () => {
    for (let len = 0; len < 12; len++) {
      const src = new Uint8Array(len).map((_, i) => (i * 37 + 250) % 256);
      const enc = bufferToBase64url(src);
      expect(enc).not.toMatch(/[+/=]/);
      expect(toArray(base64urlToBuffer(enc))).toEqual(Array.from(src));
    }
    const all = new Uint8Array(256).map((_, i) => i);
    expect(toArray(base64urlToBuffer(bufferToBase64url(all)))).toEqual(
      Array.from(all),
    );
  });

  it("uses the url-safe alphabet without padding", () => {
    // 0xfb 0xff 0xbe is "+/++" in standard base64.
    expect(bufferToBase64url(bytes(0xfb, 0xff, 0xbe))).toBe("-_--");
    expect(bufferToBase64url(bytes(0xff))).toBe("_w");
  });

  it("accepts padded input and rejects characters outside the alphabet", () => {
    expect(toArray(base64urlToBuffer("_w=="))).toEqual([0xff]);
    expect(() => base64urlToBuffer("ab+/")).toThrow();
    expect(() => base64urlToBuffer("a b")).toThrow();
  });

  it("encodes typed-array views by their own window, not the whole buffer", () => {
    const view = new Uint8Array([9, 1, 2, 3, 9]).subarray(1, 4);
    expect(bufferToBase64url(view)).toBe(bufferToBase64url(bytes(1, 2, 3)));
  });
});

describe("decodeRequestOptions", () => {
  const challenge = bufferToBase64url(bytes(1, 2, 3, 4));
  const credId = bufferToBase64url(bytes(7, 8));

  it("unwraps go-webauthn's publicKey envelope and decodes binary fields", () => {
    const o = decodeRequestOptions({
      publicKey: {
        challenge,
        rpId: "localhost",
        timeout: 60000,
        userVerification: "required",
        allowCredentials: [{ type: "public-key", id: credId }],
      },
    });
    expect(toArray(o.challenge as ArrayBuffer)).toEqual([1, 2, 3, 4]);
    expect(o.rpId).toBe("localhost");
    expect(o.timeout).toBe(60000);
    expect(o.userVerification).toBe("required");
    const allow = o.allowCredentials?.[0];
    expect(allow?.type).toBe("public-key");
    expect(toArray(allow?.id as ArrayBuffer)).toEqual([7, 8]);
  });

  it("accepts bare options and discoverable-credential requests", () => {
    const o = decodeRequestOptions({ challenge });
    expect(toArray(o.challenge as ArrayBuffer)).toEqual([1, 2, 3, 4]);
    expect(o.allowCredentials).toBeUndefined();
  });

  it("fails clearly when the challenge is missing", () => {
    expect(() => decodeRequestOptions({ publicKey: {} })).toThrow(/challenge/);
    expect(() => decodeRequestOptions(null)).toThrow();
  });
});

describe("decodeCreationOptions", () => {
  it("decodes challenge, user id, and excluded credentials", () => {
    const o = decodeCreationOptions({
      publicKey: {
        rp: { name: "hal9001", id: "localhost" },
        user: {
          id: bufferToBase64url(bytes(5, 6)),
          name: "owner",
          displayName: "hal9001 owner",
        },
        challenge: bufferToBase64url(bytes(9)),
        pubKeyCredParams: [{ type: "public-key", alg: -7 }],
        excludeCredentials: [
          { type: "public-key", id: bufferToBase64url(bytes(1)) },
        ],
      },
    });
    expect(toArray(o.challenge as ArrayBuffer)).toEqual([9]);
    expect(toArray(o.user.id as ArrayBuffer)).toEqual([5, 6]);
    expect(o.user.name).toBe("owner");
    expect(o.rp.id).toBe("localhost");
    expect(o.pubKeyCredParams).toEqual([{ type: "public-key", alg: -7 }]);
    expect(toArray(o.excludeCredentials?.[0]?.id as ArrayBuffer)).toEqual([1]);
  });

  it("requires a user", () => {
    expect(() =>
      decodeCreationOptions({ publicKey: { challenge: "AA" } }),
    ).toThrow(/user/);
  });
});

describe("encodeAssertion", () => {
  const base = {
    id: "abc",
    rawId: bytes(1, 2),
    type: "public-key",
    response: {
      authenticatorData: bytes(3),
      clientDataJSON: bytes(4, 5),
      signature: bytes(0xff),
      userHandle: null as ArrayBuffer | null,
    },
  };

  it("emits base64url strings and omits an empty user handle", () => {
    const json = encodeAssertion(base);
    expect(json).toEqual({
      id: "abc",
      rawId: "AQI",
      type: "public-key",
      response: {
        authenticatorData: "Aw",
        clientDataJSON: "BAU",
        signature: "_w",
      },
      clientExtensionResults: {},
    });
  });

  it("includes the user handle, attachment, and extension results when present", () => {
    const json = encodeAssertion({
      ...base,
      authenticatorAttachment: "platform",
      response: { ...base.response, userHandle: bytes(6) },
      getClientExtensionResults: () => ({ appid: true }),
    });
    expect(json.authenticatorAttachment).toBe("platform");
    expect(json.clientExtensionResults).toEqual({ appid: true });
    expect((json.response as Record<string, unknown>).userHandle).toBe("Bg");
  });
});

describe("encodeAttestation", () => {
  it("emits the attestation object and transports when the browser reports them", () => {
    const json = encodeAttestation({
      id: "id1",
      rawId: bytes(1),
      type: "public-key",
      response: {
        attestationObject: bytes(0xde, 0xad),
        clientDataJSON: bytes(1),
        getTransports: () => ["internal", "hybrid"],
      },
    });
    expect(json.response).toEqual({
      attestationObject: "3q0",
      clientDataJSON: "AQ",
      transports: ["internal", "hybrid"],
    });
    expect(json.rawId).toBe("AQ");
  });

  it("omits transports when none are reported", () => {
    const json = encodeAttestation({
      id: "id1",
      rawId: bytes(1),
      type: "public-key",
      response: {
        attestationObject: bytes(1),
        clientDataJSON: bytes(1),
        getTransports: () => [],
      },
    });
    expect(json.response).not.toHaveProperty("transports");
  });
});

describe("isAssertionStep", () => {
  it("matches the daemon's step kind and the documented spelling only", () => {
    expect(isAssertionStep("webauthn.assertion")).toBe(true);
    expect(isAssertionStep("webauthn_assertion")).toBe(true);
    expect(isAssertionStep("password")).toBe(false);
    expect(isAssertionStep("webauthn.attestation")).toBe(false);
  });
});

describe("describeWebAuthnError", () => {
  it("maps a dismissed prompt to a plain message", () => {
    const e = Object.assign(new Error("x"), { name: "NotAllowedError" });
    expect(describeWebAuthnError(e, "sign in")).toBe(
      "Passkey sign in was cancelled or timed out.",
    );
  });

  it("falls back to the error message", () => {
    expect(describeWebAuthnError(new Error("boom"), "register")).toBe("boom");
  });
});

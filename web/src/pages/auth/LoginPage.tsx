import { useState, type FormEvent } from "react";
import { Button, Field, Spinner, TextInput } from "../../components/ui";
import { useLogin, useLoginMethods } from "./login";
import { loginErrorMessage, type LoginMethod } from "./loginErrors";
import { passkeysSupported } from "./webauthn";

/** Full-screen sign-in, shown by AuthGate when whoami says `authenticated: false`. */
export function LoginPage() {
  const methods = useLoginMethods();
  const { password: passwordLogin, passkey: passkeyLogin } = useLogin();
  const [password, setPassword] = useState("");
  const [failed, setFailed] = useState<{
    method: LoginMethod;
    error: unknown;
  } | null>(null);

  const known = methods.state === "ready" ? methods.methods : undefined;
  const offerPassword = known === undefined || known.includes("password");
  const offerPasskey =
    passkeysSupported() && (known === undefined || known.includes("webauthn"));
  const busy = passwordLogin.isPending || passkeyLogin.isPending;

  const submitPassword = (e: FormEvent) => {
    e.preventDefault();
    if (password === "") return;
    setFailed(null);
    passwordLogin.mutate(password, {
      onError: (error) => {
        setPassword("");
        setFailed({ method: "password", error });
      },
    });
  };

  const submitPasskey = () => {
    setFailed(null);
    passkeyLogin.mutate(undefined, {
      onError: (error) => setFailed({ method: "webauthn", error }),
    });
  };

  return (
    <main className="flex min-h-screen items-center justify-center bg-bg p-4">
      <div className="w-full max-w-sm space-y-4 rounded-md border border-line bg-panel p-5">
        <div>
          <h1 className="m-0 text-base font-semibold">hal9001</h1>
          <p className="m-0 text-xs text-muted">Sign in to continue.</p>
        </div>

        {methods.state === "loading" && (
          <div className="py-4">
            <Spinner label="Checking sign-in methods" />
          </div>
        )}

        {methods.state === "ready" && !offerPassword && !offerPasskey && (
          <p role="alert" className="m-0 text-xs text-muted">
            {known !== undefined && known.length === 0
              ? "No interactive sign-in is configured for the ui service. Access depends on where the request comes from."
              : "This browser cannot use any sign-in method the daemon offers."}
          </p>
        )}

        {methods.state === "ready" && offerPassword && (
          <form onSubmit={submitPassword} className="space-y-3">
            <Field label="Password">
              <TextInput
                type="password"
                name="password"
                autoComplete="current-password"
                autoFocus
                value={password}
                disabled={busy}
                invalid={failed?.method === "password"}
                onChange={(e) => setPassword(e.target.value)}
              />
            </Field>
            <Button
              type="submit"
              variant="primary"
              className="w-full justify-center"
              disabled={busy || password === ""}
            >
              {passwordLogin.isPending ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        )}

        {methods.state === "ready" && offerPassword && offerPasskey && (
          <div className="text-center text-[11px] text-muted">or</div>
        )}

        {methods.state === "ready" && offerPasskey && (
          <Button
            className="w-full justify-center"
            disabled={busy}
            onClick={submitPasskey}
          >
            {passkeyLogin.isPending
              ? "Waiting for passkey…"
              : "Sign in with passkey"}
          </Button>
        )}

        {failed && (
          <p role="alert" className="m-0 text-xs text-bad">
            {loginErrorMessage(failed.error, failed.method)}
          </p>
        )}
      </div>
    </main>
  );
}

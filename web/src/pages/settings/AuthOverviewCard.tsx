import { useState } from "react";
import {
  useAuthOverview,
  useUpdateAuthMethods,
  type AuthMethod,
  type AuthMethodsRequest,
  type AuthOverview,
} from "../../api/auth";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { Badge, ErrorState, LoadingState, Panel } from "../../components/ui";
import { STATE_LABEL, STATE_TONE } from "./authView";
import { AuthSwitch } from "./AuthSwitch";
import { ChangeStatus } from "./ChangeStatus";
import { ConfirmAuthDialog } from "./ConfirmAuthDialog";
import { useConfirmedChange } from "./useConfirmedChange";

type Switchable = keyof Omit<AuthMethodsRequest, "confirm">;

const REQUEST_KEY: Partial<Record<AuthMethod["id"], Switchable>> = {
  tailscale: "tailscale",
  passkeys: "passkeys",
  password: "password",
};

/** One row per way in: its state, why, and a switch where the method can be turned on or off. */
export function AuthOverviewCard({
  onSetPassword,
}: {
  /** Turning the password on needs one: the page brings the Password card into view. */
  onSetPassword: () => void;
}) {
  const overview = useAuthOverview();
  const update = useUpdateAuthMethods();
  const change = useConfirmedChange<AuthMethodsRequest>(update.mutateAsync);
  const [forgetPassword, setForgetPassword] = useState(false);

  const toggle = (overviewData: AuthOverview, m: AuthMethod, next: boolean) => {
    const key = REQUEST_KEY[m.id];
    if (!key) return;
    if (key === "password") {
      if (next && !overviewData.password.set) {
        onSetPassword();
        return;
      }
      // Turning it off forgets the stored password; say so before doing it.
      if (!next && overviewData.password.set) {
        setForgetPassword(true);
        return;
      }
    }
    void change.run({ [key]: next });
  };

  return (
    <Panel title="Ways in">
      {overview.isPending && <LoadingState />}
      {overview.isError && (
        <ErrorState
          error={overview.error}
          onRetry={() => void overview.refetch()}
        />
      )}
      {overview.isSuccess && (
        <div className="space-y-3">
          <ul
            className="m-0 list-none divide-y divide-line p-0"
            data-testid="auth-overview"
          >
            {overview.data.methods.map((m) => (
              <MethodRow
                key={m.id}
                method={m}
                isCurrent={overview.data.current.method === m.id}
                busy={change.busy}
                onToggle={(next) => toggle(overview.data, m, next)}
              />
            ))}
          </ul>
          <ChangeStatus result={change.result} error={change.error} />
        </div>
      )}

      <ConfirmAuthDialog
        confirmation={change.confirmation}
        busy={change.busy}
        onCancel={change.cancel}
        onConfirm={() => void change.confirm()}
      />
      <ConfirmDialog
        open={forgetPassword}
        testId="auth-forget-password-dialog"
        title="Turn off password sign-in?"
        confirmLabel="Turn off and forget"
        busy={change.busy}
        onCancel={() => setForgetPassword(false)}
        onConfirm={() => {
          setForgetPassword(false);
          void change.run({ password: false });
        }}
      >
        <p className="m-0">
          This also forgets the password. To use a password again you will set a
          new one.
        </p>
      </ConfirmDialog>
    </Panel>
  );
}

function MethodRow({
  method: m,
  isCurrent,
  busy,
  onToggle,
}: {
  method: AuthMethod;
  isCurrent: boolean;
  busy: boolean;
  onToggle: (next: boolean) => void;
}) {
  return (
    <li
      className="flex items-start gap-3 py-2 first:pt-0 last:pb-0"
      data-testid={`auth-method-${m.id}`}
    >
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span className="font-medium">{m.name}</span>
          <Badge tone={STATE_TONE[m.state]}>{STATE_LABEL[m.state]}</Badge>
          {m.custom && <Badge tone="info">custom setup</Badge>}
          {isCurrent && <Badge tone="muted">this session</Badge>}
        </div>
        <p className="m-0 text-xs text-muted">{m.detail}</p>
        {m.custom && (
          <p className="m-0 text-[11px] text-muted">
            The stacks are set up by hand. The switch rewrites them as described
            under Advanced.
          </p>
        )}
      </div>
      {m.toggleable ? (
        <AuthSwitch
          checked={m.enabled}
          label={`${m.name} sign-in`}
          disabled={busy}
          onChange={onToggle}
        />
      ) : (
        <span className="text-xs text-muted">Always on</span>
      )}
    </li>
  );
}

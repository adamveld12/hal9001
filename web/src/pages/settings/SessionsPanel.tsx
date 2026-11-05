import { errorMessage } from "../../api/client";
import {
  useAuthSessions,
  useLogout,
  useRevokeAllSessions,
  useRevokeSession,
} from "../../api/auth";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  Panel,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { ConfirmButton } from "./ConfirmButton";

export function SessionsPanel() {
  const sessions = useAuthSessions();
  const revoke = useRevokeSession();
  const revokeAll = useRevokeAllSessions();
  const logout = useLogout();
  const error = revoke.error ?? revokeAll.error ?? logout.error;
  const list = sessions.data?.sessions ?? [];

  return (
    <Panel
      title="Sessions"
      actions={
        <>
          <Button onClick={() => logout.mutate()} disabled={logout.isPending}>
            Sign out
          </Button>
          <ConfirmButton
            confirmLabel="Revoke all, including this one?"
            disabled={revokeAll.isPending || list.length === 0}
            onConfirm={() => revokeAll.mutate()}
          >
            Revoke all
          </ConfirmButton>
        </>
      }
    >
      {sessions.isPending && <LoadingState />}
      {sessions.isError && (
        <ErrorState
          error={sessions.error}
          onRetry={() => void sessions.refetch()}
        />
      )}
      {sessions.isSuccess && list.length === 0 && (
        <EmptyState
          title="No active sessions"
          hint="Requests allowed by the stack itself, such as localhost trust, don't create one."
        />
      )}
      {sessions.isSuccess && list.length > 0 && (
        <Table>
          <thead>
            <tr>
              <Th>Principal</Th>
              <Th>Services</Th>
              <Th>Modules</Th>
              <Th>Host</Th>
              <Th>Created</Th>
              <Th>Last seen</Th>
              <Th>Expires</Th>
              <Th className="w-px" />
            </tr>
          </thead>
          <tbody>
            {list.map((s) => (
              <tr key={s.id}>
                <Td>
                  {s.principal}
                  {s.id === sessions.data.current && (
                    <Badge tone="info" className="ml-2">
                      this session
                    </Badge>
                  )}
                </Td>
                <Td>
                  <div className="flex flex-wrap gap-1">
                    {s.services.map((v) => (
                      <Badge key={v}>{v}</Badge>
                    ))}
                  </div>
                </Td>
                <Td className="font-mono text-muted">{s.modules.join(", ")}</Td>
                <Td className="text-muted">
                  {[s.host, s.remote_addr].filter(Boolean).join(" · ") || "–"}
                </Td>
                <Td>
                  <Time ts={s.created_at} />
                </Td>
                <Td>
                  <Time ts={s.last_seen_at} />
                </Td>
                <Td>
                  <Time ts={s.expires_at} />
                </Td>
                <Td>
                  <ConfirmButton
                    disabled={revoke.isPending}
                    onConfirm={() => revoke.mutate(s.id)}
                  >
                    Revoke
                  </ConfirmButton>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      {error && (
        <p role="alert" className="mb-0 mt-3 text-xs text-bad">
          {errorMessage(error)}
        </p>
      )}
    </Panel>
  );
}

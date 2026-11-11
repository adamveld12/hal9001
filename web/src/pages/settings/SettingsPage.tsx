import { useAuthSessions, usePasskeys } from "../../api/auth";
import { useSettings } from "../../api/settings";
import { usePageContext } from "../../layout/pageContext";
import { factList } from "../../lib/contextSummary";
import { Navigate, useSearchParams } from "react-router";
import { PageHeader } from "../../components/ui";
import { stackViews } from "./authView";
import { AuthEventsPanel } from "./AuthEventsPanel";
import { AuthStackPanel, TailscalePanel } from "./AuthConfigPanels";
import { ContextSection } from "./ContextSection";
import { EnvSection } from "./EnvSection";
import { GeneralSection } from "./GeneralSection";
import { McpSection } from "./McpSection";
import { PasskeysPanel } from "./PasskeysPanel";
import { RuntimesSection } from "./RuntimesSection";
import { TailscaleSection } from "./TailscaleSection";
import { SessionsPanel } from "./SessionsPanel";
import { UpdatesSection } from "./UpdatesSection";

function SettingsPageShell({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <>
      <PageHeader title={title} />
      <div className="mx-auto max-w-4xl space-y-4 p-4">{children}</div>
    </>
  );
}

/** Old `/settings?tab=<key>` links map to the per-tab routes. */
const LEGACY_TAB_ROUTES: Record<string, string> = {
  general: "/settings/general",
  context: "/settings/context",
  env: "/settings/environment",
  runtimes: "/settings/runtimes",
  auth: "/settings/auth",
};

/** `/settings` goes to General, or to the tab named by a legacy `?tab=`. */
export function SettingsRedirect() {
  const [params] = useSearchParams();
  const to = LEGACY_TAB_ROUTES[params.get("tab") ?? ""] ?? "/settings/general";
  return <Navigate to={to} replace />;
}

export function SettingsGeneralPage() {
  return (
    <SettingsPageShell title="General">
      <GeneralSection />
    </SettingsPageShell>
  );
}

export function SettingsContextPage() {
  return (
    <SettingsPageShell title="Context">
      <ContextSection />
    </SettingsPageShell>
  );
}

export function SettingsEnvironmentPage() {
  return (
    <SettingsPageShell title="Environment">
      <EnvSection />
    </SettingsPageShell>
  );
}

export function SettingsRuntimesPage() {
  return (
    <SettingsPageShell title="Runtimes">
      <RuntimesSection />
    </SettingsPageShell>
  );
}

export function SettingsUpdatesPage() {
  return (
    <SettingsPageShell title="Updates">
      <UpdatesSection />
    </SettingsPageShell>
  );
}

export function SettingsAuthPage() {
  useAuthPageContext();
  return (
    <SettingsPageShell title="Auth">
      <SessionsPanel />
      <PasskeysPanel />
      <AuthStackPanel />
      <TailscalePanel />
      <AuthEventsPanel />
    </SettingsPageShell>
  );
}

/** Counts and names of the auth setup; never session ids, passkey ids or stack options. */
function useAuthPageContext() {
  const settings = useSettings();
  const passkeys = usePasskeys();
  const sessions = useAuthSessions();
  usePageContext(
    settings.data
      ? {
          title: "Settings: Auth",
          facts: factList([
            ["passkeys", passkeys.data?.credentials.length],
            ["active sessions", sessions.data?.sessions.length],
          ]),
          entities: stackViews(settings.data.config.auth).map((s) => ({
            kind: "auth_stack",
            id: s.name,
            detail: `guards ${s.services.join(", ") || "nothing"}; modules ${s.entries.map((e) => `${e.module} (${e.control})`).join(", ")}`,
          })),
        }
      : null,
  );
}

export function SettingsMcpPage() {
  return (
    <SettingsPageShell title="MCP">
      <McpSection />
    </SettingsPageShell>
  );
}

export function SettingsTailscalePage() {
  return (
    <SettingsPageShell title="Tailscale">
      <TailscaleSection />
    </SettingsPageShell>
  );
}

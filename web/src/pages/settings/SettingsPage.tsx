import { Navigate, useSearchParams } from "react-router";
import { PageHeader } from "../../components/ui";
import { AuthEventsPanel } from "./AuthEventsPanel";
import { AuthStackPanel, TailscalePanel } from "./AuthConfigPanels";
import { EnvSection } from "./EnvSection";
import { GeneralSection } from "./GeneralSection";
import { PasskeysPanel } from "./PasskeysPanel";
import { RuntimesSection } from "./RuntimesSection";
import { SessionsPanel } from "./SessionsPanel";

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

export function SettingsAuthPage() {
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

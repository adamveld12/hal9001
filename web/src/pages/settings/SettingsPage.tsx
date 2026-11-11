import { useEffect } from "react";
import { Navigate, useLocation, useSearchParams } from "react-router";
import { useAuthOverview, useAuthStacks } from "../../api/auth";
import { useTailscaleStatus } from "../../api/tailscale";
import { usePageContext } from "../../layout/pageContext";
import { PageHeader } from "../../components/ui";
import { AuthAdvancedCard } from "./AuthAdvancedCard";
import { AuthEventsPanel } from "./AuthEventsPanel";
import { AuthOverviewCard } from "./AuthOverviewCard";
import { authPageContext } from "./authView";
import { scrollTargetId } from "./authRoutes";
import { ContextSection } from "./ContextSection";
import { EnvSection } from "./EnvSection";
import { GeneralSection } from "./GeneralSection";
import { McpSection } from "./McpSection";
import { PasskeysPanel } from "./PasskeysPanel";
import {
  NEW_PASSWORD_INPUT_ID,
  PASSWORD_CARD_ID,
  PasswordCard,
} from "./PasswordCard";
import { RuntimesSection } from "./RuntimesSection";
import { SessionsPanel } from "./SessionsPanel";
import { TailscaleCard } from "./TailscaleCard";
import { UpdatesSection } from "./UpdatesSection";

function SettingsPageShell({
  title,
  intro,
  children,
}: {
  title: string;
  /** One line under the header. */
  intro?: string;
  children: React.ReactNode;
}) {
  return (
    <>
      <PageHeader title={title} />
      <div className="mx-auto max-w-4xl space-y-4 p-4">
        {intro && <p className="m-0 text-xs text-muted">{intro}</p>}
        {children}
      </div>
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
  const overview = useAuthOverview();
  const { hash } = useLocation();
  // `#tailscale` and the like: scroll to the card, again once the cards above it have their content.
  useEffect(() => {
    const id = scrollTargetId(hash);
    if (id) document.getElementById(id)?.scrollIntoView({ block: "start" });
  }, [hash, overview.isSuccess]);

  const showPasswordForm = () => {
    document
      .getElementById(PASSWORD_CARD_ID)
      ?.scrollIntoView({ behavior: "smooth", block: "start" });
    document
      .getElementById(NEW_PASSWORD_INPUT_ID)
      ?.focus({ preventScroll: true });
  };

  return (
    <SettingsPageShell
      title="Auth"
      intro="How you sign in to hal: which ways in are on, and who can use them."
    >
      <AuthOverviewCard onSetPassword={showPasswordForm} />
      <TailscaleCard />
      <PasswordCard />
      <PasskeysPanel />
      <SessionsPanel />
      <AuthEventsPanel />
      <AuthAdvancedCard />
    </SettingsPageShell>
  );
}

/** Which ways in are on, whether a password is set, Tailscale sharing and the stacks by name; never logins, ids or options. */
function useAuthPageContext() {
  const overview = useAuthOverview();
  const stacks = useAuthStacks();
  const tailscale = useTailscaleStatus();
  usePageContext(
    authPageContext({
      overview: overview.data,
      stacks: stacks.data,
      tailscale: tailscale.data,
    }),
  );
}

export function SettingsMcpPage() {
  return (
    <SettingsPageShell title="MCP">
      <McpSection />
    </SettingsPageShell>
  );
}

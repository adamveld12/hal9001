import { useSearchParams } from "react-router";
import { Tabs, type TabItem } from "../../components/Tabs";
import { PageHeader } from "../../components/ui";
import { AuthEventsPanel } from "./AuthEventsPanel";
import { AuthStackPanel, TailscalePanel } from "./AuthConfigPanels";
import { EnvSection } from "./EnvSection";
import { GeneralSection } from "./GeneralSection";
import { PasskeysPanel } from "./PasskeysPanel";
import { RuntimesSection } from "./RuntimesSection";
import { SessionsPanel } from "./SessionsPanel";

const TABS = [
  { key: "general", label: "General" },
  { key: "env", label: "Environment" },
  { key: "runtimes", label: "Runtimes" },
  { key: "auth", label: "Auth" },
] as const satisfies readonly TabItem<string>[];

type TabKey = (typeof TABS)[number]["key"];

function parseTab(v: string | null): TabKey {
  return TABS.find((t) => t.key === v)?.key ?? "general";
}

export function SettingsPage() {
  const [params, setParams] = useSearchParams();
  const tab = parseTab(params.get("tab"));

  return (
    <>
      <PageHeader title="Settings" />
      <Tabs
        tabs={TABS}
        active={tab}
        onChange={(key) =>
          setParams(key === "general" ? {} : { tab: key }, { replace: true })
        }
      />
      <div className="mx-auto max-w-4xl space-y-4 p-4">
        {tab === "general" && <GeneralSection />}
        {tab === "env" && <EnvSection />}
        {tab === "runtimes" && <RuntimesSection />}
        {tab === "auth" && (
          <>
            <SessionsPanel />
            <PasskeysPanel />
            <AuthStackPanel />
            <TailscalePanel />
            <AuthEventsPanel />
          </>
        )}
      </div>
    </>
  );
}

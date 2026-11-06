import {
  QueryCache,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { lazy, Suspense, useState, type ComponentType } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router";
import { ApiError } from "./api/client";
import { qk } from "./api/keys";
import { useWhoAmI } from "./api/status";
import { StreamProvider } from "./api/stream";
import { ErrorState, LoadingState } from "./components/ui";
import { AppShell } from "./layout/AppShell";

/** Code-split a named page export. */
function page<K extends string>(
  load: () => Promise<Record<K, ComponentType>>,
  name: K,
) {
  return lazy(async () => ({ default: (await load())[name] }));
}

const LoginPage = page(() => import("./pages/auth/LoginPage"), "LoginPage");
const TimelinePage = page(
  () => import("./pages/timeline/TimelinePage"),
  "TimelinePage",
);
const EventDetailPage = page(
  () => import("./pages/timeline/EventDetailPage"),
  "EventDetailPage",
);
const RunDetailPage = page(
  () => import("./pages/runs/RunDetailPage"),
  "RunDetailPage",
);
const AgentRunPage = page(
  () => import("./pages/runs/AgentRunPage"),
  "AgentRunPage",
);
const AgentRunsPage = page(
  () => import("./pages/runs/AgentRunsPage"),
  "AgentRunsPage",
);
const FlowVersionPage = page(
  () => import("./pages/runs/FlowVersionPage"),
  "FlowVersionPage",
);
const InboxPage = page(() => import("./pages/inbox/InboxPage"), "InboxPage");
const ApprovalsPage = page(
  () => import("./pages/approvals/ApprovalsPage"),
  "ApprovalsPage",
);
const FlowsPage = page(() => import("./pages/flows/FlowsPage"), "FlowsPage");
const FlowEditorPage = page(
  () => import("./pages/flows/FlowEditorPage"),
  "FlowEditorPage",
);
const SensorsPage = page(
  () => import("./pages/sensors/SensorsPage"),
  "SensorsPage",
);
const PromptsPage = page(
  () => import("./pages/prompts/PromptsPage"),
  "PromptsPage",
);
const SettingsPage = page(
  () => import("./pages/settings/SettingsPage"),
  "SettingsPage",
);

function makeQueryClient(): QueryClient {
  const client: QueryClient = new QueryClient({
    queryCache: new QueryCache({
      // Any 401 means the session is gone or never existed: re-check who we are.
      onError: (error) => {
        if (error instanceof ApiError && error.status === 401) {
          void client.invalidateQueries({ queryKey: qk("whoami") });
        }
      },
    }),
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        // 4xx answers are deterministic; only retry transport failures and 5xx.
        retry: (count, error) =>
          !(error instanceof ApiError && error.status < 500) && count < 2,
      },
    },
  });
  return client;
}

function AuthGate() {
  const who = useWhoAmI();
  if (who.isPending) return <LoadingState label="Connecting" />;
  if (who.isError)
    return <ErrorState error={who.error} onRetry={() => void who.refetch()} />;
  if (!who.data.authenticated) return <LoginPage />;
  return (
    <StreamProvider>
      <Routes>
        <Route element={<AppShell />}>
          <Route index element={<Navigate to="/timeline" replace />} />
          <Route path="timeline" element={<TimelinePage />} />
          <Route path="events/:id" element={<EventDetailPage />} />
          <Route path="runs/:id" element={<RunDetailPage />} />
          <Route path="agent-runs" element={<AgentRunsPage />} />
          <Route path="agent-runs/:id" element={<AgentRunPage />} />
          <Route path="inbox" element={<InboxPage />} />
          <Route path="inbox/:id" element={<InboxPage />} />
          <Route path="approvals" element={<ApprovalsPage />} />
          <Route path="approvals/:id" element={<ApprovalsPage />} />
          <Route path="flows" element={<FlowsPage />} />
          <Route path="flows/:id" element={<FlowEditorPage />} />
          <Route
            path="flows/:id/versions/:hash"
            element={<FlowVersionPage />}
          />
          <Route path="sensors" element={<SensorsPage />} />
          <Route path="sensors/:id" element={<SensorsPage />} />
          <Route path="prompts" element={<PromptsPage />} />
          <Route path="prompts/:name" element={<PromptsPage />} />
          <Route path="settings" element={<SettingsPage />} />
          <Route path="*" element={<Navigate to="/timeline" replace />} />
        </Route>
      </Routes>
    </StreamProvider>
  );
}

export default function App() {
  const [client] = useState(makeQueryClient);
  return (
    <QueryClientProvider client={client}>
      <BrowserRouter>
        <Suspense fallback={<LoadingState />}>
          <AuthGate />
        </Suspense>
      </BrowserRouter>
    </QueryClientProvider>
  );
}

import { useState, type ReactNode } from "react";
import {
  useCheckUpdate,
  useInstallUpdate,
  useUpdateStatus,
} from "../api/update";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Button } from "../components/ui";
import { bannerView, refusal } from "./updateView";

const DISMISSED_ITEM = "hal9001.update.dismissed";

function readDismissed(): string | null {
  try {
    return localStorage.getItem(DISMISSED_ITEM);
  } catch {
    return null;
  }
}

function ManualHint({
  detail,
  releaseUrl,
}: {
  detail?: string;
  releaseUrl: string;
}) {
  return (
    <span>
      {detail ?? "Open the HAL9001 app to install, or download the build "}
      {releaseUrl && (
        <>
          {" "}
          <a
            className="underline"
            href={releaseUrl}
            target="_blank"
            rel="noreferrer"
          >
            from the release page
          </a>
        </>
      )}
      .
    </span>
  );
}

function Bar({
  tone,
  children,
}: {
  tone: "accent" | "warn" | "bad";
  children: ReactNode;
}) {
  const cls = {
    accent: "border-accent/40 bg-accent/5",
    warn: "border-warn/40 bg-warn/5",
    bad: "border-bad/40 bg-bad/5",
  }[tone];
  return (
    <div
      role="status"
      className={`flex flex-wrap items-center gap-3 border-b px-4 py-2 text-xs ${cls}`}
    >
      {children}
    </div>
  );
}

/** App-wide update notice above the page content. */
export function UpdateBanner() {
  const status = useUpdateStatus();
  const install = useInstallUpdate();
  const check = useCheckUpdate();
  const [dismissed, setDismissed] = useState(readDismissed);
  const [confirmForce, setConfirmForce] = useState(false);

  const view = bannerView(status.data, dismissed);
  if (view.kind === "hidden") return null;

  const dismiss = (version: string) => {
    try {
      localStorage.setItem(DISMISSED_ITEM, version);
    } catch {
      // not persisted
    }
    setDismissed(version);
  };
  const manual = refusal(install.error);
  const releaseUrl = status.data?.latest?.notes_url ?? "";

  switch (view.kind) {
    case "installing":
      return (
        <Bar tone="accent">
          <span>Installing update ({view.phase})… HAL9001 will restart.</span>
        </Bar>
      );
    case "error":
      return (
        <Bar tone="bad">
          <span>Update failed: {view.message}</span>
          <Button
            className="ml-auto"
            icon="refresh"
            disabled={check.isPending}
            onClick={() => check.mutate()}
          >
            Check again
          </Button>
        </Bar>
      );
    case "pending":
      return (
        <Bar tone="warn">
          <span>
            Update {view.version} will install when {view.running} running flow
            {view.running === 1 ? "" : "s"} finish.
          </span>
          <Button
            className="ml-auto"
            variant="danger"
            disabled={install.isPending}
            onClick={() => setConfirmForce(true)}
          >
            Force install now
          </Button>
          <ConfirmDialog
            open={confirmForce}
            title="Force install now?"
            confirmLabel="Install and restart"
            busy={install.isPending}
            onCancel={() => setConfirmForce(false)}
            onConfirm={() =>
              install.mutate(true, { onSettled: () => setConfirmForce(false) })
            }
          >
            Running flows will be interrupted when HAL9001 restarts.
          </ConfirmDialog>
        </Bar>
      );
    case "available":
      return (
        <Bar tone="accent">
          <span>
            Update available: HAL9001 {view.version}
            {view.notesUrl && (
              <>
                {" "}
                <a
                  className="underline"
                  href={view.notesUrl}
                  target="_blank"
                  rel="noreferrer"
                >
                  Release notes
                </a>
              </>
            )}
          </span>
          {(manual || view.hint) && (
            <ManualHint
              detail={manual?.detail}
              releaseUrl={manual?.releaseUrl || releaseUrl}
            />
          )}
          {install.isError && !manual && (
            <span className="text-bad">{install.error.message}</span>
          )}
          <span className="ml-auto flex gap-2">
            <Button
              variant="primary"
              disabled={install.isPending}
              onClick={() => install.mutate(false)}
            >
              Install &amp; Restart
            </Button>
            <Button variant="ghost" onClick={() => dismiss(view.version)}>
              Dismiss
            </Button>
          </span>
        </Bar>
      );
  }
}
